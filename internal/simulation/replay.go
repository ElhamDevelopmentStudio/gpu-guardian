package simulation

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/elhamdev/gpu-guardian/internal/control"
	"github.com/elhamdev/gpu-guardian/internal/telemetry"
	"github.com/elhamdev/gpu-guardian/internal/throughput"
)

const defaultReplayPollInterval = 2 * time.Second
const maxTelemetryWindowSamples = 300

type ReplayConfig struct {
	TelemetryLogPath           string
	ControlLogPath             string
	MinConcurrency            int
	MaxConcurrency            int
	StartConcurrency          int
	MaxConcurrencyStep        int
	InitialBaselineThroughput float64
	RuleCfg                   control.RuleConfig
	AdjustmentCooldown        time.Duration
	ThroughputWindow          time.Duration
	BaselineWindow            time.Duration
	PollInterval              time.Duration
	EventLogPath              string
	MaxTicks                  int
}

type ReplayResult struct {
	Ticks             int           `json:"ticks"`
	TelemetrySamples  int           `json:"telemetry_samples"`
	DecisionSamples   int           `json:"decision_samples"`
	StartConcurrency  int           `json:"start_concurrency"`
	FinalConcurrency  int           `json:"final_concurrency"`
	FinalAction       string        `json:"final_action"`
	FinalReason       string        `json:"final_reason"`
	StartedAt         string        `json:"started_at"`
	CompletedAt       string        `json:"completed_at"`
	ThroughputSamples int           `json:"throughput_samples_used"`
	EventLogPath      string        `json:"event_log_path,omitempty"`
}

type throughputSamplePoint struct {
	Timestamp time.Time
	Rate      float64
}

type controlLogEvent struct {
	ThroughputBps   *float64 `json:"throughput_bps"`
	ThroughputRatio *float64 `json:"throughput_ratio"`
	BaselineBps     *float64 `json:"baseline_bps"`
	TS              string   `json:"ts"`
	Timestamp       string   `json:"timestamp"`
}

type replayTickEvent struct {
	Event             string  `json:"event"`
	Timestamp         string  `json:"timestamp"`
	Action            string  `json:"action"`
	ActionReason      string  `json:"action_reason"`
	Concurrency       int     `json:"concurrency"`
	TargetConcurrency int     `json:"target_concurrency"`
	TempC             int     `json:"temp_c"`
	TempValid         bool    `json:"temp_valid"`
	ThroughputBps     float64 `json:"throughput_bps"`
	BaselineBps       float64 `json:"baseline_bps"`
	ThroughputRatio   float64 `json:"throughput_ratio"`
}

