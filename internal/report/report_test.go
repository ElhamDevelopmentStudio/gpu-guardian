package report

import (
	"os"
	"path/filepath"
	"testing"
)

func writeLines(t *testing.T, path string, lines []string) {
	t.Helper()
	out := ""
	for _, line := range lines {
		out += line + "\n"
	}
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

func TestGenerateReportFromControlLog(t *testing.T) {
	tmp := t.TempDir()
	controlPath := filepath.Join(tmp, "control.log")
	writeLines(t, controlPath, []string{
		`{"event":"engine_tick","throughput_ratio":0.9,"throughput_ratio_valid":true,"temp_c":60,"temp_valid":true,"action":"hold","concurrency":1,"target_concurrency":1,"ts":"2026-01-01T00:00:00Z"}`,
		`{"event":"engine_tick","throughput_ratio":0.6,"throughput_ratio_valid":true,"temp_c":65,"temp_valid":true,"action":"decrease","concurrency":1,"target_concurrency":1,"ts":"2026-01-01T00:00:01Z"}`,
		`{"event":"engine_tick","throughput_ratio":0.5,"throughput_ratio_valid":true,"temp_c":70,"temp_valid":true,"action":"decrease","concurrency":1,"target_concurrency":1,"ts":"2026-01-01T00:00:03Z"}`,
	})

	rep, err := Generate(controlPath, "", 0.7)
	if err != nil {
		t.Fatalf("generate report: %v", err)
	}

	if rep.EngineTickSamples != 3 {
		t.Fatalf("expected 3 engine ticks, got %d", rep.EngineTickSamples)
	}
	if rep.TimeBelowFloorSec != 2 {
		t.Fatalf("expected 2 seconds below floor, got %.1f", rep.TimeBelowFloorSec)
	}
	if rep.WorstSlowdown != 0.5 {
		t.Fatalf("expected 0.5 slowdown, got %.3f", rep.WorstSlowdown)
	}
	if rep.Thermal.SampleCount != 3 {
		t.Fatalf("expected 3 thermal samples, got %d", rep.Thermal.SampleCount)
	}
	if rep.Thermal.MinTempC != 60 || rep.Thermal.MaxTempC != 70 {
		t.Fatalf("unexpected thermal bounds: min=%d max=%d", rep.Thermal.MinTempC, rep.Thermal.MaxTempC)
	}
	if rep.Recovery.DecreaseActions != 2 || rep.Recovery.HoldActions != 1 {
		t.Fatalf("unexpected recovery counts: %+v", rep.Recovery)
	}
	if rep.Recovery.MaxDecreaseStreak != 2 {
		t.Fatalf("expected max decrease streak 2, got %d", rep.Recovery.MaxDecreaseStreak)
	}
}

func TestGenerateReportFromTelemetryFallback(t *testing.T) {
	tmp := t.TempDir()
	controlPath := filepath.Join(tmp, "control.log")
	telemetryPath := filepath.Join(tmp, "telemetry.log")
	writeLines(t, controlPath, []string{
		`{"event":"engine_tick","throughput_ratio":1,"throughput_ratio_valid":true,"temp_valid":false,"action":"hold","concurrency":2,"target_concurrency":2,"ts":"2026-01-01T00:00:00Z"}`,
	})
	writeLines(t, telemetryPath, []string{
		`{"temp_c":40,"temp_valid":true,"timestamp":"2026-01-01T00:00:00Z"}`,
		`{"temp_c":50,"temp_valid":true,"timestamp":"2026-01-01T00:00:01Z"}`,
	})

	rep, err := Generate(controlPath, telemetryPath, 0)
	if err != nil {
		t.Fatalf("generate report: %v", err)
	}

	if rep.TelemetrySamples != 2 {
		t.Fatalf("expected 2 telemetry samples, got %d", rep.TelemetrySamples)
	}
	if rep.Thermal.SampleCount != 2 {
		t.Fatalf("expected 2 thermal samples, got %d", rep.Thermal.SampleCount)
	}
	if rep.Thermal.MinTempC != 40 || rep.Thermal.MaxTempC != 50 {
		t.Fatalf("unexpected thermal bounds from telemetry fallback: min=%d max=%d", rep.Thermal.MinTempC, rep.Thermal.MaxTempC)
	}
	if rep.WorstSlowdown != 0 {
		t.Fatalf("expected zero slowdown with ratio=1, got %f", rep.WorstSlowdown)
	}
}

func TestEvaluateSuccessCriteriaPassesForHealthyRun(t *testing.T) {
	tmp := t.TempDir()
	controlPath := filepath.Join(tmp, "control.log")
	telemetryPath := filepath.Join(tmp, "telemetry.log")

	controlLines := []string{
		`{"event":"engine_tick","throughput_ratio":1,"throughput_ratio_valid":true,"temp_valid":true,"temp_c":60,"ts":"2026-01-01T00:00:00Z","timestamp":"2026-01-01T00:00:00Z"}`,
		`{"event":"engine_tick","throughput_ratio":1,"throughput_ratio_valid":true,"temp_valid":true,"temp_c":61,"ts":"2026-01-01T00:00:01Z","timestamp":"2026-01-01T00:00:01Z"}`,
		`{"event":"engine_tick","throughput_ratio":1,"throughput_ratio_valid":true,"temp_valid":true,"temp_c":62,"ts":"2026-01-01T00:00:02Z","timestamp":"2026-01-01T00:00:02Z"}`,
		`{"event":"engine_tick","throughput_ratio":1,"throughput_ratio_valid":true,"temp_valid":true,"temp_c":63,"ts":"2026-01-01T00:00:03Z","timestamp":"2026-01-01T00:00:03Z"}`,
	}
	writeLines(t, controlPath, controlLines)

	telemetryLines := []string{
		`{"temp_c":60,"temp_valid":true,"timestamp":"2026-01-01T00:00:00Z"}`,
		`{"temp_c":64,"temp_valid":true,"timestamp":"2026-01-01T00:00:01Z"}`,
		`{"temp_c":66,"temp_valid":true,"timestamp":"2026-01-01T00:00:02Z"}`,
	}
	writeLines(t, telemetryPath, telemetryLines)

	got, err := EvaluateSuccessCriteria(controlPath, telemetryPath, SuccessCriteriaPolicy{
		ThroughputFloorRatio:      0.7,
		MaxSustainedSlowdownRatio: 0.2,
		MaxSustainedSlowdownSec:   3,
		MinRuntimeAboveFloorRatio: 0.95,
		ThermalCeilingC:           70,
	})
	if err != nil {
		t.Fatalf("evaluate success criteria: %v", err)
	}
	if !got.Passed {
		t.Fatalf("expected healthy run to pass, checks=%+v", got.Checks)
	}
	if got.RuntimeSec != 3 {
		t.Fatalf("expected runtime 3, got %.3f", got.RuntimeSec)
	}
}

func TestEvaluateSuccessCriteriaFailsOnSustainedSlowdown(t *testing.T) {
	tmp := t.TempDir()
	controlPath := filepath.Join(tmp, "control.log")
	controlLines := []string{
		`{"event":"engine_tick","throughput_ratio":0.6,"throughput_ratio_valid":true,"temp_c":60,"temp_valid":true,"ts":"2026-01-01T00:00:00Z"}`,
		`{"event":"engine_tick","throughput_ratio":0.1,"throughput_ratio_valid":true,"temp_c":60,"temp_valid":true,"ts":"2026-01-01T00:00:01Z"}`,
		`{"event":"engine_tick","throughput_ratio":0.1,"throughput_ratio_valid":true,"temp_c":60,"temp_valid":true,"ts":"2026-01-01T00:00:02Z"}`,
		`{"event":"engine_tick","throughput_ratio":0.1,"throughput_ratio_valid":true,"temp_c":60,"temp_valid":true,"ts":"2026-01-01T00:00:03Z"}`,
		`{"event":"engine_tick","throughput_ratio":0.1,"throughput_ratio_valid":true,"temp_c":60,"temp_valid":true,"ts":"2026-01-01T00:00:04Z"}`,
		`{"event":"engine_tick","throughput_ratio":0.1,"throughput_ratio_valid":true,"temp_c":60,"temp_valid":true,"ts":"2026-01-01T00:00:05Z"}`,
		`{"event":"engine_tick","throughput_ratio":0.1,"throughput_ratio_valid":true,"temp_c":60,"temp_valid":true,"ts":"2026-01-01T00:00:06Z"}`,
	}
	writeLines(t, controlPath, controlLines)

	got, err := EvaluateSuccessCriteria(controlPath, "", SuccessCriteriaPolicy{
		ThroughputFloorRatio:      0.7,
		MaxSustainedSlowdownRatio: 0.2,
		MaxSustainedSlowdownSec:   2,
		RequireThermalSafetyCheck: false,
		RequireFloorUptimeCheck:   false,
	})
	if err != nil {
		t.Fatalf("evaluate success criteria: %v", err)
	}
	if got.Passed {
		t.Fatalf("expected sustained slowdown to fail, got %+v", got)
	}
}

func TestEvaluateSuccessCriteriaFailsThermalCeilingViolation(t *testing.T) {
	tmp := t.TempDir()
	controlPath := filepath.Join(tmp, "control.log")
	telemetryPath := filepath.Join(tmp, "telemetry.log")
	controlLines := []string{
		`{"event":"engine_tick","throughput_ratio":1,"throughput_ratio_valid":true,"temp_valid":true,"temp_c":60,"ts":"2026-01-01T00:00:00Z"}`,
		`{"event":"engine_tick","throughput_ratio":1,"throughput_ratio_valid":true,"temp_valid":true,"temp_c":62,"ts":"2026-01-01T00:00:01Z"}`,
	}
	writeLines(t, controlPath, controlLines)
	writeLines(t, telemetryPath, []string{
		`{"temp_c":84,"temp_valid":true,"timestamp":"2026-01-01T00:00:00Z"}`,
		`{"temp_c":90,"temp_valid":true,"timestamp":"2026-01-01T00:00:01Z"}`,
	})

	got, err := EvaluateSuccessCriteria(controlPath, telemetryPath, SuccessCriteriaPolicy{
		ThroughputFloorRatio:      0.7,
		MaxSustainedSlowdownRatio: 0.2,
		MaxSustainedSlowdownSec:   30,
		ThermalCeilingC:           85,
		RequireSlowdownCheck:      false,
		RequireFloorUptimeCheck:   false,
	})
	if err != nil {
		t.Fatalf("evaluate success criteria: %v", err)
	}
	if got.Passed {
		t.Fatalf("expected thermal ceiling violation to fail, got %+v", got)
	}
}
