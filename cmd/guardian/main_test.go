//go:build integration

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestControlLoopE2EWithMaxTicks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "guardian.log")
	workloadLog := filepath.Join(tmpDir, "workload.log")
	telemetryLog := filepath.Join(tmpDir, "telemetry.log")

	nvidiaScript := filepath.Join(tmpDir, "nvidia-smi")
	script := "#!/bin/sh\nprintf 'GPU-CONTROL,72, 35, 4096, 8192, 120.0, 160.0, 1500, 5000, power_cap\\n'\n"
	if err := os.WriteFile(nvidiaScript, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write fake nvidia-smi: %v", err)
	}
	if err := os.Chmod(nvidiaScript, 0o755); err != nil {
		t.Fatalf("failed to chmod fake nvidia-smi: %v", err)
	}

	env := append([]string{}, os.Environ()...)
	env = append(env, "PATH="+tmpDir+":"+os.Getenv("PATH"))

	cmd := execCommand(ctx, "go", "run", ".", "control",
		"--cmd", "sh -lc 'while true; do echo workload; sleep 0.1; done'",
		"--poll-interval-sec", "1",
		"--max-ticks", "2",
		"--log-file", logPath,
		"--telemetry-log", telemetryLog,
		"--workload-log", workloadLog,
		"--adapter-stop-timeout-sec", "1",
		"--echo-workload-output=false",
	)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatal("e2e control loop timed out")
	}
	if err != nil {
		t.Fatalf("e2e command failed: %v; out=%s", err, string(out))
	}

	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("expected log file at %s: %v", logPath, err)
	}
	if _, err := os.Stat(workloadLog); err != nil {
		t.Fatalf("expected workload log at %s: %v", workloadLog, err)
	}
	if _, err := os.Stat(telemetryLog); err != nil {
		t.Fatalf("expected telemetry log at %s: %v", telemetryLog, err)
	}
}

func TestCalibrationCommandOutputsProfile(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	tmpDir := t.TempDir()
	profilePath := filepath.Join(tmpDir, "calibration.json")

	nvidiaScript := filepath.Join(tmpDir, "nvidia-smi")
	script := "#!/bin/sh\nconc=\"${CONCURRENCY:-1}\"\ntemp=$((55 + conc * 4))\nprintf 'GPU-CALIB-%s,%d, 80, %s, 5000, 120.0, 160.0, 1500, 5000, 0\\n' \"$conc\" \"$temp\" \"$((1000 + conc * 20))\"\n"
	if err := os.WriteFile(nvidiaScript, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write fake nvidia-smi: %v", err)
	}
	if err := os.Chmod(nvidiaScript, 0o755); err != nil {
		t.Fatalf("failed to chmod fake nvidia-smi: %v", err)
	}

	env := append([]string{}, os.Environ()...)
	env = append(env, "PATH="+tmpDir+":"+os.Getenv("PATH"))

	cmd := execCommand(ctx, "go", "run", ".", "calibrate",
		"--cmd", "while true; do echo workload; sleep 0.02; done",
		"--min-concurrency", "1",
		"--max-concurrency", "3",
		"--concurrency-step", "1",
		"--poll-interval-sec", "1",
		"--calibration-step-duration-sec", "1",
		"--step-samples", "4",
		"--throughput-drop-ratio", "0.7",
		"--hard-temp", "200",
		"--output", profilePath,
		"--adapter-stop-timeout-sec", "1",
	)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatal("calibration command timed out")
	}
	if err != nil {
		t.Fatalf("calibration command failed: %v; out=%s", err, string(out))
	}

	raw, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("expected calibration output at %s: %v", profilePath, err)
	}
	var profile struct {
		SafeConcurrencyCeiling int        `json:"safe_concurrency_ceiling"`
		BaselineThroughput     float64    `json:"baseline_throughput"`
		Curve                  []struct{} `json:"thermal_saturation_curve"`
	}
	if err := json.Unmarshal(raw, &profile); err != nil {
		t.Fatalf("decode calibration profile: %v", err)
	}
	if profile.SafeConcurrencyCeiling < 1 {
		t.Fatalf("expected safe concurrency ceiling in profile, got %d", profile.SafeConcurrencyCeiling)
	}
	if profile.BaselineThroughput <= 0 {
		t.Fatalf("expected positive baseline throughput, got %f", profile.BaselineThroughput)
	}
	if len(profile.Curve) != 3 {
		t.Fatalf("expected 3-point thermal saturation curve, got %d", len(profile.Curve))
	}
}

