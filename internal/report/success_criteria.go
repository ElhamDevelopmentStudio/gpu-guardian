package report

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	defaultSustainedSlowdownRatio = 0.2
	defaultSustainedSlowdownSec   = 30.0
	defaultMinFloorUptimeRatio    = 0.95
)

type SuccessCriteriaPolicy struct {
	ThroughputFloorRatio      float64
	MaxSustainedSlowdownRatio float64
	MaxSustainedSlowdownSec   float64
	MinRuntimeAboveFloorRatio float64
	ThermalCeilingC           int
	RequireFloorUptimeCheck   bool
	RequireSlowdownCheck      bool
	RequireThermalSafetyCheck bool
	CheckDaemonAPI            bool
	DaemonBaseURL             string
	DaemonAPIToken            string
	DaemonAPITimeout          time.Duration
}

type SuccessCriteriaCheck struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Details string `json:"details"`
}

type SuccessCriteriaResult struct {
	Passed                      bool                   `json:"passed"`
	Checks                      []SuccessCriteriaCheck `json:"checks"`
	RuntimeSec                  float64                `json:"runtime_sec"`
	TimeAboveThroughputFloorSec float64                `json:"time_above_throughput_floor_sec"`
	TimeBelowThroughputFloorSec float64                `json:"time_below_throughput_floor_sec"`
	RuntimeAboveFloorRatio      float64                `json:"runtime_above_floor_ratio"`
	WorstSlowdown               float64                `json:"worst_slowdown"`
	MaxSustainedSlowdownSec     float64                `json:"max_sustained_slowdown_sec"`
	MaxSustainedSlowdownRatio   float64                `json:"max_sustained_slowdown_ratio"`
	ThermalProfile              ThermalProfile         `json:"thermal_profile"`
	DaemonAPIPassed             bool                   `json:"daemon_api_passed"`
	DaemonAPIError              string                 `json:"daemon_api_error,omitempty"`
}

func EvaluateSuccessCriteria(controlLogPath, telemetryLogPath string, policy SuccessCriteriaPolicy) (SuccessCriteriaResult, error) {
	cfg, err := normalizeSuccessCriteriaPolicy(policy)
	if err != nil {
		return SuccessCriteriaResult{}, err
	}
	if controlLogPath == "" && telemetryLogPath == "" {
		return SuccessCriteriaResult{}, fmt.Errorf("no logs provided")
	}

	res := SuccessCriteriaResult{
		Checks: make([]SuccessCriteriaCheck, 0, 4),
	}

	var throughputStats throughputStats
	if controlLogPath != "" {
		ticks, err := readControlLog(controlLogPath)
		if err != nil {
			return SuccessCriteriaResult{}, err
		}
		throughputStats = summarizeThroughput(ticks, cfg.ThroughputFloorRatio, cfg.MaxSustainedSlowdownRatio)
		res.RuntimeSec = throughputStats.RuntimeSec
		res.TimeAboveThroughputFloorSec = throughputStats.TimeAboveFloorSec
		res.TimeBelowThroughputFloorSec = throughputStats.TimeBelowFloorSec
		res.RuntimeAboveFloorRatio = throughputStats.RuntimeAboveFloorRatio
		res.WorstSlowdown = throughputStats.WorstSlowdown
		res.MaxSustainedSlowdownSec = throughputStats.MaxSustainedSlowdownSec
		res.MaxSustainedSlowdownRatio = throughputStats.MinThroughputRatio
	} else {
		res.Checks = append(res.Checks, SuccessCriteriaCheck{
			Name:    "throughput_time",
			Passed:  false,
			Details: "missing control log prevents throughput criteria evaluation",
		})
	}

	profile, err := collectThermalProfile(controlLogPath, telemetryLogPath)
	if err != nil {
		return SuccessCriteriaResult{}, err
	}
	if profile.SampleCount > 0 {
		res.ThermalProfile = profile
	}

	// Throughput floor uptime.
	if cfg.RequireFloorUptimeCheck {
		check := evaluateFloorUptime(throughputStats, cfg.MinRuntimeAboveFloorRatio)
		res.Checks = append(res.Checks, check)
	}

	// Sustained slowdown check.
	if cfg.RequireSlowdownCheck {
		check := evaluateSustainedSlowdown(throughputStats, cfg.MaxSustainedSlowdownRatio, cfg.MaxSustainedSlowdownSec)
		res.Checks = append(res.Checks, check)
	}

	// Thermal ceiling check.
	if cfg.RequireThermalSafetyCheck {
		check := evaluateThermalSafety(profile, cfg.ThermalCeilingC)
		res.Checks = append(res.Checks, check)
	}

	// Daemon API stability check.
	if cfg.CheckDaemonAPI {
		check, _ := EvaluateStableDaemonAPI(cfg.DaemonBaseURL, cfg.DaemonAPIToken, cfg.DaemonAPITimeout)
		res.DaemonAPIPassed = check.Passed
		res.Checks = append(res.Checks, SuccessCriteriaCheck{
			Name:    "daemon_api_stability",
			Passed:  check.Passed,
			Details: check.Details,
		})
		if check.Err != "" {
			res.DaemonAPIError = check.Err
		}
	}

	res.Passed = true
	for _, check := range res.Checks {
		if !check.Passed {
			res.Passed = false
			break
		}
	}
	return res, nil
}

