package throughput

import (
	"sync"
	"time"
)

type Sample struct {
	Timestamp  time.Time `json:"timestamp"`
	Throughput float64   `json:"throughput_units_per_sec"`
}

type Tracker struct {
	mu               sync.Mutex
	throughputWindow time.Duration
	baselineWindow   time.Duration

	samples []Sample

	lastTotal     uint64
	lastSample    time.Time
	startedAt     time.Time
	baseline      float64
	baselineReady bool
}

func NewTracker(throughputWindow, baselineWindow time.Duration) *Tracker {
	return &Tracker{
		throughputWindow: throughputWindow,
		baselineWindow:   baselineWindow,
	}
}

func (t *Tracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.samples = nil
	t.lastSample = time.Time{}
	t.startedAt = time.Time{}
	t.lastTotal = 0
	t.baseline = 0
	t.baselineReady = false
}

func (t *Tracker) Add(totalUnits uint64, ts time.Time) Sample {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.startedAt.IsZero() {
		t.startedAt = ts
		t.lastSample = ts
		t.lastTotal = totalUnits
		s := Sample{Timestamp: ts, Throughput: 0}
		t.samples = append(t.samples, s)
		return s
	}

	deltaT := ts.Sub(t.lastSample).Seconds()
	th := 0.0
	if deltaT > 0 && totalUnits >= t.lastTotal {
		th = float64(totalUnits-t.lastTotal) / deltaT
	}
	t.lastSample = ts
	t.lastTotal = totalUnits

	s := Sample{Timestamp: ts, Throughput: th}
	t.samples = append(t.samples, s)
	t.dropOldSamplesLocked(ts)
	t.updateBaselineLocked(ts)

	return s
}

func (t *Tracker) Samples() []Sample {
	t.mu.Lock()
	defer t.mu.Unlock()
	cp := make([]Sample, len(t.samples))
	copy(cp, t.samples)
	return cp
}

func (t *Tracker) Baseline() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.baseline
}

// RestoreBaseline seeds baseline estimation when continuing from a checkpoint/profile.
func (t *Tracker) RestoreBaseline(throughput float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.baseline = throughput
	t.baselineReady = throughput > 0
}

func (t *Tracker) IsBaselineReady() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.baselineReady
}

func (t *Tracker) Average(window time.Duration) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.averageLocked(time.Now(), window)
}

func (t *Tracker) dropOldSamplesLocked(now time.Time) {
	cutoff := now.Add(-t.throughputWindow)
	i := 0
	for ; i < len(t.samples); i++ {
		if t.samples[i].Timestamp.After(cutoff) {
			break
		}
	}
	if i > 0 {
		t.samples = append([]Sample(nil), t.samples[i:]...)
	}
}

func (t *Tracker) updateBaselineLocked(now time.Time) {
	if t.baselineReady || t.startedAt.IsZero() {
		return
	}
	if now.Sub(t.startedAt) < t.baselineWindow {
		return
	}
	if len(t.samples) == 0 {
		return
	}
	t.baseline = t.averageLocked(now, t.baselineWindow)
	t.baselineReady = true
}

func (t *Tracker) averageLocked(now time.Time, window time.Duration) float64 {
	if len(t.samples) == 0 {
		return 0
	}
	cutoff := now.Add(-window)
	sum := 0.0
	count := 0
	for _, sample := range t.samples {
		if sample.Timestamp.Before(cutoff) {
			continue
		}
		sum += sample.Throughput
		count++
	}
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}
