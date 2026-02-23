package telemetry

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type TelemetrySample struct {
	Timestamp           time.Time `json:"timestamp"`
	TempC               int       `json:"temp_c"`
	TempValid           bool      `json:"temp_valid"`
	UtilPct             float64   `json:"util_pct"`
	UtilValid           bool      `json:"util_valid"`
	VramUsedMB          int       `json:"vram_used_mb"`
	VramUsedValid       bool      `json:"vram_used_valid"`
	VramTotalMB         int       `json:"vram_total_mb"`
	VramTotalValid      bool      `json:"vram_total_valid"`
	PowerDrawW          float64   `json:"power_draw_w"`
	PowerDrawValid      bool      `json:"power_draw_valid"`
	PowerLimitW         float64   `json:"power_limit_w"`
	PowerLimitValid     bool      `json:"power_limit_valid"`
	ClockSmMHz          float64   `json:"clock_sm_mhz"`
	ClockSmValid        bool      `json:"clock_sm_valid"`
	ClockMemMHz         float64   `json:"clock_mem_mhz"`
	ClockMemValid       bool      `json:"clock_mem_valid"`
	MemoryPressure      float64   `json:"memory_pressure"`
	MemoryPressureValid bool      `json:"memory_pressure_valid"`
	ThrottleRisk        float64   `json:"throttle_risk"`
	ThrottleRiskValid   bool      `json:"throttle_risk_valid"`
	Error               string    `json:"error,omitempty"`
}

type Collector struct{}

func NewCollector() *Collector {
	return &Collector{}
}

func parseFloatField(v string) (float64, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, fmt.Errorf("empty field")
	}
	return strconv.ParseFloat(v, 64)
}

func parseIntField(v string) (int, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, fmt.Errorf("empty field")
	}
	return strconv.Atoi(v)
}

const nvidiaQueryFields = "temperature.gpu,utilization.gpu,memory.used,memory.total,power.draw,power.limit,clocks.current.sm,clocks.current.memory"
const nvidiaQueryFieldsFallback = "temperature.gpu,utilization.gpu,memory.used,memory.total"

func runNvidiaSMI(ctx context.Context, query string) ([]byte, error) {
	cmd := exec.CommandContext(
		ctx,
		"nvidia-smi",
		"--query-gpu="+query,
		"--format=csv,noheader,nounits",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return out, nil
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func (c *Collector) Sample(ctx context.Context) TelemetrySample {
	s := TelemetrySample{Timestamp: time.Now()}
	out, err := runNvidiaSMI(ctx, nvidiaQueryFields)
	if err != nil {
		primaryErr := err
		out, err = runNvidiaSMI(ctx, nvidiaQueryFieldsFallback)
		if err != nil {
			s.Error = fmt.Sprintf("nvidia-smi error: %v; fallback error: %v", primaryErr, err)
			return s
		}
		s.Error = "telemetry query fallback: extended fields unavailable"
	}

	line := bytes.TrimSpace(out)
	if len(line) == 0 {
		s.Error = "nvidia-smi returned empty output"
		return s
	}

	rows := bytes.Split(line, []byte("\n"))
	parts := strings.Split(string(rows[0]), ",")
	if len(parts) < 4 {
		s.Error = fmt.Sprintf("%s; nvidia-smi output format mismatch", s.Error)
		return s
	}

	parseCoreFields := func() {
		if val, err := parseIntField(parts[0]); err == nil {
			s.TempC = val
			s.TempValid = true
		} else {
			s.Error = fmt.Sprintf("%s; temp parse failed: %v", s.Error, err)
		}

		if val, err := parseFloatField(parts[1]); err == nil {
			s.UtilPct = val
			s.UtilValid = true
		} else {
			s.Error = fmt.Sprintf("%s; util parse failed: %v", s.Error, err)
		}

		if val, err := parseIntField(parts[2]); err == nil {
			s.VramUsedMB = val
			s.VramUsedValid = true
		} else {
			s.Error = fmt.Sprintf("%s; memory.used parse failed: %v", s.Error, err)
		}

		if val, err := parseIntField(parts[3]); err == nil {
			s.VramTotalMB = val
			s.VramTotalValid = true
		} else {
			s.Error = fmt.Sprintf("%s; memory.total parse failed: %v", s.Error, err)
		}
	}

	parseCoreFields()
	if len(parts) < 8 {
		setDerivedMetrics(&s)
		return s
	}

	if val, err := parseFloatField(parts[4]); err == nil {
		s.PowerDrawW = val
		s.PowerDrawValid = true
	} else {
		s.Error = fmt.Sprintf("%s; power.draw parse failed: %v", s.Error, err)
	}

	if val, err := parseFloatField(parts[5]); err == nil {
		s.PowerLimitW = val
		s.PowerLimitValid = true
	} else {
		s.Error = fmt.Sprintf("%s; power.limit parse failed: %v", s.Error, err)
	}

	if val, err := parseFloatField(parts[6]); err == nil {
		s.ClockSmMHz = val
		s.ClockSmValid = true
	} else {
		s.Error = fmt.Sprintf("%s; clocks.current.sm parse failed: %v", s.Error, err)
	}

	if val, err := parseFloatField(parts[7]); err == nil {
		s.ClockMemMHz = val
		s.ClockMemValid = true
	} else {
		s.Error = fmt.Sprintf("%s; clocks.current.memory parse failed: %v", s.Error, err)
	}

	setDerivedMetrics(&s)
	return s
}

func setDerivedMetrics(s *TelemetrySample) {
	if s.VramUsedValid && s.VramTotalValid && s.VramTotalMB > 0 {
		s.MemoryPressure = float64(s.VramUsedMB) / float64(s.VramTotalMB)
		s.MemoryPressureValid = true
	}

	if s.PowerDrawValid && s.PowerLimitValid && s.PowerLimitW > 0 {
		s.ThrottleRisk = clamp01(s.PowerDrawW / s.PowerLimitW)
		s.ThrottleRiskValid = true
	}
}
