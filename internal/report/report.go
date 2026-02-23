package report

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/elhamdev/gpu-guardian/internal/telemetry"
)

const defaultThroughputFloorRatio = 0.7

// RecoveryMetrics summarizes control decisions and recovery behavior.
type RecoveryMetrics struct {
	DecisionSamples   int     `json:"decision_samples"`
	HoldActions       int     `json:"hold_actions"`
	IncreaseActions   int     `json:"increase_actions"`
	DecreaseActions   int     `json:"decrease_actions"`
	PauseActions      int     `json:"pause_actions"`
	MaxDecreaseStreak int     `json:"max_decrease_streak"`
	RecoveryTimeSec   float64 `json:"time_in_recovery_sec"`
	FinalAction       string  `json:"final_action"`
	FinalConcurrency  int     `json:"final_concurrency"`
}

// ThermalProfile summarizes observed temperature range and mean.
type ThermalProfile struct {
	SampleCount int     `json:"sample_count"`
	MinTempC    int     `json:"min_temp_c"`
	MaxTempC    int     `json:"max_temp_c"`
	AvgTempC    float64 `json:"avg_temp_c"`
	sumC        float64 `json:"-"`
}

// SessionReport is a compact summary generated from persisted logs.
type SessionReport struct {
	GeneratedAt          string          `json:"generated_at"`
	ControlLogPath       string          `json:"control_log"`
	TelemetryLogPath     string          `json:"telemetry_log"`
	ThroughputFloorRatio float64         `json:"throughput_floor_ratio"`
	EngineTickSamples    int             `json:"engine_tick_samples"`
	TelemetrySamples     int             `json:"telemetry_samples"`
	WorstSlowdown        float64         `json:"worst_slowdown"`
	TimeBelowFloorSec    float64         `json:"time_below_throughput_floor_sec"`
	Thermal              ThermalProfile  `json:"thermal_profile"`
	Recovery             RecoveryMetrics `json:"recovery_metrics"`
}

type controlEvent struct {
	Event                string  `json:"event"`
	ThroughputRatio      float64 `json:"throughput_ratio"`
	ThroughputRatioValid bool    `json:"throughput_ratio_valid"`
	ThroughputBps        float64 `json:"throughput_bps"`
	BaselineBps          float64 `json:"baseline_bps"`
	TempC                int     `json:"temp_c"`
	TempValid            bool    `json:"temp_valid"`
	Action               string  `json:"action"`
	Concurrency          int     `json:"concurrency"`
	TargetConcurrency    int     `json:"target_concurrency"`
	TS                   string  `json:"ts"`
	Timestamp            string  `json:"timestamp"`
}

// Generate reads structured control and telemetry logs and creates a session report.
func Generate(controlLogPath, telemetryLogPath string, throughputFloorRatio float64) (SessionReport, error) {
	if throughputFloorRatio <= 0 {
		throughputFloorRatio = defaultThroughputFloorRatio
	}

	rep := SessionReport{
		GeneratedAt:          time.Now().UTC().Format(time.RFC3339),
		ControlLogPath:       controlLogPath,
		TelemetryLogPath:     telemetryLogPath,
		ThroughputFloorRatio: throughputFloorRatio,
	}

	if controlLogPath == "" && telemetryLogPath == "" {
		return SessionReport{}, fmt.Errorf("no logs provided")
	}

	if controlLogPath != "" {
		ticks, err := readControlLog(controlLogPath)
		if err != nil {
			return SessionReport{}, err
		}
		rep.EngineTickSamples = len(ticks)
		updateFromControlLog(&rep, ticks, throughputFloorRatio)
	}

	if telemetryLogPath != "" {
		if err := readTelemetryLog(telemetryLogPath, &rep); err != nil {
			return SessionReport{}, err
		}
	}

	if rep.EngineTickSamples == 0 && rep.TelemetrySamples == 0 {
		return SessionReport{}, fmt.Errorf("no usable samples in provided logs")
	}
	finalizeThermal(&rep.Thermal)
	return rep, nil
}

