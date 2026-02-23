package calibration

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/elhamdev/gpu-guardian/internal/telemetry"
)

// WorkloadAdapter mirrors the required adapter surface for calibration experiments.
//
// It intentionally omits progress reporting and only depends on adapter behaviors
// used by the calibration flow.
type WorkloadAdapter interface {
	Start(ctx context.Context, cmd string, concurrency int) error
	Restart(ctx context.Context, concurrency int) error
	Stop() error
	GetThroughput() uint64
	IsRunning() bool
}

// TelemetrySource returns one telemetry sample for the current process context.
type TelemetrySource interface {
	Sample(ctx context.Context) telemetry.TelemetrySample
}

// Config describes one calibration run.
type Config struct {
	Command             string
	PollInterval        time.Duration
	MinConcurrency      int
	MaxConcurrency      int
	ConcurrencyStep     int
	StepDuration        time.Duration
	StepSamples         int
	WarmupSamples       int
	HardTempC           float64
	ThroughputDropRatio float64
}

// CalibrationPoint captures one concurrency probe result.
type CalibrationPoint struct {
	Concurrency           int     `json:"concurrency"`
	ThroughputUnitsPerSec float64 `json:"throughput_units_per_sec"`
	AvgTempC              float64 `json:"avg_temp_c"`
	MaxTempC              int     `json:"max_temp_c"`
	MaxTempValid          bool    `json:"max_temp_valid"`
	TempSampleCount       int     `json:"temp_sample_count"`
	AvgVramUsedMB         float64 `json:"avg_vram_used_mb"`
	VramUsedSampleCount   int     `json:"vram_used_sample_count"`
}

// Profile contains the calibration output used by later profile persistence work.
type Profile struct {
	Command                string             `json:"command"`
	WorkloadType           string             `json:"workload_type"`
	GPUUUID                string             `json:"gpu_uuid"`
	MeasuredAt             time.Time          `json:"measured_at"`
	BaselineConcurrency    int                `json:"baseline_concurrency"`
	BaselineThroughput     float64            `json:"baseline_throughput"`
	SafeConcurrencyCeiling int                `json:"safe_concurrency_ceiling"`
	ThroughputDropRatio    float64            `json:"throughput_drop_ratio"`
	ThermalSaturationCurve []CalibrationPoint `json:"thermal_saturation_curve"`
	VramPerLoadUnitMB      float64            `json:"vram_per_load_unit_mb"`
}

// Run executes a short concurrency sweep and returns a calibration profile.
//
// The caller can limit each step by setting StepSamples to a fixed value. Otherwise,
// StepDuration controls how long each concurrency level is measured.
func Run(ctx context.Context, cfg Config, adapter WorkloadAdapter, source TelemetrySource) (Profile, error) {
	cfg.normalize()
	if cfg.Command == "" {
		return Profile{}, fmt.Errorf("command is required")
	}
	if cfg.MinConcurrency > cfg.MaxConcurrency {
		return Profile{}, fmt.Errorf("min concurrency %d is greater than max concurrency %d", cfg.MinConcurrency, cfg.MaxConcurrency)
	}

	if err := adapter.Start(ctx, cfg.Command, cfg.MinConcurrency); err != nil {
		return Profile{}, err
	}
	defer func() {
		_ = adapter.Stop()
	}()

	points := []CalibrationPoint{}
	for conc := cfg.MinConcurrency; conc <= cfg.MaxConcurrency; conc += cfg.ConcurrencyStep {
		if conc != cfg.MinConcurrency {
			if err := adapter.Restart(ctx, conc); err != nil {
				return Profile{}, err
			}
		}
		point, err := sampleStep(ctx, adapter, source, cfg, conc)
		if err != nil {
			return Profile{}, err
		}
		points = append(points, point)
	}
	if len(points) == 0 {
		return Profile{}, fmt.Errorf("no calibration points collected")
	}

	sort.Slice(points, func(i, j int) bool {
		return points[i].Concurrency < points[j].Concurrency
	})

	baselineThroughput := 0.0
	baselineConc := cfg.MinConcurrency
	for _, point := range points {
		if point.ThroughputUnitsPerSec <= 0 {
			continue
		}
		baselineThroughput = point.ThroughputUnitsPerSec
		baselineConc = point.Concurrency
		break
	}
	if baselineThroughput <= 0 {
		return Profile{}, fmt.Errorf("calibration baseline throughput is zero")
	}

	safeCeiling := computeSafeConcurrency(points, baselineThroughput, cfg.HardTempC, cfg.ThroughputDropRatio)
	vramPerLoad := estimateVramPerLoad(points)

	return Profile{
		Command:                cfg.Command,
		MeasuredAt:             time.Now(),
		BaselineConcurrency:    baselineConc,
		BaselineThroughput:     baselineThroughput,
		SafeConcurrencyCeiling: safeCeiling,
		ThroughputDropRatio:    cfg.ThroughputDropRatio,
		ThermalSaturationCurve: points,
		VramPerLoadUnitMB:      vramPerLoad,
	}, nil
}