func Replay(cfg ReplayConfig) (ReplayResult, error) {
	if cfg.TelemetryLogPath == "" {
		return ReplayResult{}, fmt.Errorf("telemetry log path is required")
	}
	if cfg.MaxConcurrencyStep <= 0 {
		cfg.MaxConcurrencyStep = 1
	}
	// Keep explicit zero cooldown to allow deterministic immediate transitions for tests and what-if runs.
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = defaultReplayPollInterval
	}
	if cfg.ThroughputWindow <= 0 {
		cfg.ThroughputWindow = 30 * time.Second
	}
	if cfg.BaselineWindow <= 0 {
		cfg.BaselineWindow = 120 * time.Second
	}

	telemetrySamples, err := LoadTelemetryLog(cfg.TelemetryLogPath)
	if err != nil {
		return ReplayResult{}, err
	}
	if len(telemetrySamples) == 0 {
		return ReplayResult{}, fmt.Errorf("no usable telemetry samples in %s", cfg.TelemetryLogPath)
	}

	controlThroughput, err := LoadThroughputFromControlLog(cfg.ControlLogPath, cfg.InitialBaselineThroughput)
	if err != nil {
		return ReplayResult{}, err
	}
	var telemetryClockOffset time.Duration
	if len(telemetrySamples) > 0 && !telemetrySamples[0].Timestamp.IsZero() {
		telemetryClockOffset = time.Now().Sub(telemetrySamples[0].Timestamp)
	}

	cfg.StartConcurrency = clampInt(cfg.StartConcurrency, cfg.MinConcurrency, cfg.MaxConcurrency)

	controller := control.NewRuleController(cfg.RuleCfg)
	estimator := control.NewStateEstimator()
	throughTracker := throughput.NewTracker(cfg.ThroughputWindow, cfg.BaselineWindow)
	if cfg.InitialBaselineThroughput > 0 {
		throughTracker.RestoreBaseline(cfg.InitialBaselineThroughput)
	}

	eventWriter, err := maybeOpenEventLog(cfg.EventLogPath)
	if err != nil {
		return ReplayResult{}, err
	}
	defer eventWriter.Close()

	startedAt := time.Now().UTC()
	result := ReplayResult{
		Ticks:            0,
		TelemetrySamples: len(telemetrySamples),
		StartConcurrency: cfg.StartConcurrency,
		ThroughputSamples: len(controlThroughput),
		StartedAt:        startedAt.Format(time.RFC3339),
		EventLogPath:     cfg.EventLogPath,
	}

	state := control.State{
		CurrentConcurrency: cfg.StartConcurrency,
		MinConcurrency:     cfg.MinConcurrency,
		MaxConcurrency:     cfg.MaxConcurrency,
		BaselineThroughput: cfg.InitialBaselineThroughput,
	}

	currentConcurrency := cfg.StartConcurrency
	var lastAction control.Action
	lastAction.Type = control.ActionHold
	var lastActionAt time.Time
	var telemetryWindow []telemetry.TelemetrySample

	var cumulativeOutput uint64
	var lastSample time.Time
	var syntheticBase time.Time
	rateFallback := cfg.InitialBaselineThroughput
	throughputCursor := -1

	for i, sample := range telemetrySamples {
		if cfg.MaxTicks > 0 && i >= cfg.MaxTicks {
			result.FinalReason = "max_ticks_reached"
			break
		}

		now := sample.Timestamp
		if now.IsZero() {
			if syntheticBase.IsZero() {
				syntheticBase = startedAt
			}
			now = syntheticBase.Add(time.Duration(i) * cfg.PollInterval)
		}

		decisionSample := sample
		if sample.Timestamp.IsZero() {
			decisionSample.Timestamp = now
		} else {
			decisionSample.Timestamp = sample.Timestamp.Add(telemetryClockOffset)
		}
		telemetryWindow = appendTelemetryWindow(telemetryWindow, decisionSample)
		state.CurrentConcurrency = currentConcurrency
		state.LastActionAt = lastActionAt

		rate := resolveThroughputRate(now, controlThroughput, rateFallback, &throughputCursor)
		if rate > 0 {
			rateFallback = rate
		}

		var tpSample throughput.Sample
		if lastSample.IsZero() {
			tpSample = throughTracker.Add(0, now)
		} else {
			delta := now.Sub(lastSample).Seconds()
			if delta > 0 {
				increment := uint64(math.Round(math.Max(0, rate*delta)))
				cumulativeOutput += increment
			}
			tpSample = throughTracker.Add(cumulativeOutput, now)
		}
		lastSample = now

		state.BaselineThroughput = throughTracker.Baseline()
		if state.BaselineThroughput == 0 && cfg.InitialBaselineThroughput > 0 {
			state.BaselineThroughput = cfg.InitialBaselineThroughput
		}
		decisionThroughput := shiftThroughputSamples(throughTracker.Samples(), telemetryClockOffset)
		state.Estimate = estimator.Estimate(telemetryWindow, decisionThroughput)

		action := controller.Decide(telemetryWindow, decisionThroughput, state)
		cooldown := effectiveCooldown(cfg.AdjustmentCooldown, action)
		if isDirectionalAction(action.Type) && isOppositeDirection(lastAction.Type, action.Type) {
			cooldown *= 2
		}
		if action.Type != control.ActionHold && !lastActionAt.IsZero() && now.Sub(lastActionAt) < cooldown {
			action = control.Action{
				Type:        control.ActionHold,
				Concurrency: currentConcurrency,
				Reason:      "cooldown",
			}
		}

		throughputRatio := 0.0
		if state.BaselineThroughput > 0 {
			throughputRatio = tpSample.Throughput / state.BaselineThroughput
		}

		logEvent := replayTickEvent{
			Event:             "replay_tick",
			Timestamp:         now.Format(time.RFC3339),
			Action:            string(action.Type),
			ActionReason:      action.Reason,
			Concurrency:       currentConcurrency,
			TargetConcurrency: action.Concurrency,
			TempC:             sample.TempC,
			TempValid:         sample.TempValid,
			ThroughputBps:     tpSample.Throughput,
			BaselineBps:       state.BaselineThroughput,
			ThroughputRatio:   throughputRatio,
		}
		if eventWriter != nil {
			if err := writeEvent(eventWriter, logEvent); err != nil {
				return ReplayResult{}, err
			}
		}
		result.DecisionSamples++

		result.Ticks++

		switch action.Type {
		case control.ActionHold:
			continue
		case control.ActionPause:
			result.FinalAction = string(control.ActionPause)
			result.FinalReason = action.Reason
			result.FinalConcurrency = currentConcurrency
			result.CompletedAt = now.Format(time.RFC3339)
			return result, nil
		}

		target := boundedStepDelta(
			currentConcurrency,
			action.Concurrency,
			cfg.MinConcurrency,
			cfg.MaxConcurrency,
			cfg.MaxConcurrencyStep,
		)
		if target == currentConcurrency {
			action = control.Action{
				Type:        control.ActionHold,
				Concurrency: currentConcurrency,
				Reason:      "bounded by step/min-max",
			}
			logEvent.Action = string(action.Type)
			logEvent.ActionReason = action.Reason
			if eventWriter != nil {
				if err := writeEvent(eventWriter, logEvent); err != nil {
					return ReplayResult{}, err
				}
			}
			continue
		}

		currentConcurrency = target
		lastAction = action
		lastActionAt = now

		result.FinalAction = string(action.Type)
		result.FinalReason = action.Reason
		result.FinalConcurrency = currentConcurrency

		throughTracker.Reset()
		cumulativeOutput = 0
		if cfg.InitialBaselineThroughput > 0 {
			throughTracker.RestoreBaseline(cfg.InitialBaselineThroughput)
		}
		lastSample = now
		rateFallback = rate
	}

	if result.CompletedAt == "" {
		result.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if result.FinalReason == "" {
		result.FinalReason = "completed"
	}
	if result.FinalAction == "" {
		result.FinalAction = "hold"
	}
	if result.FinalConcurrency == 0 {
		result.FinalConcurrency = currentConcurrency
	}
	return result, nil
}

