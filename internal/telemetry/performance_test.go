package telemetry

import (
	"context"
	"sort"
	"testing"
	"time"
)

func TestCollectorSampleLatencyBudget(t *testing.T) {
	originalRunner := runNvidiaSMICommand
	defer func() {
		runNvidiaSMICommand = originalRunner
	}()

	runNvidiaSMICommand = func(_ context.Context, _ string) ([]byte, error) {
		return []byte("GPU-11111111-1111-1111-1111-111111111111,70,95.0,4096,8192,80.0,120.0,1250.0,5000.0,0x0\n"), nil
	}

	collector := NewCollector()
	const iterations = 240
	durations := make([]time.Duration, 0, iterations)
	for i := 0; i < iterations; i++ {
		start := time.Now()
		_ = collector.Sample(context.Background())
		durations = append(durations, time.Since(start))
	}

	sort.Slice(durations, func(i, j int) bool {
		return durations[i] < durations[j]
	})

	p95 := durations[int(0.95*float64(len(durations))-1)]
	if p95 > 20*time.Millisecond {
		t.Fatalf("nvidia-smi parse/sample p95=%s exceeds 20ms budget", p95)
	}
}

func BenchmarkCollectorSampleLatency(b *testing.B) {
	originalRunner := runNvidiaSMICommand
	defer func() {
		runNvidiaSMICommand = originalRunner
	}()

	runNvidiaSMICommand = func(_ context.Context, _ string) ([]byte, error) {
		return []byte("GPU-11111111-1111-1111-1111-111111111111,70,95.0,4096,8192,80.0,120.0,1250.0,5000.0,0x0\n"), nil
	}

	collector := NewCollector()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = collector.Sample(context.Background())
	}
}
