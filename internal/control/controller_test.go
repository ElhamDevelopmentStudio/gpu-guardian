package control

import (
	"testing"
	"time"

	"github.com/elhamdev/gpu-guardian/internal/telemetry"
	"github.com/elhamdev/gpu-guardian/internal/throughput"
)

func newSample(ts time.Time, temp int, tempValid bool, throughputValue float64, memPressure float64, throttleRisk float64) (telemetry.TelemetrySample, throughput.Sample) {
	return telemetry.TelemetrySample{
			Timestamp:           ts,
			TempC:               temp,
			TempValid:           tempValid,
			MemoryPressure:      memPressure,
			MemoryPressureValid: memPressure > 0,
			ThrottleRisk:        throttleRisk,
			ThrottleRiskValid:   throttleRisk > 0,
		}, throughput.Sample{
			Timestamp:  ts,
			Throughput: throughputValue,
		}
}

func TestRuleController_DecreaseOnHardTemp(t *testing.T) {
	c := NewRuleController(RuleConfig{
		SoftTemp:                 78,
		HardTemp:                 84,
		TempHysteresisC:          2,
		ThroughputFloorRatio:     0.7,
		ThroughputWindowSec:      30,
		ThroughputFloorSec:       30,
		ThroughputRecoveryMargin: 0.05,
	})
	telemetrySample, throughputSample := newSample(time.Now(), 84, true, 100, 0, 0)
	state := State{
		CurrentConcurrency: 4,
		MinConcurrency:     1,
		MaxConcurrency:     8,
	}
	action := c.Decide([]telemetry.TelemetrySample{telemetrySample}, []throughput.Sample{throughputSample}, state)
	if action.Type != ActionDecrease {
		t.Fatalf("expected decrease on hard temp, got %s", action.Type)
	}
}

func TestRuleController_DecreaseOnThroughputFloor(t *testing.T) {
	now := time.Now()
	c := NewRuleController(RuleConfig{
		SoftTemp:                 78,
		HardTemp:                 90,
		TempHysteresisC:          2,
		ThroughputFloorRatio:     0.7,
		ThroughputWindowSec:      30,
		ThroughputFloorSec:       10,
		ThroughputRecoveryMargin: 0.05,
	})
	telemetrySamples := []telemetry.TelemetrySample{
		{Timestamp: now, TempC: 60, TempValid: true},
		{Timestamp: now.Add(time.Second), TempC: 61, TempValid: true},
	}
	throughSamples := []throughput.Sample{
		{Timestamp: now.Add(time.Second), Throughput: 5},
		{Timestamp: now.Add(2 * time.Second), Throughput: 4},
		{Timestamp: now.Add(3 * time.Second), Throughput: 3},
	}
	state := State{
		CurrentConcurrency: 4,
		MinConcurrency:     1,
		MaxConcurrency:     8,
		BaselineThroughput: 10,
	}
	action := c.Decide(telemetrySamples, throughSamples, state)
	if action.Type != ActionDecrease {
		t.Fatalf("expected decrease on throughput floor, got %s", action.Type)
	}
}

func TestRuleController_IncreaseWhenHealthy(t *testing.T) {
	now := time.Now()
	c := NewRuleController(RuleConfig{
		SoftTemp:                 78,
		HardTemp:                 90,
		TempHysteresisC:          2,
		ThroughputFloorRatio:     0.7,
		ThroughputWindowSec:      30,
		ThroughputFloorSec:       30,
		ThroughputRecoveryMargin: 0.05,
		MemoryPressureLimit:      0.9,
		ThrottleRiskLimit:        0.9,
	})
	telemetrySamples := []telemetry.TelemetrySample{
		{Timestamp: now.Add(-2 * time.Second), TempC: 60, TempValid: true, MemoryPressure: 0.5, MemoryPressureValid: true, ThrottleRisk: 0.1, ThrottleRiskValid: true},
		{Timestamp: now, TempC: 61, TempValid: true, MemoryPressure: 0.5, MemoryPressureValid: true, ThrottleRisk: 0.1, ThrottleRiskValid: true},
	}
	throughSamples := []throughput.Sample{
		{Timestamp: now.Add(-3 * time.Second), Throughput: 9},
		{Timestamp: now.Add(-2 * time.Second), Throughput: 10},
		{Timestamp: now.Add(-1 * time.Second), Throughput: 11},
	}
	state := State{
		CurrentConcurrency: 4,
		MinConcurrency:     1,
		MaxConcurrency:     8,
		BaselineThroughput: 12,
	}
	action := c.Decide(telemetrySamples, throughSamples, state)
	if action.Type != ActionIncrease {
		t.Fatalf("expected increase on healthy condition, got %s", action.Type)
	}
}

