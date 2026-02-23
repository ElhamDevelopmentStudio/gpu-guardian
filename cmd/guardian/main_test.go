//go:build integration

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
	script := "#!/bin/sh\nprintf '72, 35, 4096, 8192, 120.0, 160.0, 1500, 5000\\n'\n"
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
	script := "#!/bin/sh\nconc=\"${CONCURRENCY:-1}\"\ntemp=$((55 + conc * 4))\nprintf '%d, 80, %s, 5000, 120.0, 160.0, 1500, 5000, 0\\n' \"$temp\" \"$((1000 + conc * 20))\"\n"
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

func execCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd
}
