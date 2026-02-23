package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/elhamdev/gpu-guardian/internal/engine"
)

func TestHealthEndpoint(t *testing.T) {
	t.Helper()

	s := NewServer(DefaultListenAddress)
	ts := httptest.NewServer(s)
	defer ts.Close()

	r, err := http.Get(ts.URL + "/v1/health")
	if err != nil {
		t.Fatalf("health request failed: %v", err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", r.StatusCode)
	}

	var h HealthResponse
	if err := json.NewDecoder(r.Body).Decode(&h); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if h.Version != APIVersion {
		t.Fatalf("expected version %s, got %q", APIVersion, h.Version)
	}
}

func TestSessionLifecycleEndpoint(t *testing.T) {
	s := NewServer(DefaultListenAddress)
	ts := httptest.NewServer(s)
	defer ts.Close()

	startReq := StartRequest{
		Command:                  "sh -lc 'i=0; while true; do echo cycle-$i; i=$((i+1)); sleep 0.05; done'",
		PollIntervalSec:          1,
		SoftTemp:                 78,
		HardTemp:                 84,
		MinConcurrency:           1,
		MaxConcurrency:           2,
		StartConcurrency:         1,
		ThroughputFloorRatio:     0.7,
		AdjustmentCooldownSec:    1,
		BaselineWindowSec:        3,
		ThroughputWindowSec:      1,
		ThroughputFloorWindowSec: 1,
		AdapterStopTimeoutSec:    1,
	}
	startBody, err := json.Marshal(startReq)
	if err != nil {
		t.Fatalf("failed to marshal start request: %v", err)
	}

	r, err := http.Post(ts.URL+"/v1/sessions", "application/json", bytes.NewReader(startBody))
	if err != nil {
		t.Fatalf("start request failed: %v", err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", r.StatusCode)
	}
	var startResp SessionResponse
	if err := json.NewDecoder(r.Body).Decode(&startResp); err != nil {
		t.Fatalf("decode start response: %v", err)
	}
	if startResp.SessionID == "" {
		t.Fatal("expected session id")
	}

	if !waitUntil(func() bool {
		s, err := getSession(ts.URL, startResp.SessionID)
		if err != nil {
			return false
		}
		return s.Running
	}, 2*time.Second) {
		t.Fatal("session never reached running state")
	}

	r = postJSON(t, ts.URL+"/v1/sessions/"+startResp.SessionID+"/telemetry", http.MethodGet, nil)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("expected telemetry 200, got %d", r.StatusCode)
	}
	var telemetryResp TelemetryResponse
	if err := json.NewDecoder(r.Body).Decode(&telemetryResp); err != nil {
		t.Fatalf("decode telemetry response: %v", err)
	}
	if telemetryResp.SessionID != startResp.SessionID {
		t.Fatalf("expected telemetry session id %s, got %q", startResp.SessionID, telemetryResp.SessionID)
	}
	if telemetryResp.Session.ID != startResp.SessionID {
		t.Fatalf("expected session payload id %s, got %q", startResp.SessionID, telemetryResp.Session.ID)
	}
	if telemetryResp.Session.PolicyVersion != APIPolicyVersion {
		t.Fatalf("expected policy version %q, got %q", APIPolicyVersion, telemetryResp.Session.PolicyVersion)
	}
	_ = r.Body.Close()

	if err := postJSONWithNoBody(ts.URL+"/v1/sessions/"+startResp.SessionID+"/stop", http.MethodPost); err != nil {
		t.Fatalf("failed to stop session: %v", err)
	}

	if !waitUntil(func() bool {
		s, err := getSession(ts.URL, startResp.SessionID)
		if err != nil {
			return false
		}
		return !s.Running
	}, 2*time.Second) {
		t.Fatal("session never stopped")
	}
}

func TestSessionRecoveryPausesOnRepeatedFailure(t *testing.T) {
	s := NewServer(DefaultListenAddress)
	ts := httptest.NewServer(s)
	defer ts.Close()

	f, err := os.CreateTemp("", "guardian-session-checkpoint-*.json")
	if err != nil {
		t.Fatalf("create temp checkpoint: %v", err)
	}
	defer os.Remove(f.Name())
	if err := f.Close(); err != nil {
		t.Fatalf("close temp checkpoint: %v", err)
	}

	startReq := StartRequest{
		Command:                  "sh -lc 'exit 1'",
		PollIntervalSec:          1,
		SoftTemp:                 78,
		HardTemp:                 84,
		MinConcurrency:           1,
		MaxConcurrency:           1,
		StartConcurrency:         1,
		ThroughputFloorRatio:     0.7,
		AdjustmentCooldownSec:    1,
		BaselineWindowSec:        1,
		ThroughputWindowSec:      1,
		ThroughputFloorWindowSec: 1,
		AdapterStopTimeoutSec:    1,
		Mode:                     string(SessionModeStateful),
		CheckpointPath:           f.Name(),
		RecoveryMaxRetries:       1,
		RecoveryCooldownSec:      0,
	}

	startBody, err := json.Marshal(startReq)
	if err != nil {
		t.Fatalf("failed to marshal start request: %v", err)
	}
	r, err := http.Post(ts.URL+"/v1/sessions", "application/json", bytes.NewReader(startBody))
	if err != nil {
		t.Fatalf("start request failed: %v", err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", r.StatusCode)
	}
	var startResp SessionResponse
	if err := json.NewDecoder(r.Body).Decode(&startResp); err != nil {
		t.Fatalf("decode start response: %v", err)
	}
	if startResp.SessionID == "" {
		t.Fatal("expected session id")
	}

	if !waitUntil(func() bool {
		state, err := getSession(ts.URL, startResp.SessionID)
		if err != nil {
			return false
		}
		return !state.Running
	}, 6*time.Second) {
		t.Fatal("session did not stop after recovery attempts")
	}

	state, err := getSession(ts.URL, startResp.SessionID)
	if err != nil {
		t.Fatalf("get session state failed: %v", err)
	}
	if state.Goal != string(SessionGoalPaused) {
		t.Fatalf("expected goal %q, got %q", SessionGoalPaused, state.Goal)
	}
	if state.Retries < 1 {
		t.Fatalf("expected at least one recovery retry, got %d", state.Retries)
	}
	if len(state.Errors) == 0 {
		t.Fatalf("expected at least one recorded error")
	}

	raw, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatalf("checkpoint read failed: %v", err)
	}
	var onDisk SessionState
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("decode checkpoint state: %v", err)
	}
	if onDisk.ID != startResp.SessionID {
		t.Fatalf("checkpoint id mismatch: %q", onDisk.ID)
	}
	if onDisk.Mode != string(SessionModeStateful) {
		t.Fatalf("checkpoint mode mismatch: %q", onDisk.Mode)
	}
	if len(onDisk.Errors) == 0 {
		t.Fatalf("expected checkpoint to include errors")
	}
}

func TestStatefulSessionAppliesCheckpointDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	checkpointPath := filepath.Join(tmpDir, "session-checkpoint.json")
	profile := SessionState{
		ID:   "default",
		Mode: string(SessionModeStateful),
		State: engine.RunState{
			CurrentConcurrency: 4,
			BaselineThroughput: 55.5,
		},
	}
	raw, err := json.Marshal(profile)
	if err != nil {
		t.Fatalf("failed to marshal checkpoint state: %v", err)
	}
	if err := os.WriteFile(checkpointPath, raw, 0o600); err != nil {
		t.Fatalf("failed to write checkpoint state: %v", err)
	}

	req := &StartRequest{
		Mode:             string(SessionModeStateful),
		CheckpointPath:   checkpointPath,
		MinConcurrency:   1,
		MaxConcurrency:   8,
		StartConcurrency: 1,
	}
	applyStatefulCheckpointDefaults(req)
	if req.StartConcurrency != 4 {
		t.Fatalf("expected checkpoint start concurrency to be 4, got %d", req.StartConcurrency)
	}
	if req.InitialBaselineThroughput != 55.5 {
		t.Fatalf("expected checkpoint baseline restore to 55.5, got %f", req.InitialBaselineThroughput)
	}
}

func getSession(baseURL, id string) (SessionState, error) {
	r, err := http.Get(baseURL + "/v1/sessions/" + id)
	if err != nil {
		return SessionState{}, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		return SessionState{}, &urlError{msg: r.Status}
	}
	var out SessionState
	if err := json.NewDecoder(r.Body).Decode(&out); err != nil {
		return SessionState{}, err
	}
	return out, nil
}

func postJSON(t *testing.T, url, method string, payload []byte) *http.Response {
	t.Helper()
	r, err := http.NewRequest(method, url, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("content-type", "application/json")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	return resp
}

func postJSONWithNoBody(url, method string) error {
	r := bytes.NewReader(nil)
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return &urlError{msg: resp.Status}
	}
	return nil
}

type urlError struct {
	msg string
}

func (e *urlError) Error() string {
	return e.msg
}

func waitUntil(cond func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(25 * time.Millisecond)
	}
}
