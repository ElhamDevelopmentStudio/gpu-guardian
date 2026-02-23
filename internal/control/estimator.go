package control

import (
	"math"
	"strings"
	"time"

	"github.com/elhamdev/gpu-guardian/internal/telemetry"
	"github.com/elhamdev/gpu-guardian/internal/throughput"
)

type StateEstimate struct {
	Timestamp time.Time `json:"timestamp"`

	TempSlopeCPerSec     float64 `json:"temp_slope_c_per_sec"`
	TempSlopeValid       bool    `json:"temp_slope_valid"`
	ThroughputTrend      float64 `json:"throughput_trend"`
	ThroughputTrendValid bool    `json:"throughput_trend_valid"`

	ThrottleRiskScore      float64 `json:"throttle_risk_score"`
	ThrottleRiskScoreValid bool    `json:"throttle_risk_score_valid"`
	StabilityIndex         float64 `json:"stability_index"`
	StabilityIndexValid    bool    `json:"stability_index_valid"`

	Confidence      float64 `json:"confidence"`
	ConfidenceValid bool    `json:"confidence_valid"`
}

type EstimateConfig struct {
	SmoothingFactor float64
}

type StateEstimator struct {
	smoothing   float64
	initialized bool

	smoothedTempSlope       float64
	smoothedThroughputTrend float64
	smoothedThrottleRisk    float64
	smoothedStability       float64
	smoothedConfidence      float64
}

func NewStateEstimator() *StateEstimator {
	return NewStateEstimatorWithConfig(EstimateConfig{})
}

func NewStateEstimatorWithConfig(cfg EstimateConfig) *StateEstimator {
	alpha := cfg.SmoothingFactor
	if alpha <= 0 || alpha >= 1 {
		alpha = 0.35
	}
	return &StateEstimator{smoothing: alpha}
}

func (e *StateEstimator) Estimate(
	telemetrySamples []telemetry.TelemetrySample,
	throughSamples []throughput.Sample,
) StateEstimate {
	estimate := StateEstimate{
		Timestamp: time.Now(),
	}

	rawTempSlope, tempSlopeValid := tempSlope(telemetrySamples)
	rawThroughputTrend, throughputTrendValid := throughputTrend(throughSamples)
	rawThrottleRisk, throttleRiskValid := throttleRiskScore(telemetrySamples)

	estimate.TempSlopeValid = tempSlopeValid
	estimate.ThroughputTrendValid = throughputTrendValid
	estimate.ThrottleRiskScoreValid = throttleRiskValid

	rawStability, stabilityValid := stabilityEstimate(
		rawTempSlope,
		tempSlopeValid,
		rawThroughputTrend,
		throughputTrendValid,
		rawThrottleRisk,
		throttleRiskValid,
	)

	estimate.TempSlopeCPerSec = e.updateEstimate(
		rawTempSlope,
		tempSlopeValid,
		&estimate.TempSlopeCPerSec,
		&e.smoothedTempSlope,
		e.initialized,
	)
	estimate.ThroughputTrend = e.updateEstimate(
		rawThroughputTrend,
		throughputTrendValid,
		&estimate.ThroughputTrend,
		&e.smoothedThroughputTrend,
		e.initialized,
	)
	estimate.ThrottleRiskScore = e.updateEstimate(
		rawThrottleRisk,
		throttleRiskValid,
		&estimate.ThrottleRiskScore,
		&e.smoothedThrottleRisk,
		e.initialized,
	)
	estimate.StabilityIndex = e.updateEstimate(
		rawStability,
		stabilityValid,
		&estimate.StabilityIndex,
		&e.smoothedStability,
		e.initialized,
	)
	estimate.StabilityIndexValid = stabilityValid

	estimate.Confidence = e.updateConfidence(confidence(tempSlopeValid, throughputTrendValid, throttleRiskValid), e.initialized)
	estimate.ConfidenceValid = true

	e.initialized = true
	return estimate
}

