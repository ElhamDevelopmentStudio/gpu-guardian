package engine

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/elhamdev/gpu-guardian/internal/control"
	"github.com/elhamdev/gpu-guardian/internal/logger"
	"github.com/elhamdev/gpu-guardian/internal/telemetry"
	"github.com/elhamdev/gpu-guardian/internal/throughput"
)

const API_VERSION = "v1"

const maxTelemetrySamples = 300

// EventLogger is the engine logging abstraction exposed by the API boundary.
//
// The logger is intentionally dependency-injected so the CLI and daemon layers can route
// engine events to different sinks without coupling engine internals.
type EventLogger interface {
	Info(string, logger.Entry)
	Warn(string, logger.Entry)
	Error(string, logger.Entry)
}

// WorkloadAdapter is the engine-facing workload control contract.
//
// It intentionally stays small and stable in this phase so daemon/API layers can
// reuse it and alternate runtimes can be swapped behind it.
type WorkloadAdapter interface {
	Start(ctx context.Context, cmd string, concurrency int) error
	Pause(ctx context.Context) error
	Resume(ctx context.Context) error
	UpdateParameters(ctx context.Context, concurrency int) error
	GetThroughput() uint64
	GetProgress() float64
	Restart(ctx context.Context, concurrency int) error
	Stop() error
	GetPID() int
	IsRunning() bool
	OutputBytes() uint64
}

// Config contains canonical engine bootstrap parameters.
type Config struct {
	APIVersion                string        `json:"api_version"`
	Command                   string        `json:"command"`
	PollInterval              time.Duration `json:"poll_interval"`
	SoftTemp                  float64       `json:"soft_temp"`
	HardTemp                  float64       `json:"hard_temp"`
	MinConcurrency            int           `json:"min_concurrency"`
	MaxConcurrency            int           `json:"max_concurrency"`
	StartConcurrency          int           `json:"start_concurrency"`
	ThroughputFloorRatio      float64       `json:"throughput_floor_ratio"`
	AdjustmentCooldown        time.Duration `json:"adjustment_cooldown"`
	ThroughputWindow          time.Duration `json:"throughput_window"`
	ThroughputFloorWindow     time.Duration `json:"throughput_floor_window"`
	BaselineWindow            time.Duration `json:"baseline_window"`
	MaxConcurrencyStep        int           `json:"max_concurrency_step"`
	MaxTicks                  int           `json:"max_ticks"`
	TelemetryLogPath          string        `json:"telemetry_log_path"`
	InitialBaselineThroughput float64       `json:"initial_baseline_throughput"`
}

// RunState exposes the latest engine decision context and runtime snapshot.
type RunState struct {
	Ticks              int                       `json:"ticks"`
	CurrentConcurrency int                       `json:"current_concurrency"`
	LastAction         control.Action            `json:"last_action"`
	LastActionAt       time.Time                 `json:"last_action_at"`
	LastTelemetryAt    time.Time                 `json:"last_telemetry_at"`
	LastTelemetry      telemetry.TelemetrySample `json:"last_telemetry"`
	LastThroughput     throughput.Sample         `json:"last_throughput"`
	BaselineThroughput float64                   `json:"baseline_throughput"`
	Estimate           control.StateEstimate     `json:"estimate"`
	ProcessPID         int                       `json:"process_pid"`
}

// LifecyclePhase captures explicit high-level engine execution state.
type LifecyclePhase string

const (
	LifecycleIdle     LifecyclePhase = "idle"
	LifecycleStarting LifecyclePhase = "starting"
	LifecycleRunning  LifecyclePhase = "running"
	LifecycleStopping LifecyclePhase = "stopping"
	LifecycleStopped  LifecyclePhase = "stopped"
	LifecycleFailed   LifecyclePhase = "failed"
)

