package control

import (
	"time"

	"github.com/elhamdev/gpu-guardian/internal/telemetry"
	"github.com/elhamdev/gpu-guardian/internal/throughput"
)

type ActionType string

const (
	ActionHold     ActionType = "hold"
	ActionIncrease ActionType = "increase"
	ActionDecrease ActionType = "decrease"
	ActionPause    ActionType = "pause"
)

type Action struct {
	Type            ActionType `json:"type"`
	Concurrency     int        `json:"concurrency"`
	Reason          string     `json:"reason"`
	CooldownSec     float64    `json:"cooldown_sec,omitempty"`
	ConcurrencyStep int        `json:"concurrency_step,omitempty"`
	BatchSize       int        `json:"batch_size,omitempty"`
	ChunkSize       int        `json:"chunk_size,omitempty"`
}

type State struct {
	CurrentConcurrency int
	MinConcurrency     int
	MaxConcurrency     int
	BaselineThroughput float64
	LastActionAt       time.Time
	Estimate           StateEstimate
}

type Controller interface {
	Decide(samples []telemetry.TelemetrySample, through []throughput.Sample, state State) Action
}

type RuleConfig struct {
	SoftTemp                         float64
	HardTemp                         float64
	ThroughputFloorRatio             float64
	ThroughputWindowSec              int
	ThroughputFloorSec               int
	ThroughputSlowdownFloorRatio     float64
	ThroughputRecoveryMaxAttempts    int
	ThroughputRecoveryStepMultiplier int
	TempHysteresisC                  float64
	ThroughputRecoveryMargin         float64
	MemoryPressureLimit              float64
	ThrottleRiskLimit                float64
	MaxConcurrencyStep               int
}

type RuleController struct {
	SoftTemp                         float64
	HardTemp                         float64
	ThroughputFloorRatio             float64
	ThroughputWindow                 time.Duration
	ThroughputFloorWindow            time.Duration
	ThroughputSlowdownFloorRatio     float64
	ThroughputRecoveryMaxAttempts    int
	ThroughputRecoveryStepMultiplier int
	TempHysteresisC                  float64
	ThroughputRecoveryMargin         float64
	MemoryPressureLimit              float64
	ThrottleRiskLimit                float64
	MaxConcurrencyStep               int

	throughputRecoveryAttempts int
}

func NewRuleController(cfg RuleConfig) *RuleController {
	return &RuleController{
		SoftTemp:                         cfg.SoftTemp,
		HardTemp:                         cfg.HardTemp,
		ThroughputFloorRatio:             cfg.ThroughputFloorRatio,
		ThroughputWindow:                 time.Duration(cfg.ThroughputWindowSec) * time.Second,
		ThroughputFloorWindow:            time.Duration(cfg.ThroughputFloorSec) * time.Second,
		ThroughputSlowdownFloorRatio:     cfg.ThroughputSlowdownFloorRatio,
		ThroughputRecoveryMaxAttempts:    cfg.ThroughputRecoveryMaxAttempts,
		ThroughputRecoveryStepMultiplier: cfg.ThroughputRecoveryStepMultiplier,
		TempHysteresisC:                  cfg.TempHysteresisC,
		ThroughputRecoveryMargin:         cfg.ThroughputRecoveryMargin,
		MemoryPressureLimit:              cfg.MemoryPressureLimit,
		ThrottleRiskLimit:                cfg.ThrottleRiskLimit,
		MaxConcurrencyStep:               cfg.MaxConcurrencyStep,
	}
}

func (c *RuleController) defaults() {
	if c.SoftTemp <= 0 {
		c.SoftTemp = 78
	}
	if c.HardTemp <= 0 {
		c.HardTemp = 84
	}
	if c.ThroughputFloorRatio <= 0 {
		c.ThroughputFloorRatio = 0.7
	}
	if c.TempHysteresisC < 0 {
		c.TempHysteresisC = 0
	}
	if c.ThroughputRecoveryMargin < 0 {
		c.ThroughputRecoveryMargin = 0
	}
	if c.ThroughputRecoveryMargin == 0 {
		c.ThroughputRecoveryMargin = 0.05
	}
	if c.ThroughputSlowdownFloorRatio <= 0 || c.ThroughputSlowdownFloorRatio > c.ThroughputFloorRatio {
		c.ThroughputSlowdownFloorRatio = 0.5
	}
	if c.ThroughputRecoveryMaxAttempts <= 0 {
		c.ThroughputRecoveryMaxAttempts = 3
	}
	if c.ThroughputRecoveryStepMultiplier <= 1 {
		c.ThroughputRecoveryStepMultiplier = 2
	}
	if c.MemoryPressureLimit <= 0 {
		c.MemoryPressureLimit = 0.9
	}
	if c.ThrottleRiskLimit <= 0 {
		c.ThrottleRiskLimit = 0.85
	}
	if c.MaxConcurrencyStep <= 0 {
		c.MaxConcurrencyStep = 1
	}
}

func latestTemp(samples []telemetry.TelemetrySample) (int, bool) {
	for i := len(samples) - 1; i >= 0; i-- {
		if samples[i].TempValid {
			return samples[i].TempC, true
		}
	}
	return 0, false
}

func previousTemp(samples []telemetry.TelemetrySample) (int, bool) {
	found := false
	for i := len(samples) - 1; i >= 0; i-- {
		if !samples[i].TempValid {
			continue
		}
		if found {
			return samples[i].TempC, true
		}
		found = true
	}
	return 0, false
}

func latestMemoryPressure(samples []telemetry.TelemetrySample) (float64, bool) {
	for i := len(samples) - 1; i >= 0; i-- {
		if samples[i].MemoryPressureValid {
			return samples[i].MemoryPressure, true
		}
	}
	return 0, false
}