func (e *StateEstimator) updateEstimate(
	raw float64,
	valid bool,
	observed *float64,
	smoothed *float64,
	initialized bool,
) float64 {
	if !valid {
		*observed = *smoothed
		return *observed
	}
	if !initialized {
		*smoothed = raw
		*observed = raw
		return raw
	}
	*smoothed = (e.smoothing * raw) + ((1 - e.smoothing) * *smoothed)
	*observed = *smoothed
	return *observed
}

func (e *StateEstimator) updateConfidence(raw float64, initialized bool) float64 {
	if !initialized {
		e.smoothedConfidence = raw
		return raw
	}
	e.smoothedConfidence = (e.smoothing * raw) + ((1 - e.smoothing) * e.smoothedConfidence)
	return e.smoothedConfidence
}

func tempSlope(samples []telemetry.TelemetrySample) (float64, bool) {
	var current, previous telemetry.TelemetrySample
	found := false
	for i := len(samples) - 1; i >= 0; i-- {
		s := samples[i]
		if !s.TempValid {
			continue
		}
		if !found {
			current = s
			found = true
			continue
		}
		previous = s
		break
	}
	if !found || !previous.TempValid {
		return 0, false
	}

	delta := current.Timestamp.Sub(previous.Timestamp).Seconds()
	if delta <= 0 {
		return 0, false
	}

	return float64(current.TempC-previous.TempC) / delta, true
}

func throughputTrend(samples []throughput.Sample) (float64, bool) {
	if len(samples) < 2 {
		return 0, false
	}

	var current, previous throughput.Sample
	found := false
	for i := len(samples) - 1; i >= 0; i-- {
		s := samples[i]
		if s.Timestamp.IsZero() {
			continue
		}
		if !found {
			current = s
			found = true
			continue
		}
		previous = s
		break
	}
	if !found || previous.Timestamp.IsZero() {
		return 0, false
	}

	deltaT := current.Timestamp.Sub(previous.Timestamp).Seconds()
	if deltaT <= 0 {
		return 0, false
	}
	if previous.Throughput == 0 {
		if current.Throughput == 0 {
			return 0, true
		}
		return 1, true
	}
	return (current.Throughput - previous.Throughput) / previous.Throughput, true
}

func throttleRiskScore(samples []telemetry.TelemetrySample) (float64, bool) {
	for i := len(samples) - 1; i >= 0; i-- {
		s := samples[i]
		if !s.ThrottleRiskValid && !s.ThrottleReasonsValid {
			continue
		}
		score := s.ThrottleRisk
		if s.ThrottleReasonsValid && hasThrottleReason(s.ThrottleReasons) {
			score += 0.2
		}
		return clamp01(score), true
	}
	return 0, false
}

func hasThrottleReason(raw string) bool {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "", "0", "none", "[none]", "(none)":
		return false
	default:
		return true
	}
}

func stabilityEstimate(
	tempSlope float64,
	tempSlopeValid bool,
	throughputTrend float64,
	throughputTrendValid bool,
	throttleRisk float64,
	throttleRiskValid bool,
) (float64, bool) {
	validCount := 0
	for _, valid := range []bool{tempSlopeValid, throughputTrendValid, throttleRiskValid} {
		if valid {
			validCount++
		}
	}
	if validCount == 0 {
		return 0, false
	}

	tempTerm := 0.0
	if tempSlopeValid {
		tempTerm = clamp01(math.Abs(tempSlope) / 5.0)
	}
	trendTerm := 0.0
	if throughputTrendValid {
		trendTerm = clamp01(math.Abs(throughputTrend) / 0.5)
	}
	riskTerm := 0.0
	if throttleRiskValid {
		riskTerm = clamp01(throttleRisk)
	}

	return clamp(1-((0.45*tempTerm)+(0.35*trendTerm)+(0.2*riskTerm)), 0, 1), true
}

func confidence(tempSlopeValid, throughputTrendValid, throttleRiskValid bool) float64 {
	validCount := 0
	for _, valid := range []bool{tempSlopeValid, throughputTrendValid, throttleRiskValid} {
		if valid {
			validCount++
		}
	}
	return clamp(float64(validCount)/3.0, 0, 1)
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func clamp(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