func TestControlLoadsProfileDefaults(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	tmpDir := t.TempDir()
	nvidiaScript := filepath.Join(tmpDir, "nvidia-smi")
	script := "#!/bin/sh\nconc=\"${CONCURRENCY:-1}\"\ntemp=$((55 + conc * 5))\nprintf 'GPU-E2E,%d, 80, 1024, 2048, 120.0, 160.0, 1500, 5000, 0\\n' \"$temp\"\n"
	if err := os.WriteFile(nvidiaScript, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write fake nvidia-smi: %v", err)
	}
	if err := os.Chmod(nvidiaScript, 0o755); err != nil {
		t.Fatalf("failed to chmod fake nvidia-smi: %v", err)
	}

	env := append([]string{}, os.Environ()...)
	env = append(env, "PATH="+tmpDir+":"+os.Getenv("PATH"))

	profilePath := filepath.Join(tmpDir, "profiles.json")
	profileOutPath := filepath.Join(tmpDir, "calibration.json")
	logPath := filepath.Join(tmpDir, "guardian.log")
	telemetryLog := filepath.Join(tmpDir, "telemetry.log")
	workloadLog := filepath.Join(tmpDir, "workload.log")

	calibrate := execCommand(ctx, "go", "run", ".", "calibrate",
		"--cmd", "while true; do echo workload; sleep 0.02; done",
		"--min-concurrency", "1",
		"--max-concurrency", "3",
		"--concurrency-step", "1",
		"--poll-interval-sec", "1",
		"--calibration-step-duration-sec", "1",
		"--step-samples", "4",
		"--throughput-drop-ratio", "0.7",
		"--hard-temp", "60",
		"--workload-type", "integration-e2e",
		"--profile-path", profilePath,
		"--output", profileOutPath,
		"--adapter-stop-timeout-sec", "1",
	)
	calibrate.Env = env
	if out, err := calibrate.CombinedOutput(); err != nil {
		t.Fatalf("calibration command failed: %v; out=%s", err, string(out))
	}

	profileRaw, err := os.ReadFile(profileOutPath)
	if err != nil {
		t.Fatalf("expected calibration output file at %s: %v", profileOutPath, err)
	}
	var profile struct {
		SafeConcurrency int `json:"safe_concurrency_ceiling"`
	}
	if err := json.Unmarshal(profileRaw, &profile); err != nil {
		t.Fatalf("decode calibration profile: %v", err)
	}
	if profile.SafeConcurrency <= 0 {
		t.Fatalf("invalid safe concurrency from calibration profile: %d", profile.SafeConcurrency)
	}

	controlCmd := execCommand(ctx, "go", "run", ".", "control",
		"--cmd", "while true; do echo workload; sleep 0.1; done",
		"--poll-interval-sec", "1",
		"--max-ticks", "1",
		"--workload-type", "integration-e2e",
		"--profile-path", profilePath,
		"--log-file", logPath,
		"--telemetry-log", telemetryLog,
		"--workload-log", workloadLog,
		"--adapter-stop-timeout-sec", "1",
		"--throughput-floor-ratio", "1",
		"--echo-workload-output=false",
	)
	controlCmd.Env = env
	if out, err := controlCmd.CombinedOutput(); err != nil {
		t.Fatalf("control command failed: %v; out=%s", err, string(out))
	}

	logRaw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("expected control log at %s: %v", logPath, err)
	}

	lines := strings.Split(string(logRaw), "\n")
	foundStartLine := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("decode control log line: %v", err)
		}
		if entry["msg"] != "starting control loop" {
			continue
		}
		foundStartLine = true
		startRaw, ok := entry["start"].(float64)
		if !ok {
			t.Fatalf("expected start field in control start event: %#v", entry)
		}
		if int(startRaw) != profile.SafeConcurrency {
			t.Fatalf("expected control start to use persisted safe concurrency %d, got %.0f", profile.SafeConcurrency, startRaw)
		}
		break
	}
	if !foundStartLine {
		t.Fatalf("expected control log to include startup event")
	}
}