type daemonAPICheck struct {
	Passed  bool
	Details string
	Err     string
}

type throughputStats struct {
	RuntimeSec              float64
	TimeBelowFloorSec       float64
	TimeAboveFloorSec       float64
	RuntimeAboveFloorRatio  float64
	WorstSlowdown           float64
	MaxSustainedSlowdownSec float64
	MinThroughputRatio      float64
}

func evaluateFloorUptime(stats throughputStats, minRatio float64) SuccessCriteriaCheck {
	if stats.RuntimeSec <= 0 {
		return SuccessCriteriaCheck{
			Name:    "throughput_floor_uptime",
			Passed:  false,
			Details: "insufficient throughput timeline data",
		}
	}
	detail := fmt.Sprintf("runtime_above_floor_ratio=%.3f", stats.RuntimeAboveFloorRatio)
	if stats.RuntimeAboveFloorRatio >= minRatio {
		return SuccessCriteriaCheck{Name: "throughput_floor_uptime", Passed: true, Details: detail}
	}
	return SuccessCriteriaCheck{
		Name:    "throughput_floor_uptime",
		Passed:  false,
		Details: detail + " below minimum 95% uptime requirement",
	}
}

func evaluateSustainedSlowdown(stats throughputStats, maxSlowdownRatio, maxDuration float64) SuccessCriteriaCheck {
	if maxDuration < 0 {
		maxDuration = 0
	}
	if stats.RuntimeSec <= 0 {
		return SuccessCriteriaCheck{
			Name:    "throughput_slowdown",
			Passed:  false,
			Details: "insufficient throughput timeline data",
		}
	}
	detail := fmt.Sprintf(
		"max_sustained_slowdown_ratio=%.3f max_sustained_slowdown_sec=%.3f",
		stats.MinThroughputRatio,
		stats.MaxSustainedSlowdownSec,
	)
	if stats.MaxSustainedSlowdownSec > maxDuration && maxDuration > 0 {
		return SuccessCriteriaCheck{
			Name:    "throughput_slowdown",
			Passed:  false,
			Details: detail + " above sustained 4-5x slowdown threshold",
		}
	}
	if maxSlowdownRatio > 0 && stats.MinThroughputRatio > maxSlowdownRatio && stats.MaxSustainedSlowdownSec == 0 {
		detail = detail + ", never below configured slowdown ratio"
	}
	return SuccessCriteriaCheck{Name: "throughput_slowdown", Passed: true, Details: detail}
}

