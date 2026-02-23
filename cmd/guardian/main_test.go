//go:build integration

package main

import (
	"context"
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

	nvidiaScript := filepath.Join(tmpDir, "nvidia-smi")
	script := "#!/bin/sh\nprintf '72, 35, 4096, 8192\\n'\n"
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
}

func execCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd
}
