package control

import (
	"testing"
	"time"

	"github.com/elhamdev/gpu-guardian/internal/telemetry"
	"github.com/elhamdev/gpu-guardian/internal/throughput"
)

func newSample(ts time.Time, temp int, tempValid bool, throughputValue float64) (telemetry.TelemetrySample, throughput.Sample) {
	return telemetry.TelemetrySample{
			Timestamp: ts,
			TempC:     temp,
			TempValid: tempValid,
		}, throughput.Sample{
			Timestamp:  ts,
			Throughput: throughputValue,
		}
}

func TestRuleController_DecreaseOnHardTemp(t *testing.T) {
	c := NewRuleController(RuleConfig{
		SoftTemp:             78,
		HardTemp:             84,
		ThroughputFloorRatio: 0.7,
		ThroughputWindowSec:  30,
		ThroughputFloorSec:   30,
	})
	telemetrySample, throughputSample := newSample(time.Now(), 84, true, 100)
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
		SoftTemp:             78,
		HardTemp:             90,
		ThroughputFloorRatio: 0.7,
		ThroughputWindowSec:  30,
		ThroughputFloorSec:   10,
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
		SoftTemp:             78,
		HardTemp:             90,
		ThroughputFloorRatio: 0.7,
		ThroughputWindowSec:  30,
		ThroughputFloorSec:   30,
	})
	telemetrySamples := []telemetry.TelemetrySample{
		{Timestamp: now.Add(-2 * time.Second), TempC: 60, TempValid: true},
		{Timestamp: now, TempC: 61, TempValid: true},
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