func evaluateThermalSafety(profile ThermalProfile, ceiling int) SuccessCriteriaCheck {
	if ceiling <= 0 {
		return SuccessCriteriaCheck{
			Name:    "thermal_ceiling",
			Passed:  true,
			Details: "thermal ceiling check disabled",
		}
	}
	if profile.SampleCount == 0 {
		return SuccessCriteriaCheck{
			Name:    "thermal_ceiling",
			Passed:  false,
			Details: "no thermal samples available",
		}
	}
	if profile.MaxTempC <= ceiling {
		return SuccessCriteriaCheck{
			Name:    "thermal_ceiling",
			Passed:  true,
			Details: fmt.Sprintf("max_temp_c=%d within ceiling %d", profile.MaxTempC, ceiling),
		}
	}
	return SuccessCriteriaCheck{
		Name:    "thermal_ceiling",
		Passed:  false,
		Details: fmt.Sprintf("max_temp_c=%d exceeds ceiling %d", profile.MaxTempC, ceiling),
	}
}

func summarizeThroughput(ticks []controlEvent, throughputFloorRatio float64, maxSlowdownRatio float64) throughputStats {
	var stats throughputStats
	stats.MinThroughputRatio = 1.0
	if len(ticks) == 0 {
		return stats
	}

	var (
		prevTime          *time.Time
		prevRatio         float64
		prevRatioValid    bool
		slowdownStreakSec float64
	)

	for _, tick := range ticks {
		ratio, ratioValid := resolveThroughputRatio(tick)
		if ratioValid {
			if ratio < stats.MinThroughputRatio {
				stats.MinThroughputRatio = ratio
			}
			slowdown := 1.0 - ratio
			if slowdown < 0 {
				slowdown = 0
			}
			if slowdown > stats.WorstSlowdown {
				stats.WorstSlowdown = slowdown
			}
		}

		when, ok := parseEventTime(tick.TS, tick.Timestamp)
		if !ok {
			prevTime = nil
			prevRatioValid = false
			continue
		}
		if prevTime != nil {
			delta := when.Sub(*prevTime).Seconds()
			if delta > 0 && prevRatioValid {
				stats.RuntimeSec += delta
				if prevRatio < throughputFloorRatio {
					stats.TimeBelowFloorSec += delta
				}
				if maxSlowdownRatio > 0 && prevRatio <= maxSlowdownRatio {
					slowdownStreakSec += delta
					if slowdownStreakSec > stats.MaxSustainedSlowdownSec {
						stats.MaxSustainedSlowdownSec = slowdownStreakSec
					}
				} else {
					slowdownStreakSec = 0
				}
			}
		}
		prevTime = &when
		prevRatio = ratio
		prevRatioValid = ratioValid
	}

	stats.TimeAboveFloorSec = stats.RuntimeSec - stats.TimeBelowFloorSec
	if stats.TimeAboveFloorSec < 0 {
		stats.TimeAboveFloorSec = 0
	}
	if stats.RuntimeSec > 0 {
		stats.RuntimeAboveFloorRatio = stats.TimeAboveFloorSec / stats.RuntimeSec
	}
	return stats
}

func collectThermalProfile(controlLogPath, telemetryLogPath string) (ThermalProfile, error) {
	var out ThermalProfile
	if controlLogPath != "" {
		ticks, err := readControlLog(controlLogPath)
		if err != nil {
			return ThermalProfile{}, err
		}
		for _, tick := range ticks {
			if tick.TempValid {
				updateThermal(&out, tick.TempC, true)
			}
		}
	}
	if telemetryLogPath != "" {
		tmp := SessionReport{}
		if err := readTelemetryLog(telemetryLogPath, &tmp); err != nil {
			return ThermalProfile{}, err
		}
		if tmp.Thermal.SampleCount > 0 {
			mergeThermalProfiles(&out, tmp.Thermal)
		}
	}
	return out, nil
}

func mergeThermalProfiles(base *ThermalProfile, add ThermalProfile) {
	if add.SampleCount == 0 {
		return
	}
	if base.SampleCount == 0 {
		*base = add
		return
	}
	base.SampleCount += add.SampleCount
	base.sumC += add.sumC
	if add.MinTempC < base.MinTempC {
		base.MinTempC = add.MinTempC
	}
	if add.MaxTempC > base.MaxTempC {
		base.MaxTempC = add.MaxTempC
	}
	base.AvgTempC = base.sumC / float64(base.SampleCount)
}

