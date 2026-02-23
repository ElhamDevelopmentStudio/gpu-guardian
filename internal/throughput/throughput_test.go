package throughput

import (
	"testing"
	"time"
)

func TestTrackerBaseline(t *testing.T) {
	tr := NewTracker(3*time.Second, 2*time.Second)
	start := time.Now()

	s1 := tr.Add(100, start)
	if s1.Throughput != 0 {
		t.Fatalf("expected first sample throughput 0, got %f", s1.Throughput)
	}

	s2 := tr.Add(200, start.Add(time.Second))
	if s2.Throughput != 100 {
		t.Fatalf("unexpected throughput %f", s2.Throughput)
	}

	s3 := tr.Add(260, start.Add(2*time.Second))
	if s3.Throughput != 60 {
		t.Fatalf("unexpected throughput %f", s3.Throughput)
	}

	s4 := tr.Add(320, start.Add(3*time.Second))
	if s4.Throughput != 60 {
		t.Fatalf("unexpected throughput %f", s4.Throughput)
	}

	if !tr.IsBaselineReady() {
		t.Fatal("expected baseline to become ready")
	}
	if got := tr.Baseline(); got <= 0 {
		t.Fatalf("expected positive baseline, got %f", got)
	}
}

func TestTrackerWindowAveragesSamples(t *testing.T) {
	tr := NewTracker(3*time.Second, 60*time.Second)
	start := time.Now()
	tr.Add(0, start)
	tr.Add(100, start.Add(time.Second))
	tr.Add(220, start.Add(2*time.Second))
	tr.Add(250, start.Add(3*time.Second))

	if avg := tr.Average(2 * time.Second); avg <= 0 {
		t.Fatalf("expected average over window, got %f", avg)
	}
}