func latestThrottleRisk(samples []telemetry.TelemetrySample) (float64, bool) {
	for i := len(samples) - 1; i >= 0; i-- {
		if samples[i].ThrottleRiskValid {
			return samples[i].ThrottleRisk, true
		}
	}
	return 0, false
}

func (c *RuleController) avgThroughput(samples []throughput.Sample, window time.Duration, now time.Time) float64 {
	if len(samples) == 0 {
		return 0
	}
	cutoff := now.Add(-window)
	sum := 0.0
	count := 0
	for _, s := range samples {
		if s.Timestamp.Before(cutoff) {
			continue
		}
		sum += s.Throughput
		count++
	}
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}

func (c *RuleController) shouldIncrease(state State, avgTp float64, temp int, memPressure float64, throttleRisk float64) bool {
	if state.CurrentConcurrency >= state.MaxConcurrency {
		return false
	}
	if float64(temp) > c.SoftTemp-c.TempHysteresisC {
		return false
	}
	if memPressure >= c.MemoryPressureLimit-0.03 {
		return false
	}
	if throttleRisk >= c.ThrottleRiskLimit*0.8 {
		return false
	}
	if state.BaselineThroughput <= 0 {
		return true
	}
	return avgTp/state.BaselineThroughput >= c.ThroughputFloorRatio+c.ThroughputRecoveryMargin
}

func throughputBelowThreshold(samples []throughput.Sample, now time.Time, threshold float64, window time.Duration) bool {
	if threshold <= 0 {
		return false
	}
	cutoff := now.Add(-window)
	for _, s := range samples {
		if s.Timestamp.Before(cutoff) {
			continue
		}
		if s.Throughput >= threshold {
			return false
		}
	}
	return true
}

func actionDecrease(current int, step int, reason string) Action {
	if step < 1 {
		step = 1
	}
	return Action{
		Type:        ActionDecrease,
		Concurrency: current - step,
		Reason:      reason,
		CooldownSec: 0,
	}
}

func actionIncrease(current int, step int, reason string) Action {
	if step < 1 {
		step = 1
	}
	return Action{
		Type:        ActionIncrease,
		Concurrency: current + step,
		Reason:      reason,
		CooldownSec: 0,
	}
}

func actionPause(reason string) Action {
	return Action{
		Type:        ActionPause,
		Reason:      reason,
		CooldownSec: 0,
		Concurrency: 0,
	}
}

func (c *RuleController) Decide(samples []telemetry.TelemetrySample, through []throughput.Sample, state State) Action {
	c.defaults()
	action := Action{
		Type:        ActionHold,
		Concurrency: state.CurrentConcurrency,
		Reason:      "no-op",
	}

	now := time.Now()
	avgTp := c.avgThroughput(through, c.ThroughputWindow, now)

	temp, tempValid := latestTemp(samples)
	prevTemp, prevTempValid := previousTemp(samples)
	memPressure, memPressureValid := latestMemoryPressure(samples)
	throttleRisk, throttleRiskValid := latestThrottleRisk(samples)
	if tempValid {
		if float64(temp) >= c.HardTemp {
			action = actionDecrease(state.CurrentConcurrency, c.MaxConcurrencyStep, "temperature at hard limit")
			action.CooldownSec = 2
			return action
		}

		if prevTempValid && temp >= int(c.SoftTemp) && temp > prevTemp {
			action = actionDecrease(state.CurrentConcurrency, c.MaxConcurrencyStep, "temperature rising at/above soft limit")
			action.CooldownSec = 1
			return action
		}
	}

	if memPressureValid && memPressure >= c.MemoryPressureLimit {
		action = actionDecrease(state.CurrentConcurrency, c.MaxConcurrencyStep, "memory pressure near saturation")
		action.CooldownSec = 1.5
		return action
	}

	if throttleRiskValid && throttleRisk >= c.ThrottleRiskLimit {
		action = actionDecrease(state.CurrentConcurrency, c.MaxConcurrencyStep, "throttle risk elevated")
		action.CooldownSec = 1.5
		return action
	}

	if state.BaselineThroughput > 0 {
		threshold := state.BaselineThroughput * c.ThroughputFloorRatio
		slowdownThreshold := state.BaselineThroughput * c.ThroughputSlowdownFloorRatio

		belowFloor := throughputBelowThreshold(through, now, threshold, time.Duration(c.ThroughputFloorWindow))
		belowSlowdown := throughputBelowThreshold(through, now, slowdownThreshold, time.Duration(c.ThroughputFloorWindow))

		if (belowFloor || belowSlowdown) && avgTp > 0 {
			c.throughputRecoveryAttempts++
			if c.throughputRecoveryAttempts > c.ThroughputRecoveryMaxAttempts {
				c.throughputRecoveryAttempts = c.ThroughputRecoveryMaxAttempts
				return actionPause("throughput recovery attempts exceeded, pausing to preserve state")
			}

			action.CooldownSec = 1.5
			if belowSlowdown {
				step := c.MaxConcurrencyStep * c.ThroughputRecoveryStepMultiplier
				return actionDecrease(state.CurrentConcurrency, step, "throughput below slowdown fallback, aggressive recovery")
			}
			return actionDecrease(state.CurrentConcurrency, c.MaxConcurrencyStep, "throughput below floor sustained")
		}

		c.throughputRecoveryAttempts = 0
	}

	if !tempValid {
		return action
	}

	if c.shouldIncrease(state, avgTp, temp, memPressure, throttleRisk) {
		action = actionIncrease(state.CurrentConcurrency, 1, "temperature and throughput stable")
		return action
	}

	return action
}