func normalizeSuccessCriteriaPolicy(policy SuccessCriteriaPolicy) (SuccessCriteriaPolicy, error) {
	if policy.ThroughputFloorRatio <= 0 {
		policy.ThroughputFloorRatio = 0.7
	}
	if policy.MaxSustainedSlowdownRatio <= 0 {
		policy.MaxSustainedSlowdownRatio = defaultSustainedSlowdownRatio
	}
	if policy.MinRuntimeAboveFloorRatio <= 0 {
		policy.MinRuntimeAboveFloorRatio = defaultMinFloorUptimeRatio
	}
	if policy.MaxSustainedSlowdownSec <= 0 {
		policy.MaxSustainedSlowdownSec = defaultSustainedSlowdownSec
	}
	if policy.DaemonAPITimeout <= 0 {
		policy.DaemonAPITimeout = 2 * time.Second
	}

	// By default evaluate all criteria unless explicitly disabled by caller.
	if !policy.RequireFloorUptimeCheck && !policy.RequireSlowdownCheck && !policy.RequireThermalSafetyCheck {
		policy.RequireFloorUptimeCheck = true
		policy.RequireSlowdownCheck = true
		policy.RequireThermalSafetyCheck = true
	}

	if policy.CheckDaemonAPI {
		base := strings.TrimSpace(policy.DaemonBaseURL)
		if base == "" {
			return SuccessCriteriaPolicy{}, fmt.Errorf("daemon base URL is required when --check-daemon-api is set")
		}
		policy.DaemonBaseURL = base
	}
	return policy, nil
}

func EvaluateStableDaemonAPI(baseURL, apiToken string, timeout time.Duration) (daemonAPICheck, error) {
	client := &http.Client{Timeout: timeout}
	base := strings.TrimRight(baseURL, "/")

	healthURL := base + "/health"
	healthRaw, err := doJSONRequest(client, healthURL, apiToken)
	if err != nil {
		return daemonAPICheck{Passed: false, Err: err.Error()}, nil
	}
	healthResp, ok := healthRaw.(map[string]interface{})
	if !ok {
		return daemonAPICheck{
			Passed:  false,
			Details: "health endpoint returned non-object payload",
		}, nil
	}
	status, _ := healthResp["status"].(string)
	version, _ := healthResp["version"].(string)
	if status != "ok" {
		return daemonAPICheck{Passed: false, Details: "health endpoint status not ok", Err: fmt.Sprintf("status=%q", status)}, nil
	}
	if version == "" {
		return daemonAPICheck{Passed: false, Details: "health endpoint missing version", Err: "missing version"}, nil
	}

	metricsRaw, err := doJSONRequest(client, base+"/metrics", apiToken)
	if err != nil {
		return daemonAPICheck{Passed: false, Err: err.Error()}, nil
	}
	metricsResp, ok := metricsRaw.(map[string]interface{})
	if !ok {
		return daemonAPICheck{Passed: false, Details: "metrics endpoint returned non-object payload"}, nil
	}
	if _, ok := metricsResp["sessions_total"]; !ok {
		return daemonAPICheck{Passed: false, Details: "metrics response missing sessions_total"}, nil
	}

	sessionsResp, err := doJSONRequest(client, base+"/sessions", apiToken)
	if err != nil {
		return daemonAPICheck{Passed: false, Err: err.Error()}, nil
	}
	switch sessionsResp.(type) {
	case []interface{}:
	default:
		return daemonAPICheck{Passed: false, Details: "sessions response missing array payload"}, nil
	}
	return daemonAPICheck{
		Passed:  true,
		Details: "daemon API responses are stable",
	}, nil
}

func doJSONRequest(client *http.Client, url string, token string) (interface{}, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("request failed: %s", resp.Status)
	}
	var payload interface{}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload, nil
}
