package simulation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/elhamdev/gpu-guardian/internal/control"
)

func TestReplayAppliesControlThroughputAndPolicyDecisions(t *testing.T) {
	dir := t.TempDir()
	telemetryLog := filepath.Join(dir, "telemetry.log")
	controlLog := filepath.Join(dir, "control.log")

	writeFixtureLines(t, telemetryLog, []string{
		`{"timestamp":"2026-01-01T00:00:00Z","temp_c":62,"temp_valid":true,"throttle_risk":0.0,"throttle_risk_valid":true}`,
		`{"timestamp":"2026-01-01T00:00:01Z","temp_c":63,"temp_valid":true,"throttle_risk":0.0,"throttle_risk_valid":true}`,
		`{"timestamp":"2026-01-01T00:00:02Z","temp_c":64,"temp_valid":true,"throttle_risk":0.0,"throttle_risk_valid":true}`,
		`{"timestamp":"2026-01-01T00:00:03Z","temp_c":65,"temp_valid":true,"throttle_risk":0.0,"throttle_risk_valid":true}`,
	})
	writeFixtureLines(t, controlLog, []string{
		`{"timestamp":"2026-01-01T00:00:00Z","throughput_ratio":1.0,"baseline_bps":100}`,
		`{"timestamp":"2026-01-01T00:00:01Z","throughput_ratio":1.0,"baseline_bps":100}`,
		`{"timestamp":"2026-01-01T00:00:02Z","throughput_ratio":1.0,"baseline_bps":100}`,
		`{"timestamp":"2026-01-01T00:00:03Z","throughput_ratio":1.0,"baseline_bps":100}`,
	})

	out, err := Replay(ReplayConfig{
		TelemetryLogPath:           telemetryLog,
		ControlLogPath:             controlLog,
		MinConcurrency:             1,
		MaxConcurrency:             3,
		StartConcurrency:           1,
		MaxConcurrencyStep:         1,
		InitialBaselineThroughput: 100,
		RuleCfg:                   control.RuleConfig{ThroughputFloorRatio: 0.01, EstimateConfidenceMin: 0.1},
		AdjustmentCooldown:         0,
		ThroughputWindow:           30 * time.Second,
		BaselineWindow:             120 * time.Second,
		MaxTicks:                   4,
	})
	if err != nil {
		t.Fatalf("replay failed: %v", err)
	}

	if out.FinalConcurrency != 3 {
		t.Fatalf("expected final concurrency 3, got %d", out.FinalConcurrency)
	}
	if out.FinalAction != "increase" {
		t.Fatalf("expected final action increase, got %q", out.FinalAction)
	}
	if out.DecisionSamples != 4 {
		t.Fatalf("expected 4 decision samples, got %d", out.DecisionSamples)
	}
}

func TestReplayFallsBackToBaselineThroughputWithoutControlLog(t *testing.T) {
	dir := t.TempDir()
	telemetryLog := filepath.Join(dir, "telemetry.log")

	writeFixtureLines(t, telemetryLog, []string{
		`{"timestamp":"2026-01-01T00:00:00Z","temp_c":63,"temp_valid":true,"throttle_risk":0.0,"throttle_risk_valid":true}`,
		`{"timestamp":"2026-01-01T00:00:01Z","temp_c":64,"temp_valid":true,"throttle_risk":0.0,"throttle_risk_valid":true}`,
	})

	out, err := Replay(ReplayConfig{
		TelemetryLogPath:           telemetryLog,
		MinConcurrency:             1,
		MaxConcurrency:             2,
		StartConcurrency:           1,
		MaxConcurrencyStep:         1,
		InitialBaselineThroughput: 100,
		RuleCfg:                   control.RuleConfig{ThroughputFloorRatio: 0.01, EstimateConfidenceMin: 0.1},
		AdjustmentCooldown:         0,
		ThroughputWindow:           30 * time.Second,
		BaselineWindow:             120 * time.Second,
	})
	if err != nil {
		t.Fatalf("replay failed: %v", err)
	}

	if out.FinalConcurrency != 2 {
		t.Fatalf("expected final concurrency 2 from fallback throughput, got %d", out.FinalConcurrency)
	}
	if out.FinalAction == "" {
		t.Fatalf("expected final action to be set")
	}
}

func TestReplayDecreasesOnSustainedThroughputFloor(t *testing.T) {
	dir := t.TempDir()
	telemetryLog := filepath.Join(dir, "telemetry.log")
	controlLog := filepath.Join(dir, "control.log")

	writeFixtureLines(t, telemetryLog, []string{
		`{"timestamp":"2026-01-01T00:00:00Z","temp_c":64,"temp_valid":true,"throttle_risk":0.0,"throttle_risk_valid":true}`,
		`{"timestamp":"2026-01-01T00:00:01Z","temp_c":64,"temp_valid":true,"throttle_risk":0.0,"throttle_risk_valid":true}`,
		`{"timestamp":"2026-01-01T00:00:02Z","temp_c":64,"temp_valid":true,"throttle_risk":0.0,"throttle_risk_valid":true}`,
	})
	writeFixtureLines(t, controlLog, []string{
		`{"timestamp":"2026-01-01T00:00:00Z","throughput_ratio":0.01,"baseline_bps":100}`,
		`{"timestamp":"2026-01-01T00:00:01Z","throughput_ratio":0.01,"baseline_bps":100}`,
		`{"timestamp":"2026-01-01T00:00:02Z","throughput_ratio":0.01,"baseline_bps":100}`,
	})

	out, err := Replay(ReplayConfig{
		TelemetryLogPath:           telemetryLog,
		ControlLogPath:             controlLog,
		MinConcurrency:             1,
		MaxConcurrency:             3,
		StartConcurrency:           2,
		MaxConcurrencyStep:         1,
		InitialBaselineThroughput: 100,
		RuleCfg: control.RuleConfig{
			ThroughputRecoveryMaxAttempts:    1,
			ThroughputSlowdownFloorRatio:     0.5,
			ThroughputFloorRatio:             0.01,
			EstimateConfidenceMin:            0.1,
		},
		AdjustmentCooldown:         0,
		ThroughputWindow:           30 * time.Second,
		BaselineWindow:             120 * time.Second,
		MaxTicks:                   3,
	})
	if err != nil {
		t.Fatalf("replay failed: %v", err)
	}

	if out.FinalAction != "decrease" {
		t.Fatalf("expected decrease action, got %q", out.FinalAction)
	}
	if !strings.Contains(out.FinalReason, "throughput below slowdown fallback") {
		t.Fatalf("expected throughput slowdown recovery reason, got %q", out.FinalReason)
	}
}

func writeFixtureLines(t *testing.T, path string, lines []string) {
	t.Helper()
	payload := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("write fixture file %s: %v", path, err)
	}

	var v map[string]interface{}
	for _, line := range lines {
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("invalid fixture line in %s: %v", path, err)
		}
	}
}
