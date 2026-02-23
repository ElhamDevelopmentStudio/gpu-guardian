package telemetry

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCollectorParsesExtendedTelemetryFields(t *testing.T) {
	originalRunner := runNvidiaSMICommand
	runNvidiaSMICommand = func(_ context.Context, query string) ([]byte, error) {
		if strings.Contains(query, "clocks_throttle_reasons.active") {
			return []byte("64, 42.0, 5120, 10240, 210.5, 300.0, 1750.0, 8100.0, power_cap"), nil
		}
		return []byte("64, 42.0, 5120, 10240"), nil
	}
	defer func() {
		runNvidiaSMICommand = originalRunner
	}()

	c := NewCollector()
	s := c.Sample(context.Background())
	if s.Error != "" {
		t.Fatalf("expected parse success for extended sample, got error: %s", s.Error)
	}
	if !s.TempValid || s.TempC != 64 {
		t.Fatalf("expected temp parse, got %#v", s)
	}
	if !s.PowerDrawValid || s.PowerDrawW != 210.5 {
		t.Fatalf("expected power draw parse, got %#v", s)
	}
	if !s.ClockMemValid || s.ClockMemMHz != 8100 {
		t.Fatalf("expected memory clock parse, got %#v", s)
	}
	if !s.ThrottleReasonsValid || s.ThrottleReasons != "power_cap" {
		t.Fatalf("expected throttle reasons parse, got %#v", s)
	}
}

func TestCollectorFallsBackToCoreFieldsOnExtendedQueryFailure(t *testing.T) {
	originalRunner := runNvidiaSMICommand
	runNvidiaSMICommand = func(_ context.Context, query string) ([]byte, error) {
		if strings.Contains(query, "clocks_throttle_reasons.active") {
			return nil, &mockCommandError{msg: "unsupported field"}
		}
		return []byte("64, 42.0, 5120, 10240"), nil
	}
	defer func() {
		runNvidiaSMICommand = originalRunner
	}()

	c := NewCollector()
	s := c.Sample(context.Background())
	if s.Error == "" {
		t.Fatal("expected fallback notice")
	}
	if !s.TempValid || s.PowerDrawValid {
		t.Fatalf("expected fallback core fields and no power draw, got %#v", s)
	}
	if !s.MemoryPressureValid || s.MemoryPressure != 0.5 {
		t.Fatalf("expected memory pressure from core fields, got %#v", s)
	}
}

func TestCollectorSurvivesMalformedOutputWithoutPanicking(t *testing.T) {
	originalRunner := runNvidiaSMICommand
	runNvidiaSMICommand = func(_ context.Context, _ string) ([]byte, error) {
		return []byte("bad"), nil
	}
	defer func() {
		runNvidiaSMICommand = originalRunner
	}()

	c := NewCollector()
	s := c.Sample(context.Background())
	if s.Error == "" {
		t.Fatal("expected parse error for malformed output")
	}
}

func TestSampleStoreWritesLinePerSample(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "telemetry.log")

	store, err := NewSampleStore(storePath)
	if err != nil {
		t.Fatalf("new sample store: %v", err)
	}
	if store == nil {
		t.Fatal("expected sample store instance")
	}
	defer store.Close()

	sample := TelemetrySample{
		Timestamp: time.Unix(1700000000, 0).UTC(),
		TempC:     77,
		TempValid: true,
		UtilPct:   88.5,
		UtilValid: true,
		Error:     "",
	}
	if err := store.Append(sample); err != nil {
		t.Fatalf("append sample: %v", err)
	}

	file, err := os.Open(storePath)
	if err != nil {
		t.Fatalf("open store file: %v", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var line string
	for scanner.Scan() {
		line = scanner.Text()
	}
	if line == "" {
		t.Fatal("expected at least one persisted sample line")
	}

	var decoded TelemetrySample
	if err := json.Unmarshal([]byte(line), &decoded); err != nil {
		t.Fatalf("decode persisted sample: %v", err)
	}
	if !decoded.TempValid || decoded.TempC != 77 {
		t.Fatalf("unexpected decoded sample: %#v", decoded)
	}
}

func TestSampleStoreNoopWhenPathEmpty(t *testing.T) {
	store, err := NewSampleStore("")
	if err != nil {
		t.Fatalf("new sample store: %v", err)
	}
	if store != nil {
		t.Fatalf("expected nil store when path is empty")
	}
}

type mockCommandError struct {
	msg string
}

func (e *mockCommandError) Error() string {
	return e.msg
}
