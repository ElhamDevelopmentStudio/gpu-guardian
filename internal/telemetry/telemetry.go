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
	Timestamp      time.Time `json:"timestamp"`
	TempC          int       `json:"temp_c"`
	TempValid      bool      `json:"temp_valid"`
	UtilPct        float64   `json:"util_pct"`
	UtilValid      bool      `json:"util_valid"`
	VramUsedMB     int       `json:"vram_used_mb"`
	VramUsedValid  bool      `json:"vram_used_valid"`
	VramTotalMB    int       `json:"vram_total_mb"`
	VramTotalValid bool      `json:"vram_total_valid"`
	Error          string    `json:"error,omitempty"`
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

func (c *Collector) Sample(ctx context.Context) TelemetrySample {
	s := TelemetrySample{Timestamp: time.Now()}
	cmd := exec.CommandContext(
		ctx,
		"nvidia-smi",
		"--query-gpu=temperature.gpu,utilization.gpu,memory.used,memory.total",
		"--format=csv,noheader,nounits",
	)

	out, err := cmd.Output()
	if err != nil {
		s.Error = fmt.Sprintf("nvidia-smi error: %v", err)
		return s
	}

	line := bytes.TrimSpace(out)
	if len(line) == 0 {
		s.Error = "nvidia-smi returned empty output"
		return s
	}

	rows := bytes.Split(line, []byte("\n"))
	parts := strings.Split(string(rows[0]), ",")
	if len(parts) < 4 {
		s.Error = "nvidia-smi output format mismatch"
		return s
	}

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

	return s
}