func sampleStep(ctx context.Context, adapter WorkloadAdapter, source TelemetrySource, cfg Config, concurrency int) (CalibrationPoint, error) {
	if !adapter.IsRunning() {
		return CalibrationPoint{}, fmt.Errorf("adapter not running for concurrency %d", concurrency)
	}

	samplesToCollect := cfg.StepSamples
	if samplesToCollect <= 0 {
		samplesToCollect = int(math.Ceil(cfg.StepDuration.Seconds() / math.Max(cfg.PollInterval.Seconds(), 0.001)))
		if samplesToCollect <= 0 {
			samplesToCollect = 1
		}
	}

	var (
		throughputTotal  float64
		throughputCount  int
		throughputPrev   uint64
		throughputInit   bool
		throughputPrevAt time.Time

		tempSum      float64
		tempCount    int
		maxTemp      int
		maxTempValid bool

		avgVramSum   float64
		avgVramCount int
	)

	for i := 0; i < samplesToCollect; i++ {
		if !adapter.IsRunning() {
			return CalibrationPoint{}, fmt.Errorf("adapter stopped during calibration step at concurrency %d", concurrency)
		}
		s := source.Sample(ctx)

		now := time.Now()
		totalOutput := adapter.GetThroughput()
		if throughputInit {
			deltaOutput := totalOutput - throughputPrev
			deltaTime := now.Sub(throughputPrevAt).Seconds()
			if deltaTime > 0 && deltaOutput > 0 {
				tp := float64(deltaOutput) / deltaTime
				if i >= cfg.WarmupSamples {
					throughputTotal += tp
					throughputCount++
				}
			}
		} else {
			throughputInit = true
		}
		throughputPrev = totalOutput
		throughputPrevAt = now

		if s.TempValid {
			tempSum += float64(s.TempC)
			tempCount++
			if !maxTempValid || s.TempC > maxTemp {
				maxTemp = s.TempC
				maxTempValid = true
			}
		}

		if s.VramUsedValid {
			avgVramSum += float64(s.VramUsedMB)
			avgVramCount++
		}

		if ctx.Err() != nil {
			return CalibrationPoint{}, ctx.Err()
		}

		if i+1 < samplesToCollect {
			select {
			case <-time.After(cfg.PollInterval):
			case <-ctx.Done():
				return CalibrationPoint{}, ctx.Err()
			}
		}
	}

	throughput := 0.0
	if throughputCount > 0 {
		throughput = throughputTotal / float64(throughputCount)
	}

	point := CalibrationPoint{
		Concurrency:           concurrency,
		ThroughputUnitsPerSec: throughput,
		TempSampleCount:       tempCount,
		MaxTempValid:          maxTempValid,
		MaxTempC:              maxTemp,
	}
	if tempCount > 0 {
		point.AvgTempC = tempSum / float64(tempCount)
	}
	if avgVramCount > 0 {
		point.AvgVramUsedMB = avgVramSum / float64(avgVramCount)
		point.VramUsedSampleCount = avgVramCount
	}

	return point, nil
}

func computeSafeConcurrency(points []CalibrationPoint, baseline float64, hardTemp float64, throughputDropRatio float64) int {
	if len(points) == 0 || baseline <= 0 {
		return 0
	}
	safe := points[0].Concurrency

	for _, point := range points {
		throughputSafe := baseline > 0 && point.ThroughputUnitsPerSec >= baseline*throughputDropRatio
		tempSafe := true
		if point.MaxTempValid && hardTemp > 0 {
			tempSafe = float64(point.MaxTempC) <= hardTemp
		}
		if throughputSafe && tempSafe {
			safe = point.Concurrency
		}
	}

	return safe
}

func estimateVramPerLoad(points []CalibrationPoint) float64 {
	type vramPoint struct {
		conc int
		vram float64
	}

	prior := vramPoint{}
	hasPrior := false
	var sum float64
	var count int

	for _, point := range points {
		if point.VramUsedSampleCount == 0 {
			continue
		}
		if !hasPrior {
			prior = vramPoint{conc: point.Concurrency, vram: point.AvgVramUsedMB}
			hasPrior = true
			continue
		}

		deltaConc := float64(point.Concurrency - prior.conc)
		if deltaConc <= 0 {
			prior = vramPoint{conc: point.Concurrency, vram: point.AvgVramUsedMB}
			continue
		}
		deltaVram := point.AvgVramUsedMB - prior.vram
		if deltaVram >= 0 {
			sum += deltaVram / deltaConc
			count++
		}
		prior = vramPoint{conc: point.Concurrency, vram: point.AvgVramUsedMB}
	}

	if count == 0 {
		return 0
	}
	return sum / float64(count)
}

func (cfg *Config) normalize() {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 2 * time.Second
	}
	if cfg.MinConcurrency <= 0 {
		cfg.MinConcurrency = 1
	}
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = cfg.MinConcurrency
	}
	if cfg.ConcurrencyStep <= 0 {
		cfg.ConcurrencyStep = 1
	}
	if cfg.StepDuration <= 0 {
		cfg.StepDuration = 8 * time.Second
	}
	if cfg.StepSamples < 0 {
		cfg.StepSamples = 0
	}
	if cfg.WarmupSamples < 0 {
		cfg.WarmupSamples = 0
	}
	if cfg.HardTempC <= 0 {
		cfg.HardTempC = 84
	}
	if cfg.ThroughputDropRatio <= 0 {
		cfg.ThroughputDropRatio = 0.7
	}
	if cfg.ThroughputDropRatio > 1 {
		cfg.ThroughputDropRatio = 1
	}
}