func shiftThroughputSamples(samples []throughput.Sample, offset time.Duration) []throughput.Sample {
	if len(samples) == 0 || offset == 0 {
		copied := make([]throughput.Sample, len(samples))
		copy(copied, samples)
		return copied
	}
	out := make([]throughput.Sample, len(samples))
	for i, sample := range samples {
		shifted := sample
		if !shifted.Timestamp.IsZero() {
			shifted.Timestamp = shifted.Timestamp.Add(offset)
		}
		out[i] = shifted
	}
	return out
}

func LoadTelemetryLog(path string) ([]telemetry.TelemetrySample, error) {
	in, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer in.Close()

	var samples []telemetry.TelemetrySample
	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		var sample telemetry.TelemetrySample
		if err := json.Unmarshal(scanner.Bytes(), &sample); err != nil {
			continue
		}
		samples = append(samples, sample)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return samples, nil
}

func LoadThroughputFromControlLog(path string, fallbackBaseline float64) ([]throughputSamplePoint, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}

	in, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer in.Close()

	var samples []throughputSamplePoint
	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		var e controlLogEvent
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}

		ts, ok := parseEventTime(e.TS, e.Timestamp)
		if !ok {
			continue
		}
		rate, ok := controlLogThroughput(e, fallbackBaseline)
		if !ok {
			continue
		}
		samples = append(samples, throughputSamplePoint{Timestamp: ts, Rate: rate})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(samples) == 0 {
		return nil, nil
	}

	sort.Slice(samples, func(i, j int) bool {
		return samples[i].Timestamp.Before(samples[j].Timestamp)
	})
	return samples, nil
}

func controlLogThroughput(e controlLogEvent, fallbackBaseline float64) (float64, bool) {
	if e.ThroughputBps != nil && *e.ThroughputBps >= 0 {
		return *e.ThroughputBps, true
	}
	if e.ThroughputRatio == nil {
		return 0, false
	}
	base := fallbackBaseline
	if e.BaselineBps != nil {
		base = *e.BaselineBps
	}
	if base <= 0 {
		return 0, false
	}
	return (*e.ThroughputRatio) * base, true
}

func resolveThroughputRate(
	now time.Time,
	points []throughputSamplePoint,
	fallback float64,
	cursor *int,
) float64 {
	for *cursor+1 < len(points) && !points[*cursor+1].Timestamp.After(now) {
		*cursor++
	}
	if len(points) == 0 || *cursor < 0 {
		return fallback
	}
	if points[*cursor].Timestamp.After(now) {
		return fallback
	}
	return points[*cursor].Rate
}

func parseEventTime(values ...string) (time.Time, bool) {
	for _, raw := range values {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
			if parsed, err := time.Parse(layout, raw); err == nil {
				return parsed, true
			}
		}
	}
	return time.Time{}, false
}

func maybeOpenEventLog(path string) (io.WriteCloser, error) {
	if strings.TrimSpace(path) == "" {
		return nopWriteCloser{}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return os.Create(path)
}

func writeEvent(w io.Writer, evt replayTickEvent) error {
	line, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	_, err = w.Write(append(line, '\n'))
	return err
}

func appendTelemetryWindow(samples []telemetry.TelemetrySample, sample telemetry.TelemetrySample) []telemetry.TelemetrySample {
	samples = append(samples, sample)
	if len(samples) <= maxTelemetryWindowSamples {
		return samples
	}
	return samples[len(samples)-maxTelemetryWindowSamples:]
}

func effectiveCooldown(base time.Duration, action control.Action) time.Duration {
	if action.CooldownSec <= 0 {
		return base
	}
	requested := time.Duration(action.CooldownSec * float64(time.Second))
	if requested > base {
		return requested
	}
	return base
}

func isDirectionalAction(t control.ActionType) bool {
	return t == control.ActionIncrease || t == control.ActionDecrease
}

func isOppositeDirection(prev, next control.ActionType) bool {
	return prev == control.ActionIncrease && next == control.ActionDecrease ||
		prev == control.ActionDecrease && next == control.ActionIncrease
}

func boundedStepDelta(current, target, min, max, step int) int {
	target = clampInt(target, min, max)
	if step <= 1 {
		step = 1
	}
	delta := target - current
	if delta > step {
		return current + step
	}
	if delta < -step {
		return current - step
	}
	return target
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriteCloser) Close() error                  { return nil }
