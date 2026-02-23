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
	Type        ActionType `json:"type"`
	Concurrency int        `json:"concurrency"`
	Reason      string     `json:"reason"`
}

type State struct {
	CurrentConcurrency int
	MinConcurrency     int
	MaxConcurrency     int
	BaselineThroughput float64
	LastActionAt       time.Time
}

type Controller interface {
	Decide(samples []telemetry.TelemetrySample, through []throughput.Sample, state State) Action
}

type RuleConfig struct {
	SoftTemp             float64
	HardTemp             float64
	ThroughputFloorRatio float64
	ThroughputWindowSec  int
	ThroughputFloorSec   int
}

type RuleController struct {
	SoftTemp              float64
	HardTemp              float64
	ThroughputFloorRatio  float64
	ThroughputWindow      time.Duration
	ThroughputFloorWindow time.Duration
}

func NewRuleController(cfg RuleConfig) *RuleController {
	return &RuleController{
		SoftTemp:              cfg.SoftTemp,
		HardTemp:              cfg.HardTemp,
		ThroughputFloorRatio:  cfg.ThroughputFloorRatio,
		ThroughputWindow:      time.Duration(cfg.ThroughputWindowSec) * time.Second,
		ThroughputFloorWindow: time.Duration(cfg.ThroughputFloorSec) * time.Second,
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

func (c *RuleController) shouldIncrease(state State, samples []telemetry.TelemetrySample, avgTp float64) bool {
	if state.CurrentConcurrency >= state.MaxConcurrency {
		return false
	}
	temp, tempValid := latestTemp(samples)
	if !tempValid {
		return true
	}
	if float64(temp) > c.SoftTemp-2 {
		return false
	}
	if avgTp <= 0 {
		return false
	}
	if state.BaselineThroughput > 0 {
		ratio := avgTp / state.BaselineThroughput
		return ratio >= c.ThroughputFloorRatio
	}
	return true
}

func (c *RuleController) Decide(samples []telemetry.TelemetrySample, through []throughput.Sample, state State) Action {
	action := Action{
		Type:        ActionHold,
		Concurrency: state.CurrentConcurrency,
		Reason:      "no-op",
	}

	now := time.Now()
	avgTp := c.avgThroughput(through, c.ThroughputWindow, now)

	temp, tempValid := latestTemp(samples)
	prevTemp, prevTempValid := previousTemp(samples)
	if tempValid {
		if float64(temp) >= c.HardTemp {
			action = Action{
				Type:        ActionDecrease,
				Concurrency: state.CurrentConcurrency - 1,
				Reason:      "temperature at hard limit",
			}
			return action
		}

		if prevTempValid && temp >= int(c.SoftTemp) && temp > prevTemp {
			action = Action{
				Type:        ActionDecrease,
				Concurrency: state.CurrentConcurrency - 1,
				Reason:      "temperature rising at/above soft limit",
			}
			return action
		}
	}

	if state.BaselineThroughput > 0 {
		threshold := state.BaselineThroughput * c.ThroughputFloorRatio
		if throughputBelowThreshold(through, now, threshold, time.Duration(c.ThroughputFloorWindow)) && avgTp > 0 {
			action = Action{
				Type:        ActionDecrease,
				Concurrency: state.CurrentConcurrency - 1,
				Reason:      "throughput below floor sustained",
			}
			return action
		}
	}

	if c.shouldIncrease(state, samples, avgTp) {
		action = Action{
			Type:        ActionIncrease,
			Concurrency: state.CurrentConcurrency + 1,
			Reason:      "temperature healthy and throughput stable",
		}
		return action
	}

	return action
}