func readControlLog(path string) ([]controlEvent, error) {
	in, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open control log: %w", err)
	}
	defer in.Close()

	var out []controlEvent
	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		var entry controlEvent
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry.Event != "engine_tick" {
			continue
		}
		out = append(out, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func readTelemetryLog(path string, rep *SessionReport) error {
	in, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open telemetry log: %w", err)
	}
	defer in.Close()

	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		var sample telemetry.TelemetrySample
		if err := json.Unmarshal(scanner.Bytes(), &sample); err != nil {
			continue
		}
		rep.TelemetrySamples++
		if sample.TempValid {
			updateThermal(&rep.Thermal, sample.TempC, true)
		}
	}
	return scanner.Err()
}

func updateFromControlLog(rep *SessionReport, ticks []controlEvent, throughputFloorRatio float64) {
	var (
		prevTime       *time.Time
		prevBelowFloor bool
		decreaseStreak int
	)

	for _, tick := range ticks {
		rep.Recovery.DecisionSamples++
		rep.Recovery.FinalAction = strings.TrimSpace(tick.Action)
		if tick.TargetConcurrency > 0 {
			rep.Recovery.FinalConcurrency = tick.TargetConcurrency
		} else if tick.Concurrency > 0 {
			rep.Recovery.FinalConcurrency = tick.Concurrency
		}

		if tick.TempValid {
			updateThermal(&rep.Thermal, tick.TempC, true)
		}

		ratio, ratioValid := resolveThroughputRatio(tick)
		if ratioValid {
			slowdown := 1.0 - ratio
			if slowdown < 0 {
				slowdown = 0
			}
			if slowdown > rep.WorstSlowdown {
				rep.WorstSlowdown = slowdown
			}
		}

		action := strings.ToLower(strings.TrimSpace(tick.Action))
		switch action {
		case "hold":
			rep.Recovery.HoldActions++
			decreaseStreak = 0
		case "increase":
			rep.Recovery.IncreaseActions++
			decreaseStreak = 0
		case "decrease":
			rep.Recovery.DecreaseActions++
			decreaseStreak++
			if decreaseStreak > rep.Recovery.MaxDecreaseStreak {
				rep.Recovery.MaxDecreaseStreak = decreaseStreak
			}
		case "pause":
			rep.Recovery.PauseActions++
			decreaseStreak = 0
		default:
			decreaseStreak = 0
		}

		when, ok := parseEventTime(tick.TS, tick.Timestamp)
		if !ok {
			prevTime = nil
			prevBelowFloor = false
		} else if prevTime != nil {
			delta := when.Sub(*prevTime).Seconds()
			if delta > 0 && prevBelowFloor {
				rep.TimeBelowFloorSec += delta
			}
			if isRecoveryAction(action) && delta > 0 {
				rep.Recovery.RecoveryTimeSec += delta
			}
			prevTime = &when
		} else {
			prevTime = &when
		}

		if ratioValid {
			prevBelowFloor = ratio < throughputFloorRatio
		} else {
			prevBelowFloor = false
		}
	}
}

func finalizeThermal(profile *ThermalProfile) {
	if profile.SampleCount == 0 {
		return
	}
	profile.AvgTempC = profile.sumC / float64(profile.SampleCount)
}

func updateThermal(profile *ThermalProfile, tempC int, valid bool) {
	if !valid {
		return
	}
	if profile.SampleCount == 0 {
		profile.MinTempC = tempC
		profile.MaxTempC = tempC
	}
	profile.SampleCount++
	profile.sumC += float64(tempC)
	if tempC < profile.MinTempC {
		profile.MinTempC = tempC
	}
	if tempC > profile.MaxTempC {
		profile.MaxTempC = tempC
	}
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

func isRecoveryAction(action string) bool {
	return action == "decrease" || action == "pause"
}

func resolveThroughputRatio(e controlEvent) (float64, bool) {
	if e.ThroughputRatioValid {
		return e.ThroughputRatio, true
	}
	if e.BaselineBps > 0 && e.ThroughputBps >= 0 {
		return e.ThroughputBps / e.BaselineBps, true
	}
	return -1, false
}
