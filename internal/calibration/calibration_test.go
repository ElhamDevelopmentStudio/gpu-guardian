package calibration

import (
	"context"
	"testing"
	"time"

	"github.com/elhamdev/gpu-guardian/internal/telemetry"
)

type fakeCalibrationAdapter struct {
	throughput  map[int]uint64
	concurrency int
	running     bool
	totalOutput uint64
}

func (f *fakeCalibrationAdapter) Start(ctx context.Context, cmd string, concurrency int) error {
	_ = ctx
	_ = cmd
	f.concurrency = concurrency
	f.totalOutput = 0
	f.running = true
	return nil
}

func (f *fakeCalibrationAdapter) Restart(ctx context.Context, concurrency int) error {
	_ = ctx
	f.concurrency = concurrency
	f.totalOutput = 0
	f.running = true
	return nil
}

func (f *fakeCalibrationAdapter) Stop() error {
	f.running = false
	return nil
}

func (f *fakeCalibrationAdapter) GetThroughput() uint64 {
	if !f.running {
		return f.totalOutput
	}
	increment := f.throughput[f.concurrency]
	f.totalOutput += increment
	return f.totalOutput
}

func (f *fakeCalibrationAdapter) IsRunning() bool {
	return f.running
}

type scriptedTelemetrySource struct {
	samples []telemetry.TelemetrySample
	index   int
}

func (s *scriptedTelemetrySource) Sample(_ context.Context) telemetry.TelemetrySample {
	if s.index >= len(s.samples) {
		return s.samples[len(s.samples)-1]
	}
	i := s.index
	s.index++
	return s.samples[i]
}

func TestRunComputesSafeConcurrencyAndVramFootprint(t *testing.T) {
	adapter := &fakeCalibrationAdapter{
		throughput: map[int]uint64{
			1: 100,
			2: 90,
			3: 50,
		},
	}
	source := &scriptedTelemetrySource{
		samples: []telemetry.TelemetrySample{
			// concurrency 1
			{TempValid: true, TempC: 55, VramUsedValid: true, VramUsedMB: 1000},
			{TempValid: true, TempC: 56, VramUsedValid: true, VramUsedMB: 1000},
			{TempValid: true, TempC: 56, VramUsedValid: true, VramUsedMB: 1000},
			{TempValid: true, TempC: 56, VramUsedValid: true, VramUsedMB: 1000},
			// concurrency 2
			{TempValid: true, TempC: 63, VramUsedValid: true, VramUsedMB: 1020},
			{TempValid: true, TempC: 64, VramUsedValid: true, VramUsedMB: 1020},
			{TempValid: true, TempC: 64, VramUsedValid: true, VramUsedMB: 1020},
			{TempValid: true, TempC: 64, VramUsedValid: true, VramUsedMB: 1020},
			// concurrency 3
			{TempValid: true, TempC: 92, VramUsedValid: true, VramUsedMB: 1040},
			{TempValid: true, TempC: 93, VramUsedValid: true, VramUsedMB: 1040},
			{TempValid: true, TempC: 93, VramUsedValid: true, VramUsedMB: 1040},
			{TempValid: true, TempC: 94, VramUsedValid: true, VramUsedMB: 1040},
		},
	}

	profile, err := Run(context.Background(), Config{
		Command:             "example-cmd",
		MinConcurrency:      1,
		MaxConcurrency:      3,
		ConcurrencyStep:     1,
		StepSamples:         4,
		WarmupSamples:       1,
		PollInterval:        10 * time.Millisecond,
		HardTempC:           84,
		ThroughputDropRatio: 0.7,
	}, adapter, source)
	if err != nil {
		t.Fatalf("calibration failed: %v", err)
	}
	if profile.SafeConcurrencyCeiling != 2 {
		t.Fatalf("expected safe concurrency ceiling 2, got %d", profile.SafeConcurrencyCeiling)
	}
	if profile.BaselineConcurrency != 1 {
		t.Fatalf("expected baseline concurrency 1, got %d", profile.BaselineConcurrency)
	}
	if len(profile.ThermalSaturationCurve) != 3 {
		t.Fatalf("expected 3 sweep points, got %d", len(profile.ThermalSaturationCurve))
	}
	if profile.VramPerLoadUnitMB < 10 {
		t.Fatalf("expected positive vram-per-load estimate, got %f", profile.VramPerLoadUnitMB)
	}
}

func TestRunReturnsErrorForMissingCommand(t *testing.T) {
	_, err := Run(context.Background(), Config{
		MinConcurrency: 1,
		MaxConcurrency: 1,
	}, &fakeCalibrationAdapter{}, &scriptedTelemetrySource{})
	if err == nil {
		t.Fatal("expected command validation error")
	}
}
