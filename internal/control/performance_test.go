package control

import (
	"sort"
	"testing"
	"time"

	"github.com/elhamdev/gpu-guardian/internal/telemetry"
	"github.com/elhamdev/gpu-guardian/internal/throughput"
)

func TestRuleDecisionLatencyBudget(t *testing.T) {
	ctrl := NewRuleController(RuleConfig{})
	state := State{
		CurrentConcurrency: 4,
		MinConcurrency:     1,
		MaxConcurrency:     12,
		BaselineThroughput: 100,
		LastActionAt:       time.Now().Add(-time.Minute),
	}

	samples := make([]telemetry.TelemetrySample, 0, 80)
	throughputSamples := make([]throughput.Sample, 0, 80)
	for i := 0; i < 80; i++ {
		now := time.Now().Add(time.Duration(-80+i) * time.Millisecond)
		samples = append(samples, telemetry.TelemetrySample{
			TempC:               64 + i%6,
			TempValid:           true,
			UtilPct:             70,
			UtilValid:           true,
			VramUsedMB:          4096,
			VramUsedValid:       true,
			VramTotalMB:         8192,
			VramTotalValid:      true,
			Timestamp:           now,
			MemoryPressure:      0.5,
			MemoryPressureValid: true,
			ThrottleRisk:        0.2,
			ThrottleRiskValid:   true,
		})
		throughputSamples = append(throughputSamples, throughput.Sample{Timestamp: now, Throughput: 95 + float64(i)})
	}

	const iterations = 2000
	durations := make([]time.Duration, 0, iterations)
	for i := 0; i < iterations; i++ {
		start := time.Now()
		_ = ctrl.Decide(samples, throughputSamples, state)
		durations = append(durations, time.Since(start))
	}

	sort.Slice(durations, func(i, j int) bool {
		return durations[i] < durations[j]
	})

	p95 := durations[(len(durations)*95)/100-1]
	if p95 > 50*time.Millisecond {
		t.Fatalf("rule decision p95=%s exceeds 50ms budget", p95)
	}
}

func BenchmarkRuleDecisionLatency(b *testing.B) {
	ctrl := NewRuleController(RuleConfig{})
	state := State{
		CurrentConcurrency: 4,
		MinConcurrency:     1,
		MaxConcurrency:     12,
		BaselineThroughput: 100,
		LastActionAt:       time.Now().Add(-time.Minute),
	}

	samples := make([]telemetry.TelemetrySample, 0, 80)
	throughputSamples := make([]throughput.Sample, 0, 80)
	for i := 0; i < 80; i++ {
		now := time.Now().Add(time.Duration(-80+i) * time.Millisecond)
		samples = append(samples, telemetry.TelemetrySample{
			TempC:               64 + i%6,
			TempValid:           true,
			UtilPct:             70,
			UtilValid:           true,
			VramUsedMB:          4096,
			VramUsedValid:       true,
			VramTotalMB:         8192,
			VramTotalValid:      true,
			Timestamp:           now,
			MemoryPressure:      0.5,
			MemoryPressureValid: true,
			ThrottleRisk:        0.2,
			ThrottleRiskValid:   true,
		})
		throughputSamples = append(
			throughputSamples,
			throughput.Sample{Timestamp: now, Throughput: 95 + float64(i)},
		)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = ctrl.Decide(samples, throughputSamples, state)
	}
}
