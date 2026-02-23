package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/elhamdev/gpu-guardian/internal/control"
	"github.com/elhamdev/gpu-guardian/internal/telemetry"
	"github.com/elhamdev/gpu-guardian/internal/throughput"
)

func TestEngineLifecycleTransitionsWithImmediateShutdown(t *testing.T) {
	adapter := &fakeAdapter{}
	ctrl := control.NewRuleController(control.RuleConfig{})

	e := New(
		Config{
			Command:               "python generate_xtts.py",
			MinConcurrency:        1,
			MaxConcurrency:        4,
			StartConcurrency:      1,
			AdjustmentCooldown:    0,
			PollInterval:          0,
			ThroughputFloorWindow: 0,
			ThroughputWindow:      0,
		},
		adapter,
		ctrl,
		nil,
		nil,
		nil,
	)

	initial := e.Lifecycle()
	if initial.Phase != LifecycleIdle {
		t.Fatalf("expected initial lifecycle phase %s, got %s", LifecycleIdle, initial.Phase)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := e.Start(ctx)
	if err != nil {
		t.Fatalf("engine start with canceled context should not error: %v", err)
	}
	if result == nil {
		t.Fatal("expected engine result")
	}
	if result.Reason != "shutdown_requested" {
		t.Fatalf("expected shutdown reason, got %q", result.Reason)
	}
	final := e.Lifecycle()
	if final.Phase != LifecycleStopped {
		t.Fatalf("expected final lifecycle phase %s, got %s", LifecycleStopped, final.Phase)
	}
}

func TestEngineLifecycleInvalidConfig(t *testing.T) {
	adapter := &fakeAdapter{}
	ctrl := control.NewRuleController(control.RuleConfig{})

	e := New(
		Config{},
		adapter,
		ctrl,
		nil,
		nil,
		nil,
	)

	_, err := e.Start(context.Background())
	if err == nil {
		t.Fatal("expected invalid config error")
	}
	if e.Lifecycle().Phase != LifecycleFailed {
		t.Fatalf("expected lifecycle phase %s on invalid config, got %s", LifecycleFailed, e.Lifecycle().Phase)
	}
}

func TestEngineAppliesBoundedConcurrencyStep(t *testing.T) {
	adapter := &fakeAdapter{}
	ctrl := &scriptedController{
		actions: []control.Action{
			{Type: control.ActionIncrease, Concurrency: 10},
		},
	}

	e := New(
		Config{
			Command:            "python generate_xtts.py",
			MinConcurrency:     1,
			MaxConcurrency:     10,
			StartConcurrency:   2,
			AdjustmentCooldown: 0,
			PollInterval:       10 * time.Millisecond,
			MaxConcurrencyStep: 2,
			MaxTicks:           2,
		},
		adapter,
		ctrl,
		nil,
		nil,
		nil,
	)

	result, err := e.Start(context.Background())
	if err != nil {
		t.Fatalf("engine start failed: %v", err)
	}
	if result == nil {
		t.Fatal("expected engine result")
	}
	if adapter.restartCount != 1 {
		t.Fatalf("expected one restart, got %d", adapter.restartCount)
	}
	if result.State.CurrentConcurrency != 4 {
		t.Fatalf("expected bounded concurrency 4, got %d", result.State.CurrentConcurrency)
	}
}

func TestEngineHoldsOnDirectionalCooldown(t *testing.T) {
	adapter := &fakeAdapter{}
	ctrl := &scriptedController{
		actions: []control.Action{
			{Type: control.ActionIncrease, Concurrency: 3},
			{Type: control.ActionDecrease, Concurrency: 1},
			{Type: control.ActionHold, Concurrency: 1},
		},
	}

	e := New(
		Config{
			Command:            "python generate_xtts.py",
			MinConcurrency:     1,
			MaxConcurrency:     4,
			StartConcurrency:   2,
			AdjustmentCooldown: 100 * time.Millisecond,
			PollInterval:       10 * time.Millisecond,
			MaxConcurrencyStep: 1,
			MaxTicks:           3,
		},
		adapter,
		ctrl,
		nil,
		nil,
		nil,
	)

	result, err := e.Start(context.Background())
	if err != nil {
		t.Fatalf("engine start failed: %v", err)
	}
	if result == nil {
		t.Fatal("expected engine result")
	}
	if result.State.CurrentConcurrency != 3 {
		t.Fatalf("expected concurrency 3 after first increase, got %d", result.State.CurrentConcurrency)
	}
	if adapter.restartCount != 1 {
		t.Fatalf("expected one restart due cooldown hold, got %d", adapter.restartCount)
	}
}

type fakeAdapter struct {
	mu           sync.Mutex
	pid          int
	running      bool
	restartCount int
}

func (a *fakeAdapter) Start(_ context.Context, _ string, _ int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.running = true
	a.pid++
	if a.pid == 0 {
		a.pid = 1
	}
	return nil
}

func (a *fakeAdapter) Pause(context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.running = false
	return nil
}

func (a *fakeAdapter) Resume(context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.running = true
	return nil
}

func (a *fakeAdapter) UpdateParameters(context.Context, int) error {
	return nil
}

func (a *fakeAdapter) GetThroughput() uint64 {
	return 0
}

func (a *fakeAdapter) GetProgress() float64 {
	return 0
}

func (a *fakeAdapter) Restart(context.Context, int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.restartCount++
	a.pid++
	if a.pid == 0 {
		a.pid = 1
	}
	a.running = true
	return nil
}

func (a *fakeAdapter) Stop() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.running = false
	return nil
}

func (a *fakeAdapter) GetPID() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.running {
		return 0
	}
	return a.pid
}

func (a *fakeAdapter) IsRunning() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.running
}

func (a *fakeAdapter) OutputBytes() uint64 {
	return 0
}

type scriptedController struct {
	actions []control.Action
	idx     int
}

func (s *scriptedController) Decide(_ []telemetry.TelemetrySample, _ []throughput.Sample, _ control.State) control.Action {
	if s.idx >= len(s.actions) {
		return control.Action{Type: control.ActionHold}
	}
	action := s.actions[s.idx]
	s.idx++
	return action
}
