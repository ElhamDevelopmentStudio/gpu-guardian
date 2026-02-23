package adapter

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestXttsAdapterStartRestartStop(t *testing.T) {
	tmpDir := t.TempDir()
	outputPath := filepath.Join(tmpDir, "workload.log")
	cfg := Config{
		OutputPath:  outputPath,
		StopTimeout: 500 * time.Millisecond,
		EchoOutput:  false,
	}
	adapter := NewXttsAdapter(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := "sh -lc 'i=0; while true; do echo cycle-$i; i=$((i+1)); sleep 0.1; done'"
	if err := adapter.Start(ctx, cmd, 4); err != nil {
		t.Fatalf("failed to start adapter: %v", err)
	}
	if !adapter.IsRunning() {
		t.Fatal("adapter should be running after start")
	}
	firstPID := adapter.GetPID()
	if firstPID == 0 {
		t.Fatal("expected process id to be set")
	}

	waitUntil := time.Now().Add(2 * time.Second)
	for {
		if adapter.OutputBytes() > 0 {
			break
		}
		if time.Now().After(waitUntil) {
			content, readErr := os.ReadFile(outputPath)
			if readErr == nil && len(content) > 0 {
				break
			}
			t.Fatalf("expected adapter output bytes to increase; output bytes=%d, output file size=%d, read err=%v", adapter.OutputBytes(), len(content), readErr)
		}
		time.Sleep(50 * time.Millisecond)
	}

	if err := adapter.Restart(ctx, 2); err != nil {
		t.Fatalf("failed to restart adapter: %v", err)
	}
	if !adapter.IsRunning() {
		t.Fatal("adapter should be running after restart")
	}
	secondPID := adapter.GetPID()
	if secondPID == 0 || secondPID == firstPID {
		t.Fatalf("expected new process pid after restart, got first=%d second=%d", firstPID, secondPID)
	}
	time.Sleep(300 * time.Millisecond)

	if err := adapter.Stop(); err != nil {
		t.Fatalf("failed to stop adapter: %v", err)
	}
	if adapter.IsRunning() {
		t.Fatalf("adapter should not be running after stop")
	}

	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("failed to read workload output log: %v", err)
	}
	if !strings.Contains(string(content), "cycle-") {
		t.Fatal("workload output missing expected marker")
	}
}

func TestXttsAdapterPauseResumeUpdateParameters(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := Config{
		OutputPath:  filepath.Join(tmpDir, "workload.log"),
		StopTimeout: 500 * time.Millisecond,
		EchoOutput:  false,
	}
	adapter := NewXttsAdapter(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := "sh -lc 'i=0; while true; do echo cycle-$i; i=$((i+1)); sleep 0.1; done'"
	if err := adapter.Start(ctx, cmd, 2); err != nil {
		t.Fatalf("failed to start adapter: %v", err)
	}
	if !adapter.IsRunning() {
		t.Fatal("adapter should be running")
	}

	if err := adapter.Pause(ctx); err != nil {
		t.Fatalf("failed to pause adapter: %v", err)
	}
	if adapter.IsRunning() {
		t.Fatal("adapter should not be running after pause")
	}

	if err := adapter.UpdateParameters(ctx, 3); err != nil {
		t.Fatalf("failed to update parameters: %v", err)
	}

	if err := adapter.Resume(ctx); err != nil {
		t.Fatalf("failed to resume adapter: %v", err)
	}
	if !adapter.IsRunning() {
		t.Fatal("adapter should be running after resume")
	}
	if adapter.GetProgress() != 0 {
		t.Fatalf("expected progress fallback value 0, got %f", adapter.GetProgress())
	}
	if adapter.GetThroughput() == 0 {
		t.Log("throughput not yet available")
	}

	if err := adapter.Stop(); err != nil {
		t.Fatalf("failed to stop adapter: %v", err)
	}
}
