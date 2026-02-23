package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/elhamdev/gpu-guardian/internal/adapter"
	"github.com/elhamdev/gpu-guardian/internal/control"
	"github.com/elhamdev/gpu-guardian/internal/engine"
	"github.com/elhamdev/gpu-guardian/internal/logger"
	"github.com/elhamdev/gpu-guardian/internal/telemetry"
	"github.com/elhamdev/gpu-guardian/internal/throughput"
)

const (
	DefaultListenAddress = "127.0.0.1:8090"
	APIVersion           = "v1"
	defaultSessionID     = "default"

	defaultRecoveryMaxRetries  = 1
	defaultRecoveryCooldownSec = 1
	maxSessionErrors           = 20
)

type SessionMode string

type SessionGoal string

const (
	SessionModeStateless SessionMode = "stateless"
	SessionModeStateful  SessionMode = "stateful"

	SessionGoalRun        SessionGoal = "run"
	SessionGoalRecovering SessionGoal = "recovering"
	SessionGoalPaused     SessionGoal = "paused"
	SessionGoalStopped    SessionGoal = "stopped"
)

type StartRequest struct {
	Command                          string  `json:"command"`
	PollIntervalSec                  int     `json:"poll_interval_sec"`
	SoftTemp                         float64 `json:"soft_temp"`
	HardTemp                         float64 `json:"hard_temp"`
	MinConcurrency                   int     `json:"min_concurrency"`
	MaxConcurrency                   int     `json:"max_concurrency"`
	StartConcurrency                 int     `json:"start_concurrency"`
	ThroughputFloorRatio             float64 `json:"throughput_floor_ratio"`
	ThroughputSlowdownFloorRatio     float64 `json:"throughput_slowdown_floor_ratio"`
	TempHysteresisC                  float64 `json:"temp_hysteresis_c"`
	ThroughputRecoveryMargin         float64 `json:"throughput_recovery_margin"`
	ThroughputRecoveryMaxAttempts    int     `json:"throughput_recovery_max_attempts"`
	ThroughputRecoveryStepMultiplier int     `json:"throughput_recovery_step_multiplier"`
	MemoryPressureLimit              float64 `json:"memory_pressure_limit"`
	ThrottleRiskLimit                float64 `json:"throttle_risk_limit"`
	TelemetryLogPath                 string  `json:"telemetry_log_path"`
	AdjustmentCooldownSec            int     `json:"adjustment_cooldown_sec"`
	MaxConcurrencyStep               int     `json:"max_concurrency_step"`
	BaselineWindowSec                int     `json:"baseline_window_sec"`
	ThroughputWindowSec              int     `json:"throughput_window_sec"`
	ThroughputFloorWindowSec         int     `json:"throughput_floor_window_sec"`
	AdapterStopTimeoutSec            int     `json:"adapter_stop_timeout_sec"`
	LogPath                          string  `json:"log_file"`
	LogMaxSizeMB                     int     `json:"log_max_size_mb"`
	WorkloadLogPath                  string  `json:"workload_log_path"`
	EchoWorkloadOutput               bool    `json:"echo_workload_output"`
	InitialBaselineThroughput        float64 `json:"initial_baseline_throughput"`
	MaxTicks                         int     `json:"max_ticks"`
	Mode                             string  `json:"mode"`
	CheckpointPath                   string  `json:"checkpoint_path"`
	RecoveryMaxRetries               int     `json:"recovery_max_retries"`
	RecoveryCooldownSec              int     `json:"recovery_cooldown_sec"`
}

type HealthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

type ControlRequest struct {
	Action string `json:"action"`
}