func TestRuleController_DecreaseOnMemoryPressure(t *testing.T) {
	now := time.Now()
	c := NewRuleController(RuleConfig{
		SoftTemp:                 78,
		HardTemp:                 90,
		TempHysteresisC:          2,
		ThroughputFloorRatio:     0.7,
		ThroughputWindowSec:      30,
		ThroughputFloorSec:       30,
		ThroughputRecoveryMargin: 0.05,
		MemoryPressureLimit:      0.75,
	})
	throughSamples := []throughput.Sample{
		{Timestamp: now.Add(-2 * time.Second), Throughput: 12},
		{Timestamp: now.Add(-1 * time.Second), Throughput: 11},
	}
	telemetrySamples := []telemetry.TelemetrySample{
		{Timestamp: now, TempC: 66, TempValid: true, MemoryPressure: 0.9, MemoryPressureValid: true},
	}
	state := State{
		CurrentConcurrency: 4,
		MinConcurrency:     1,
		MaxConcurrency:     8,
		BaselineThroughput: 12,
	}
	action := c.Decide(telemetrySamples, throughSamples, state)
	if action.Type != ActionDecrease {
		t.Fatalf("expected decrease on memory pressure, got %s", action.Type)
	}
}

func TestRuleController_HysteresisBeforeIncrease(t *testing.T) {
	now := time.Now()
	c := NewRuleController(RuleConfig{
		SoftTemp:                 78,
		HardTemp:                 90,
		TempHysteresisC:          2,
		ThroughputFloorRatio:     0.7,
		ThroughputWindowSec:      30,
		ThroughputFloorSec:       30,
		ThroughputRecoveryMargin: 0.05,
	})
	throughSamples := []throughput.Sample{
		{Timestamp: now.Add(-2 * time.Second), Throughput: 7},
		{Timestamp: now.Add(-1 * time.Second), Throughput: 7.2},
	}
	telemetrySamples := []telemetry.TelemetrySample{
		{Timestamp: now, TempC: 70, TempValid: true},
	}
	state := State{
		CurrentConcurrency: 4,
		MinConcurrency:     1,
		MaxConcurrency:     8,
		BaselineThroughput: 10,
	}
	action := c.Decide(telemetrySamples, throughSamples, state)
	if action.Type != ActionHold {
		t.Fatalf("expected hold due throughput floor hysteresis, got %s", action.Type)
	}
}

func TestRuleController_HighThrottleRiskTriggersDecrease(t *testing.T) {
	now := time.Now()
	c := NewRuleController(RuleConfig{
		SoftTemp:                 78,
		HardTemp:                 90,
		TempHysteresisC:          2,
		ThroughputFloorRatio:     0.7,
		ThroughputWindowSec:      30,
		ThroughputFloorSec:       30,
		ThroughputRecoveryMargin: 0.05,
		ThrottleRiskLimit:        0.5,
	})
	telemetrySamples := []telemetry.TelemetrySample{
		{Timestamp: now, TempC: 70, TempValid: true, ThrottleRisk: 0.8, ThrottleRiskValid: true},
	}
	throughSamples := []throughput.Sample{
		{Timestamp: now.Add(-1 * time.Second), Throughput: 11},
	}
	state := State{
		CurrentConcurrency: 4,
		MinConcurrency:     1,
		MaxConcurrency:     8,
		BaselineThroughput: 12,
	}
	action := c.Decide(telemetrySamples, throughSamples, state)
	if action.Type != ActionDecrease {
		t.Fatalf("expected decrease on elevated throttle risk, got %s", action.Type)
	}
}
