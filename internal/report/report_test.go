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