type SessionResponse struct {
	SessionID string `json:"session_id"`
	Reason    string `json:"reason"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

type SessionState struct {
	ID               string                    `json:"id"`
	Running          bool                      `json:"running"`
	State            engine.RunState           `json:"state"`
	StartedAt        time.Time                 `json:"started_at"`
	StoppedAt        time.Time                 `json:"stopped_at"`
	LastReason       string                    `json:"last_reason"`
	LastError        string                    `json:"last_error,omitempty"`
	Mode             string                    `json:"mode"`
	Goal             string                    `json:"goal"`
	LastAction       control.Action            `json:"last_action"`
	LastSample       telemetry.TelemetrySample `json:"last_sample"`
	Retries          int                       `json:"retries"`
	Errors           []string                  `json:"errors"`
	CheckpointPath   string                    `json:"checkpoint_path"`
	TelemetryLogPath string                    `json:"telemetry_log_path"`
}

type SessionStateResponse struct {
	SessionState
}

// Server is the daemon process HTTP facade.
type Server struct {
	mu      sync.Mutex
	running map[string]*sessionState
	ln      string
}

type sessionState struct {
	id                 string
	running            bool
	startedAt          time.Time
	stoppedAt          time.Time
	result             *engine.EngineResult
	lastErr            string
	cancel             context.CancelFunc
	mode               string
	goal               string
	lastAction         control.Action
	lastSample         telemetry.TelemetrySample
	retries            int
	errors             []string
	checkpointPath     string
	telemetryLogPath   string
	recoveryMaxRetries int
	recoveryCooldown   time.Duration
}

func NewServer(listen string) *Server {
	return &Server{
		ln:      listen,
		running: map[string]*sessionState{},
	}
}

// Serve starts the daemon API server.
func (s *Server) Serve() error {
	h := s.handler()
	srv := &http.Server{Addr: s.ln, Handler: h}
	return srv.ListenAndServe()
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler().ServeHTTP(w, r)
}

func (s *Server) handler() http.Handler {
	h := http.NewServeMux()
	h.HandleFunc("/v1/health", s.handleHealth)
	h.HandleFunc("/v1/metrics", s.handleMetrics)
	h.HandleFunc("/v1/control", s.handleControl)
	h.HandleFunc("/v1/sessions", s.handleSessions)
	h.HandleFunc("/v1/sessions/", s.handleSession)
	return h
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, HealthResponse{Status: "ok", Version: APIVersion})
}

func (s *Server) handleControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}

	var req ControlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid control request")
		return
	}

	switch strings.ToLower(strings.TrimSpace(req.Action)) {
	case "stop":
		if err := s.stopSession(defaultSessionID); err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, SessionResponse{SessionID: defaultSessionID, Reason: "stopped"})
	default:
		writeError(w, http.StatusBadRequest, "unsupported control action")
	}
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	runningCount := 0
	for _, sess := range s.running {
		if sess.running {
			runningCount++
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"version":          APIVersion,
		"sessions_total":   len(s.running),
		"sessions_running": runningCount,
	})
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/sessions" {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.mu.Lock()
		defer s.mu.Unlock()
		out := make([]SessionState, 0, len(s.running))
		for id, sess := range s.running {
			out = append(out, s.sessionSnapshot(id, sess))
		}
		writeJSON(w, http.StatusOK, out)
	case http.MethodPost:
		var req StartRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid request: %v", err))
			return
		}
		sid, err := s.startSession(context.Background(), req)
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, SessionResponse{SessionID: sid, Reason: "started"})
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, "/v1/sessions/")
	parts := strings.Split(strings.TrimSpace(trimmed), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}

	id := parts[0]
	if len(parts) == 1 {
		s.mu.Lock()
		sess := s.running[id]
		snapshot := s.sessionSnapshot(id, sess)
		s.mu.Unlock()

		if sess == nil {
			http.NotFound(w, r)
			return
		}

		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, snapshot)
		default:
			http.NotFound(w, r)
		}
		return
	}

	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}

	switch parts[1] {
	case "stop":
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if err := s.stopSession(id); err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, SessionResponse{SessionID: id, Reason: "stopped"})
	case "control":
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var req ControlRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid control request")
			return
		}
		if strings.EqualFold(strings.TrimSpace(req.Action), "stop") {
			if err := s.stopSession(id); err != nil {
				writeError(w, http.StatusNotFound, err.Error())
				return
			}
			writeJSON(w, http.StatusAccepted, SessionResponse{SessionID: id, Reason: "stopped"})
			return
		}
		writeError(w, http.StatusBadRequest, "unsupported control action")
	case "telemetry":
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		s.mu.Lock()
		sess := s.running[id]
		if sess == nil {
			s.mu.Unlock()
			http.NotFound(w, r)
			return
		}
		telemetrySample := interface{}(sess.lastSample)
		if sess.result != nil {
			telemetrySample = sess.result.State.LastTelemetry
		}
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"session_id": id,
			"telemetry":  telemetrySample,
		})
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) sessionSnapshot(id string, sess *sessionState) SessionState {
	if sess == nil {
		return SessionState{}
	}
	sessState := SessionState{
		ID:               id,
		Mode:             sess.mode,
		Goal:             sess.goal,
		LastAction:       sess.lastAction,
		LastSample:       sess.lastSample,
		Retries:          sess.retries,
		Errors:           append([]string(nil), sess.errors...),
		CheckpointPath:   sess.checkpointPath,
		TelemetryLogPath: sess.telemetryLogPath,
	}
	sessState.Running = sess.running
	sessState.StartedAt = sess.startedAt
	sessState.StoppedAt = sess.stoppedAt
	if sess.result != nil {
		sessState.State = sess.result.State
		sessState.LastReason = sess.result.Reason
		sessState.LastError = sess.lastErr
	}
	return sessState
}

func (s *Server) startSession(ctx context.Context, req StartRequest) (string, error) {
	normalizeStartRequest(&req)
	applyStatefulCheckpointDefaults(&req)
	if strings.TrimSpace(req.Command) == "" {
		return "", errors.New("command is required")
	}

	s.mu.Lock()
	if existing, ok := s.running[defaultSessionID]; ok && existing.running {
		s.mu.Unlock()
		return "", fmt.Errorf("session %s already running", defaultSessionID)
	}

	engineCfg := StartRequestToEngineConfig(req)
	loggerSink, err := logger.New(req.LogPath, int64(req.LogMaxSizeMB)*1024*1024, true)
	if err != nil {
		s.mu.Unlock()
		return "", err
	}

	adapterCfg := adapter.Config{
		OutputPath:  req.WorkloadLogPath,
		StopTimeout: time.Duration(req.AdapterStopTimeoutSec) * time.Second,
		EchoOutput:  req.EchoWorkloadOutput,
	}
	controlCfg := control.RuleConfig{
		SoftTemp:                         req.SoftTemp,
		HardTemp:                         req.HardTemp,
		ThroughputFloorRatio:             req.ThroughputFloorRatio,
		ThroughputSlowdownFloorRatio:     req.ThroughputSlowdownFloorRatio,
		ThroughputWindowSec:              req.ThroughputWindowSec,
		ThroughputFloorSec:               req.ThroughputFloorWindowSec,
		ThroughputRecoveryMaxAttempts:    req.ThroughputRecoveryMaxAttempts,
		ThroughputRecoveryStepMultiplier: req.ThroughputRecoveryStepMultiplier,
		TempHysteresisC:                  req.TempHysteresisC,
		ThroughputRecoveryMargin:         req.ThroughputRecoveryMargin,
		MemoryPressureLimit:              req.MemoryPressureLimit,
		ThrottleRiskLimit:                req.ThrottleRiskLimit,
		MaxConcurrencyStep:               req.MaxConcurrencyStep,
	}
	controller := control.NewRuleController(controlCfg)

	tickerCfg := throughput.NewTracker(time.Duration(req.ThroughputWindowSec)*time.Second, time.Duration(req.BaselineWindowSec)*time.Second)
	sess := &sessionState{
		id:                 defaultSessionID,
		running:            true,
		startedAt:          time.Now(),
		goal:               string(SessionGoalRun),
		recoveryMaxRetries: req.RecoveryMaxRetries,
		recoveryCooldown:   time.Duration(req.RecoveryCooldownSec) * time.Second,
	}
	s.ensureModeDefaults(&req, sess)

	runCtx, cancel := context.WithCancel(ctx)
	sess.cancel = cancel
	s.running[defaultSessionID] = sess
	s.mu.Unlock()
	s.persistSessionState(sess)
	go func() {
		defer loggerSink.Close()

		for {
			if runCtx.Err() != nil {
				break
			}

			aw := adapter.NewXttsAdapter(adapterCfg)
			eng := engine.New(
				engineCfg,
				aw,
				controller,
				telemetry.NewCollector(),
				tickerCfg,
				loggerSink,
			)
			result, runErr := eng.Start(runCtx)

			s.mu.Lock()
			sess.result = result
			sess.lastErr = ""
			sess.goal = string(SessionGoalStopped)
			if result != nil {
				sess.lastAction = result.State.LastAction
				sess.lastSample = result.State.LastTelemetry
				sess.lastErr = result.Reason
			}
			if runErr != nil {
				sess.lastErr = runErr.Error()
				sess.errors = appendError(sess.errors, runErr.Error())
			}

			if runErr == nil {
				sess.running = false
				if result != nil && result.State.LastAction.Type == control.ActionPause {
					sess.goal = string(SessionGoalPaused)
				} else {
					sess.goal = string(SessionGoalStopped)
				}
				sess.stoppedAt = time.Now()
				s.mu.Unlock()
				s.persistSessionState(sess)
				return
			}

			if !isRecoverableFailure(result, runErr) {
				sess.running = false
				sess.goal = string(SessionGoalStopped)
				sess.stoppedAt = time.Now()
				s.mu.Unlock()
				s.persistSessionState(sess)
				return
			}

			if sess.retries >= sess.recoveryMaxRetries {
				sess.running = false
				sess.goal = string(SessionGoalPaused)
				sess.stoppedAt = time.Now()
				_ = aw.Pause(context.Background())
				s.mu.Unlock()
				s.persistSessionState(sess)
				return
			}

			sess.retries++
			sess.goal = string(SessionGoalRecovering)
			s.mu.Unlock()
			s.persistSessionState(sess)

			if !waitWithContext(runCtx, sess.recoveryCooldown) {
				return
			}
		}
		s.mu.Lock()
		sess.running = false
		sess.goal = string(SessionGoalStopped)
		sess.stoppedAt = time.Now()
		s.mu.Unlock()
		s.persistSessionState(sess)
	}()

	return defaultSessionID, nil
}

func (s *Server) stopSession(id string) error {
	s.mu.Lock()
	sess := s.running[id]
	if sess == nil {
		s.mu.Unlock()
		return fmt.Errorf("session %s not found", id)
	}
	if !sess.running {
		sess.goal = string(SessionGoalPaused)
		sess.stoppedAt = time.Now()
		s.mu.Unlock()
		s.persistSessionState(sess)
		return nil
	}

	sess.cancel()
	sess.goal = string(SessionGoalPaused)
	sess.stoppedAt = time.Now()
	s.mu.Unlock()
	s.persistSessionState(sess)
	return nil
}

func StartRequestToEngineConfig(req StartRequest) engine.Config {
	return engine.Config{
		Command:                   req.Command,
		PollInterval:              time.Duration(req.PollIntervalSec) * time.Second,
		SoftTemp:                  req.SoftTemp,
		HardTemp:                  req.HardTemp,
		MinConcurrency:            req.MinConcurrency,
		MaxConcurrency:            req.MaxConcurrency,
		StartConcurrency:          req.StartConcurrency,
		ThroughputFloorRatio:      req.ThroughputFloorRatio,
		AdjustmentCooldown:        time.Duration(req.AdjustmentCooldownSec) * time.Second,
		ThroughputWindow:          time.Duration(req.ThroughputWindowSec) * time.Second,
		ThroughputFloorWindow:     time.Duration(req.ThroughputFloorWindowSec) * time.Second,
		BaselineWindow:            time.Duration(req.BaselineWindowSec) * time.Second,
		MaxConcurrencyStep:        req.MaxConcurrencyStep,
		TelemetryLogPath:          req.TelemetryLogPath,
		MaxTicks:                  req.MaxTicks,
		InitialBaselineThroughput: req.InitialBaselineThroughput,
	}
}

func normalizeStartRequest(req *StartRequest) {
	req.Mode = strings.ToLower(strings.TrimSpace(req.Mode))
	if req.Mode != string(SessionModeStateful) {
		req.Mode = string(SessionModeStateless)
	}
	if req.TempHysteresisC <= 0 {
		req.TempHysteresisC = 2
	}
	if req.ThroughputRecoveryMargin <= 0 {
		req.ThroughputRecoveryMargin = 0.05
	}
	if req.MemoryPressureLimit <= 0 {
		req.MemoryPressureLimit = 0.9
	}
	if req.ThrottleRiskLimit <= 0 {
		req.ThrottleRiskLimit = 0.85
	}
	if req.TelemetryLogPath == "" {
		req.TelemetryLogPath = "guardian.telemetry.log"
	}
	if req.PollIntervalSec <= 0 {
		req.PollIntervalSec = 2
	}
	if req.SoftTemp <= 0 {
		req.SoftTemp = 78
	}
	if req.HardTemp <= 0 {
		req.HardTemp = 84
	}
	if req.MinConcurrency <= 0 {
		req.MinConcurrency = 1
	}
	if req.MaxConcurrency <= 0 {
		req.MaxConcurrency = req.MinConcurrency
	}
	if req.MinConcurrency > req.MaxConcurrency {
		req.MinConcurrency = req.MaxConcurrency
	}
	if req.StartConcurrency < req.MinConcurrency {
		req.StartConcurrency = req.MinConcurrency
	}
	if req.StartConcurrency > req.MaxConcurrency {
		req.StartConcurrency = req.MaxConcurrency
	}
	if req.ThroughputFloorRatio <= 0 {
		req.ThroughputFloorRatio = 0.7
	}
	if req.ThroughputSlowdownFloorRatio <= 0 || req.ThroughputSlowdownFloorRatio > req.ThroughputFloorRatio {
		req.ThroughputSlowdownFloorRatio = 0.5
	}
	if req.AdjustmentCooldownSec <= 0 {
		req.AdjustmentCooldownSec = 10
	}
	if req.MaxConcurrencyStep <= 0 {
		req.MaxConcurrencyStep = 1
	}
	if req.ThroughputRecoveryMaxAttempts <= 0 {
		req.ThroughputRecoveryMaxAttempts = 3
	}
	if req.ThroughputRecoveryStepMultiplier <= 1 {
		req.ThroughputRecoveryStepMultiplier = 2
	}
	if req.BaselineWindowSec <= 0 {
		req.BaselineWindowSec = 120
	}
	if req.ThroughputWindowSec <= 0 {
		req.ThroughputWindowSec = 30
	}
	if req.ThroughputFloorWindowSec <= 0 {
		req.ThroughputFloorWindowSec = 30
	}
	if req.AdapterStopTimeoutSec <= 0 {
		req.AdapterStopTimeoutSec = 5
	}
	if req.LogMaxSizeMB <= 0 {
		req.LogMaxSizeMB = 50
	}
	if req.MaxTicks < 0 {
		req.MaxTicks = 0
	}
	if req.RecoveryMaxRetries < 0 {
		req.RecoveryMaxRetries = 0
	}
	if req.RecoveryMaxRetries == 0 {
		req.RecoveryMaxRetries = defaultRecoveryMaxRetries
	}
	if req.RecoveryCooldownSec <= 0 {
		req.RecoveryCooldownSec = defaultRecoveryCooldownSec
	}
}

func applyStatefulCheckpointDefaults(req *StartRequest) {
	if req == nil || strings.TrimSpace(req.Mode) != string(SessionModeStateful) {
		return
	}
	if strings.TrimSpace(req.CheckpointPath) == "" {
		return
	}
	profile, err := readSessionCheckpoint(req.CheckpointPath)
	if err != nil {
		return
	}
	if profile.State.CurrentConcurrency > 0 {
		req.StartConcurrency = clampInt(profile.State.CurrentConcurrency, req.MinConcurrency, req.MaxConcurrency)
	}
	if profile.State.BaselineThroughput > 0 {
		req.InitialBaselineThroughput = profile.State.BaselineThroughput
	}
}

func readSessionCheckpoint(path string) (SessionState, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return SessionState{}, err
	}
	var snapshot SessionState
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return SessionState{}, err
	}
	return snapshot, nil
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func (s *Server) ensureModeDefaults(req *StartRequest, sess *sessionState) {
	sess.mode = req.Mode
	sess.telemetryLogPath = req.TelemetryLogPath
	if sess.mode != string(SessionModeStateful) {
		sess.telemetryLogPath = ""
		sess.checkpointPath = ""
		return
	}

	if req.CheckpointPath == "" {
		req.CheckpointPath = makeCheckpointPath()
	}
	sess.telemetryLogPath = req.TelemetryLogPath
	sess.checkpointPath = req.CheckpointPath
}

func makeCheckpointPath() string {
	return fmt.Sprintf("/tmp/guardian-session-%d.json", time.Now().UnixNano())
}

func appendError(entries []string, entry string) []string {
	entries = append(entries, entry)
	if len(entries) <= maxSessionErrors {
		return entries
	}
	return entries[len(entries)-maxSessionErrors:]
}

func isRecoverableFailure(result *engine.EngineResult, err error) bool {
	if err == nil {
		return false
	}
	if result != nil && result.Reason == "workload_exited_unexpectedly" {
		return true
	}
	return false
}

func waitWithContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *Server) persistSessionState(sess *sessionState) {
	if sess == nil || sess.mode != string(SessionModeStateful) || sess.checkpointPath == "" {
		return
	}

	s.mu.Lock()
	snapshot := s.sessionSnapshot(sess.id, sess)
	s.mu.Unlock()
	body, err := json.Marshal(snapshot)
	if err != nil {
		return
	}
	_ = os.WriteFile(sess.checkpointPath, body, 0o600)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("content-type", "application/json")
	w.Header().Set("X-API-Version", APIVersion)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Error: msg})
}