// Lifecycle exposes explicit engine execution state for API/state reporting.
type Lifecycle struct {
	Phase     LifecyclePhase `json:"phase"`
	Reason    string         `json:"reason,omitempty"`
	Error     string         `json:"error,omitempty"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// EngineResult summarizes a completed run.
type EngineResult struct {
	State     RunState  `json:"state"`
	StoppedAt time.Time `json:"stopped_at"`
	Reason    string    `json:"reason"`
}

// Engine is the canonical stable Go control engine.
type Engine struct {
	cfg                Config
	logger             EventLogger
	adapter            WorkloadAdapter
	controller         control.Controller
	telemetryCollector *telemetry.Collector
	throughputTracker  *throughput.Tracker
	telemetryStore     *telemetry.SampleStore
	stateEstimator     *control.StateEstimator
	nowFn              func() time.Time
	lifecycleMu        sync.Mutex
	lifecycle          Lifecycle
}

// New builds a canonical Engine instance.
func New(
	cfg Config,
	adapter WorkloadAdapter,
	controller control.Controller,
	telemetryCollector *telemetry.Collector,
	throughputTracker *throughput.Tracker,
	loggerSink EventLogger,
) *Engine {
	if cfg.APIVersion == "" {
		cfg.APIVersion = API_VERSION
	}
	if loggerSink == nil {
		loggerSink = &noopLogger{}
	}
	if telemetryCollector == nil {
		telemetryCollector = telemetry.NewCollector()
	}
	if throughputTracker == nil {
		throughputTracker = throughput.NewTracker(cfg.ThroughputWindow, cfg.BaselineWindow)
	}
	stateEstimator := control.NewStateEstimator()
	return &Engine{
		cfg:                cfg,
		logger:             loggerSink,
		adapter:            adapter,
		controller:         controller,
		telemetryCollector: telemetryCollector,
		throughputTracker:  throughputTracker,
		telemetryStore:     nil,
		stateEstimator:     stateEstimator,
		nowFn:              time.Now,
		lifecycle:          Lifecycle{Phase: LifecycleIdle, UpdatedAt: time.Now()},
	}
}

func (e *Engine) setLifecycle(phase LifecyclePhase, reason string, err error) {
	e.lifecycleMu.Lock()
	defer e.lifecycleMu.Unlock()

	e.lifecycle.Phase = phase
	e.lifecycle.Reason = reason
	if err != nil {
		e.lifecycle.Error = err.Error()
	} else {
		e.lifecycle.Error = ""
	}
	e.lifecycle.UpdatedAt = e.nowFn()
}

// Lifecycle returns a snapshot of the current engine lifecycle state.
func (e *Engine) Lifecycle() Lifecycle {
	e.lifecycleMu.Lock()
	defer e.lifecycleMu.Unlock()
	return e.lifecycle
}

func appendTelemetry(samples []telemetry.TelemetrySample, s telemetry.TelemetrySample) []telemetry.TelemetrySample {
	samples = append(samples, s)
	if len(samples) <= maxTelemetrySamples {
		return samples
	}
	return samples[len(samples)-maxTelemetrySamples:]
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

func actionMinCooldown(cfg time.Duration, overrideSec float64) time.Duration {
	if overrideSec <= 0 {
		return cfg
	}
	requested := time.Duration(overrideSec * float64(time.Second))
	if requested > cfg {
		return requested
	}
	return cfg
}

func isDirectionalAction(t control.ActionType) bool {
	return t == control.ActionIncrease || t == control.ActionDecrease
}

func isOppositeDirection(prev, next control.ActionType) bool {
	return prev == control.ActionIncrease && next == control.ActionDecrease ||
		prev == control.ActionDecrease && next == control.ActionIncrease
}

func boundedStepDelta(current, target, min, max, step int) int {
	target = clampInt(target, min, max)
	if step <= 1 {
		step = 1
	}
	delta := target - current
	if delta > step {
		return current + step
	}
	if delta < -step {
		return current - step
	}
	return target
}

func (e *Engine) ensureConfig() error {
	if e.cfg.Command == "" {
		return fmt.Errorf("command is required")
	}
	if e.cfg.PollInterval <= 0 {
		e.cfg.PollInterval = 2 * time.Second
	}
	if e.cfg.AdjustmentCooldown <= 0 {
		e.cfg.AdjustmentCooldown = 10 * time.Second
	}
	if e.cfg.SoftTemp <= 0 {
		e.cfg.SoftTemp = 78
	}
	if e.cfg.HardTemp <= 0 {
		e.cfg.HardTemp = 84
	}
	if e.cfg.MinConcurrency <= 0 {
		e.cfg.MinConcurrency = 1
	}
	if e.cfg.MaxConcurrency <= 0 {
		e.cfg.MaxConcurrency = e.cfg.MinConcurrency
	}
	if e.cfg.MinConcurrency > e.cfg.MaxConcurrency {
		e.cfg.MinConcurrency = e.cfg.MaxConcurrency
	}
	if e.cfg.StartConcurrency < e.cfg.MinConcurrency {
		e.cfg.StartConcurrency = e.cfg.MinConcurrency
	}
	if e.cfg.StartConcurrency > e.cfg.MaxConcurrency {
		e.cfg.StartConcurrency = e.cfg.MaxConcurrency
	}
	if e.cfg.ThroughputFloorRatio <= 0 {
		e.cfg.ThroughputFloorRatio = 0.7
	}
	if e.cfg.ThroughputWindow <= 0 {
		e.cfg.ThroughputWindow = 30 * time.Second
	}
	if e.cfg.BaselineWindow <= 0 {
		e.cfg.BaselineWindow = 120 * time.Second
	}
	if e.cfg.ThroughputFloorWindow <= 0 {
		e.cfg.ThroughputFloorWindow = 30 * time.Second
	}
	if e.cfg.MaxTicks < 0 {
		e.cfg.MaxTicks = 0
	}
	if e.cfg.MaxConcurrencyStep <= 0 {
		e.cfg.MaxConcurrencyStep = 1
	}
	if e.controller == nil {
		return fmt.Errorf("controller is required")
	}
	if e.adapter == nil {
		return fmt.Errorf("workload adapter is required")
	}
	return nil
}

// Start launches the workload and executes the control loop.
func (e *Engine) Start(ctx context.Context) (*EngineResult, error) {
	e.setLifecycle(LifecycleStarting, "starting", nil)
	if err := e.ensureConfig(); err != nil {
		e.setLifecycle(LifecycleFailed, "invalid_config", err)
		return nil, err
	}
	if err := e.ensureTelemetryStore(); err != nil {
		e.setLifecycle(LifecycleFailed, "telemetry_store_failed", err)
		return nil, err
	}
	if e.telemetryStore != nil {
		defer e.telemetryStore.Close()
	}

	e.logDebug("starting engine", map[string]interface{}{
		"version":                e.cfg.APIVersion,
		"command":                e.cfg.Command,
		"poll_interval_seconds":  e.cfg.PollInterval.Seconds(),
		"min_concurrency":        e.cfg.MinConcurrency,
		"max_concurrency":        e.cfg.MaxConcurrency,
		"start_concurrency":      e.cfg.StartConcurrency,
		"soft_temp":              e.cfg.SoftTemp,
		"hard_temp":              e.cfg.HardTemp,
		"throughput_floor_ratio": e.cfg.ThroughputFloorRatio,
	})

	state := RunState{CurrentConcurrency: e.cfg.StartConcurrency}
	if e.cfg.InitialBaselineThroughput > 0 {
		e.throughputTracker.RestoreBaseline(e.cfg.InitialBaselineThroughput)
		state.BaselineThroughput = e.cfg.InitialBaselineThroughput
	}
	state.ProcessPID = 0

	if err := e.adapter.Start(ctx, e.cfg.Command, state.CurrentConcurrency); err != nil {
		e.logger.Error("failed to start workload", map[string]interface{}{"error": err.Error()})
		e.setLifecycle(LifecycleFailed, "start_failed", err)
		return nil, err
	}
	e.setLifecycle(LifecycleRunning, "running", nil)

	state.ProcessPID = e.adapter.GetPID()
	result := &EngineResult{State: state}
	updateResult := func() {
		result.State = state
	}
	updateResult()
	defer func() {
		_ = e.adapter.Stop()
		result.StoppedAt = e.nowFn()
		if e.Lifecycle().Phase != LifecycleFailed {
			e.setLifecycle(LifecycleStopped, result.Reason, nil)
		}
	}()

	ticker := time.NewTicker(e.cfg.PollInterval)
	defer ticker.Stop()

	var telemetryWindow []telemetry.TelemetrySample
	for {
		select {
		case <-ctx.Done():
			result.Reason = "shutdown_requested"
			e.setLifecycle(LifecycleStopping, result.Reason, nil)
			updateResult()
			return result, nil
		case now := <-ticker.C:
			state.Ticks++
			if e.cfg.MaxTicks > 0 && state.Ticks > e.cfg.MaxTicks {
				result.Reason = "max_ticks_reached"
				e.setLifecycle(LifecycleStopping, result.Reason, nil)
				updateResult()
				return result, nil
			}

			if !e.adapter.IsRunning() {
				result.Reason = "workload_exited_unexpectedly"
				e.setLifecycle(LifecycleFailed, result.Reason, nil)
				updateResult()
				return result, fmt.Errorf("%s", result.Reason)
			}

			ts := e.telemetryCollector.Sample(ctx)
			if err := e.persistTelemetrySample(ts); err != nil {
				e.logger.Warn("failed to persist telemetry sample", map[string]interface{}{
					"error":          err.Error(),
					"telemetry_path": e.cfg.TelemetryLogPath,
				})
			}
			telemetryWindow = appendTelemetry(telemetryWindow, ts)
			state.LastTelemetry = ts
			state.LastTelemetryAt = now

			outBytes := e.adapter.OutputBytes()
			throughputSample := e.throughputTracker.Add(outBytes, now)
			state.LastThroughput = throughputSample
			state.BaselineThroughput = e.throughputTracker.Baseline()
			state.ProcessPID = e.adapter.GetPID()
			state.Estimate = e.stateEstimator.Estimate(telemetryWindow, e.throughputTracker.Samples())

			actionState := control.State{
				CurrentConcurrency: state.CurrentConcurrency,
				MinConcurrency:     e.cfg.MinConcurrency,
				MaxConcurrency:     e.cfg.MaxConcurrency,
				BaselineThroughput: state.BaselineThroughput,
				LastActionAt:       state.LastActionAt,
			}
			action := e.controller.Decide(telemetryWindow, e.throughputTracker.Samples(), actionState)
			cooldown := actionMinCooldown(e.cfg.AdjustmentCooldown, action.CooldownSec)
			if isDirectionalAction(action.Type) && isOppositeDirection(state.LastAction.Type, action.Type) {
				cooldown = cooldown * 2
			}
			if action.Type != control.ActionHold && !state.LastActionAt.IsZero() && now.Sub(state.LastActionAt) < cooldown {
				action = control.Action{Type: control.ActionHold, Concurrency: state.CurrentConcurrency, Reason: "cooldown"}
			}
			throughputRatio := 0.0
			if state.BaselineThroughput > 0 {
				throughputRatio = throughputSample.Throughput / state.BaselineThroughput
			}

			if action.Type == control.ActionPause {
				state.LastActionAt = now
				if err := e.adapter.Pause(ctx); err != nil {
					e.logger.Error("failed to pause workload", map[string]interface{}{
						"error": err.Error(),
					})
					result.Reason = "failed_to_pause_workload"
					e.setLifecycle(LifecycleFailed, result.Reason, err)
					updateResult()
					return result, err
				}
				result.Reason = action.Reason
				e.setLifecycle(LifecycleStopping, result.Reason, nil)
				e.logger.Info("workload_paused", map[string]interface{}{
					"reason":      action.Reason,
					"concurrency": state.CurrentConcurrency,
				})
				updateResult()
				return result, nil
			}

			if action.Type != control.ActionHold {
				target := boundedStepDelta(state.CurrentConcurrency, action.Concurrency, e.cfg.MinConcurrency, e.cfg.MaxConcurrency, e.cfg.MaxConcurrencyStep)
				if target == state.CurrentConcurrency {
					action = control.Action{
						Type:        control.ActionHold,
						Concurrency: state.CurrentConcurrency,
						Reason:      "bounded by step/min-max",
					}
				} else {
					action.Concurrency = target
				}
			}
			state.LastAction = action
			updateResult()

			e.logger.Info("engine_tick", map[string]interface{}{
				"timestamp":                 now.Format(time.RFC3339),
				"event":                     "engine_tick",
				"pid":                       state.ProcessPID,
				"action":                    string(action.Type),
				"action_reason":             action.Reason,
				"concurrency":               state.CurrentConcurrency,
				"target_concurrency":        action.Concurrency,
				"temp_c":                    ts.TempC,
				"temp_valid":                ts.TempValid,
				"util_pct":                  ts.UtilPct,
				"util_valid":                ts.UtilValid,
				"vram_used_mb":              ts.VramUsedMB,
				"vram_total_mb":             ts.VramTotalMB,
				"vram_valid":                ts.VramTotalValid && ts.VramUsedValid,
				"memory_pressure":           ts.MemoryPressure,
				"memory_pressure_valid":     ts.MemoryPressureValid,
				"throttle_risk":             ts.ThrottleRisk,
				"throttle_risk_valid":       ts.ThrottleRiskValid,
				"effective_cooldown_sec":    cooldown.Seconds(),
				"throughput_bps":            throughputSample.Throughput,
				"baseline_bps":              state.BaselineThroughput,
				"throughput_ratio":          throughputRatio,
				"telemetry_error":           ts.Error,
				"temp_slope_c_per_sec":      state.Estimate.TempSlopeCPerSec,
				"temp_slope_valid":          state.Estimate.TempSlopeValid,
				"throughput_trend":          state.Estimate.ThroughputTrend,
				"throughput_trend_valid":    state.Estimate.ThroughputTrendValid,
				"throttle_risk_score":       state.Estimate.ThrottleRiskScore,
				"throttle_risk_score_valid": state.Estimate.ThrottleRiskScoreValid,
				"stability_index":           state.Estimate.StabilityIndex,
				"stability_index_valid":     state.Estimate.StabilityIndexValid,
				"estimate_confidence":       state.Estimate.Confidence,
				"estimate_confidence_valid": state.Estimate.ConfidenceValid,
			})

			if action.Type == control.ActionHold {
				updateResult()
				continue
			}

			state.LastActionAt = now
			if err := e.adapter.Restart(ctx, action.Concurrency); err != nil {
				e.logger.Error("failed to restart workload", map[string]interface{}{
					"error":              err.Error(),
					"target_concurrency": action.Concurrency,
				})
				updateResult()
				continue
			}
			state.CurrentConcurrency = action.Concurrency
			e.throughputTracker.Reset()
			updateResult()
			e.logger.Info("workload_restarted", map[string]interface{}{
				"new_concurrency": state.CurrentConcurrency,
				"pid":             e.adapter.GetPID(),
			})
		}
	}
}

func (e *Engine) logDebug(msg string, fields map[string]interface{}) {
	e.logger.Info(msg, fields)
}

func (e *Engine) ensureTelemetryStore() error {
	if e.cfg.TelemetryLogPath == "" {
		return nil
	}
	store, err := telemetry.NewSampleStore(e.cfg.TelemetryLogPath)
	if err != nil {
		return err
	}
	e.telemetryStore = store
	return nil
}

func (e *Engine) persistTelemetrySample(sample telemetry.TelemetrySample) error {
	return e.telemetryStore.Append(sample)
}

// noopLogger implements EventLogger for callers that do not supply a sink.
type noopLogger struct{}

func (n *noopLogger) Info(string, logger.Entry)  {}
func (n *noopLogger) Warn(string, logger.Entry)  {}
func (n *noopLogger) Error(string, logger.Entry) {}
