package throughput

import (
	"testing"
	"time"
)

// Regression: ensure baseline is not considered ready before the configured warmup window.
func TestRegression_BaselineNotReadyBeforeWindow(t *testing.T) {
	tr := NewTracker(10*time.Second, 5*time.Second)
	start := time.Now()
	tr.Add(0, start)

	tr.Add(10, start.Add(time.Second))
	tr.Add(30, start.Add(2*time.Second))
	tr.Add(60, start.Add(3*time.Second))

	if tr.IsBaselineReady() {
		t.Fatal("baseline should not be ready before baseline window elapses")
	}
	if got := tr.Baseline(); got != 0 {
		t.Fatalf("expected baseline 0 before readiness, got %f", got)
	}
}