func TestReportCommand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tmpDir := t.TempDir()
	controlLog := filepath.Join(tmpDir, "control.log")
	telemetryLog := filepath.Join(tmpDir, "telemetry.log")
	reportPath := filepath.Join(tmpDir, "report.json")

	controlLines := []string{
		`{"event":"engine_tick","throughput_ratio":0.9,"throughput_ratio_valid":true,"temp_c":60,"temp_valid":true,"action":"hold","concurrency":1,"target_concurrency":1,"ts":"2026-01-01T00:00:00Z","timestamp":"2026-01-01T00:00:00Z"}`,
		`{"event":"engine_tick","throughput_ratio":0.6,"throughput_ratio_valid":true,"temp_c":65,"temp_valid":true,"action":"decrease","concurrency":1,"target_concurrency":1,"ts":"2026-01-01T00:00:01Z","timestamp":"2026-01-01T00:00:01Z"}`,
		`{"event":"engine_tick","throughput_ratio":0.5,"throughput_ratio_valid":true,"temp_c":70,"temp_valid":true,"action":"decrease","concurrency":1,"target_concurrency":1,"ts":"2026-01-01T00:00:03Z","timestamp":"2026-01-01T00:00:03Z"}`,
	}
	writeFixtureLines(t, controlLog, controlLines)

	telemetryLines := []string{
		`{"temp_c":60,"temp_valid":true,"timestamp":"2026-01-01T00:00:00Z","temp_valid":true}`,
		`{"temp_c":62,"temp_valid":true,"timestamp":"2026-01-01T00:00:01Z","temp_valid":true}`,
	}
	writeFixtureLines(t, telemetryLog, telemetryLines)

	cmd := execCommand(ctx, "go", "run", ".", "report",
		"--control-log", controlLog,
		"--telemetry-log", telemetryLog,
		"--throughput-floor-ratio", "0.7",
		"--output", reportPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("report command failed: %v; out=%s", err, string(out))
	}

	raw, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("expected report output at %s: %v", reportPath, err)
	}

	var got struct {
		EngineTickSamples int `json:"engine_tick_samples"`
		ThermalProfile    struct {
			SampleCount int `json:"sample_count"`
			MinTempC    int `json:"min_temp_c"`
			MaxTempC    int `json:"max_temp_c"`
		} `json:"thermal_profile"`
		Recovery struct {
			DecreaseActions int `json:"decrease_actions"`
		} `json:"recovery_metrics"`
		WorstSlowdown  float64 `json:"worst_slowdown"`
		TimeBelowFloor float64 `json:"time_below_throughput_floor_sec"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode report output: %v", err)
	}
	if got.EngineTickSamples != 3 {
		t.Fatalf("expected 3 engine tick samples, got %d", got.EngineTickSamples)
	}
	if got.ThermalProfile.SampleCount != 5 {
		t.Fatalf("expected combined 5 thermal samples, got %d", got.ThermalProfile.SampleCount)
	}
	if got.ThermalProfile.MinTempC != 60 || got.ThermalProfile.MaxTempC != 70 {
		t.Fatalf("unexpected thermal bounds: %+v", got.ThermalProfile)
	}
	if got.Recovery.DecreaseActions != 2 {
		t.Fatalf("expected 2 decrease actions, got %d", got.Recovery.DecreaseActions)
	}
	if got.WorstSlowdown != 0.5 {
		t.Fatalf("expected slowdown 0.5, got %f", got.WorstSlowdown)
	}
	if got.TimeBelowFloor != 2 {
		t.Fatalf("expected 2 seconds below throughput floor, got %.1f", got.TimeBelowFloor)
	}
}

func writeFixtureLines(t *testing.T, path string, lines []string) {
	t.Helper()
	payload := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("write fixture log: %v", err)
	}
}

func execCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd
}
