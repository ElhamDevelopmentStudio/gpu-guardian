package control

import (
	"testing"
	"time"

	"github.com/elhamdev/gpu-guardian/internal/telemetry"
	"github.com/elhamdev/gpu-guardian/internal/throughput"
)

func TestStateEstimatorComputesDerivedSignals(t *testing.T) {
	estimator := NewStateEstimatorWithConfig(EstimateConfig{SmoothingFactor: 1})
	telemetrySamples := []telemetry.TelemetrySample{
		{
			Timestamp:         time.Unix(1, 0).UTC(),
			TempC:             60,
			TempValid:         true,
			ThrottleRisk:      0.6,
			ThrottleRiskValid: true,
		},
		{
			Timestamp:            time.Unix(4, 0).UTC(),
			TempC:                66,
			TempValid:            true,
			ThrottleRisk:         0.65,
			ThrottleRiskValid:    true,
			ThrottleReasons:      "power_cap",
			ThrottleReasonsValid: true,
		},
	}
	throughputSamples := []throughput.Sample{
		{Timestamp: time.Unix(2, 0).UTC(), Throughput: 50},
		{Timestamp: time.Unix(4, 0).UTC(), Throughput: 60},
	}

	estimate := estimator.Estimate(telemetrySamples, throughputSamples)
	if !estimate.TempSlopeValid {
		t.Fatal("expected temp slope valid")
	}
	if estimate.TempSlopeCPerSec != 2 {
		t.Fatalf("expected temp slope 2, got %.2f", estimate.TempSlopeCPerSec)
	}
	if !estimate.ThroughputTrendValid {
		t.Fatal("expected throughput trend valid")
	}
	if estimate.ThroughputTrend <= 0 {
		t.Fatalf("expected positive throughput trend, got %.4f", estimate.ThroughputTrend)
	}
	if !estimate.ThrottleRiskScoreValid {
		t.Fatal("expected throttle risk score valid")
	}
	if estimate.ThrottleRiskScore <= 0.6 || estimate.ThrottleRiskScore > 1 {
		t.Fatalf("expected throttle risk score increased by throttle reasons, got %.4f", estimate.ThrottleRiskScore)
	}
	if !estimate.StabilityIndexValid {
		t.Fatal("expected stability index valid")
	}
	if estimate.StabilityIndex <= 0 || estimate.StabilityIndex > 1 {
		t.Fatalf("expected stability index in (0,1], got %.4f", estimate.StabilityIndex)
	}
	if !estimate.ConfidenceValid {
		t.Fatal("expected estimate confidence valid")
	}
	if estimate.Confidence <= 0 {
		t.Fatalf("expected positive confidence, got %.4f", estimate.Confidence)
	}
}

func TestStateEstimatorAppliesSmoothing(t *testing.T) {
	estimator := NewStateEstimatorWithConfig(EstimateConfig{SmoothingFactor: 0.2})

	telemetrySamples1 := []telemetry.TelemetrySample{
		{Timestamp: time.Unix(1, 0).UTC(), TempC: 60, TempValid: true},
		{Timestamp: time.Unix(3, 0).UTC(), TempC: 64, TempValid: true},
	}
	throughSamples1 := []throughput.Sample{
		{Timestamp: time.Unix(1, 0).UTC(), Throughput: 10},
		{Timestamp: time.Unix(3, 0).UTC(), Throughput: 8},
	}
	first := estimator.Estimate(telemetrySamples1, throughSamples1)

	telemetrySamples2 := []telemetry.TelemetrySample{
		{Timestamp: time.Unix(1, 0).UTC(), TempC: 60, TempValid: true},
		{Timestamp: time.Unix(3, 0).UTC(), TempC: 64, TempValid: true},
		{Timestamp: time.Unix(5, 0).UTC(), TempC: 90, TempValid: true},
	}
	throughSamples2 := []throughput.Sample{
		{Timestamp: time.Unix(1, 0).UTC(), Throughput: 10},
		{Timestamp: time.Unix(3, 0).UTC(), Throughput: 8},
		{Timestamp: time.Unix(5, 0).UTC(), Throughput: 30},
	}
	second := estimator.Estimate(telemetrySamples2, throughSamples2)

	// raw slope second window is (90-64)/2 = 13 while smoothed should be much lower
	if second.TempSlopeCPerSec >= 13 {
		t.Fatalf("expected smoothed temp slope below raw spike, got %.4f", second.TempSlopeCPerSec)
	}
	if second.TempSlopeCPerSec <= first.TempSlopeCPerSec {
		t.Fatalf("expected estimated temp slope to move toward new value, got first=%.4f second=%.4f", first.TempSlopeCPerSec, second.TempSlopeCPerSec)
	}
	if second.ThroughputTrend <= 0 {
		t.Fatalf("expected positive throughput trend on second sample, got %.4f", second.ThroughputTrend)
	}
	if second.Confidence != first.Confidence {
		t.Fatalf("expected confidence to remain stable with equivalent valid signals, got first=%.4f second=%.4f", first.Confidence, second.Confidence)
	}
}

func TestStateEstimatorHandlesMissingData(t *testing.T) {
	estimator := NewStateEstimator()
	telemetrySamples := []telemetry.TelemetrySample{
		{Timestamp: time.Unix(1, 0).UTC(), TempValid: false},
		{Timestamp: time.Unix(2, 0).UTC(), TempValid: false},
	}
	throughputSamples := []throughput.Sample{
		{Timestamp: time.Unix(1, 0).UTC(), Throughput: 0},
		{Timestamp: time.Unix(2, 0).UTC(), Throughput: 0},
	}

	estimate := estimator.Estimate(telemetrySamples, throughputSamples)
	if estimate.TempSlopeValid {
		t.Fatal("expected invalid temp slope on missing data")
	}
	if !estimate.ThroughputTrendValid {
		t.Fatal("expected throughput trend valid even if flat zeros")
	}
	if !estimate.StabilityIndexValid {
		t.Fatal("expected stability index valid with throughput-only signal")
	}
	if estimate.Confidence >= 1 {
		t.Fatalf("expected reduced confidence on missing telemetry, got %.4f", estimate.Confidence)
	}
}
