package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/elhamdev/gpu-guardian/internal/adapter"
	"github.com/elhamdev/gpu-guardian/internal/calibration"
	"github.com/elhamdev/gpu-guardian/internal/control"
	"github.com/elhamdev/gpu-guardian/internal/daemon"
	"github.com/elhamdev/gpu-guardian/internal/engine"
	"github.com/elhamdev/gpu-guardian/internal/logger"
	"github.com/elhamdev/gpu-guardian/internal/report"
	"github.com/elhamdev/gpu-guardian/internal/simulation"
	"github.com/elhamdev/gpu-guardian/internal/telemetry"
	"github.com/elhamdev/gpu-guardian/internal/throughput"
)

const mvpConfigPath = ".guardian-mvp.json"
const defaultProfilePath = calibration.DefaultProfileStorePath
const defaultWorkloadType = calibration.DefaultProfileWorkloadType

var daemonBaseURL = "http://" + daemon.DefaultListenAddress

type Config struct {
	Command                          string  `json:"command"`
	PollIntervalSec                  int     `json:"poll_interval_sec"`
	SoftTemp                         float64 `json:"soft_temp"`
	HardTemp                         float64 `json:"hard_temp"`
	MinConcurrency                   int     `json:"min_concurrency"`
	MaxConcurrency                   int     `json:"max_concurrency"`
	StartConcurrency                 int     `json:"start_concurrency"`
	ThroughputFloorRatio             float64 `json:"throughput_floor_ratio"`
	ThroughputSlowdownFloorRatio     float64 `json:"throughput_slowdown_floor_ratio"`
	AdjustmentCooldownSec            int     `json:"adjustment_cooldown_sec"`
	TempHysteresisC                  float64 `json:"temp_hysteresis_c"`
	ThroughputRecoveryMargin         float64 `json:"throughput_recovery_margin"`
	ThroughputRecoveryMaxAttempts    int     `json:"throughput_recovery_max_attempts"`
	ThroughputRecoveryStepMultiplier int     `json:"throughput_recovery_step_multiplier"`
	MemoryPressureLimit              float64 `json:"memory_pressure_limit"`
	ThrottleRiskLimit                float64 `json:"throttle_risk_limit"`
	TelemetryLogPath                 string  `json:"telemetry_log_path"`
	BaselineWindowSec                int     `json:"baseline_window_sec"`
	ThroughputWindowSec              int     `json:"throughput_window_sec"`
	ThroughputFloorWindowSec         int     `json:"throughput_floor_window_sec"`
	AdapterStopTimeoutSec            int     `json:"adapter_stop_timeout_sec"`
	MaxConcurrencyStep               int     `json:"max_concurrency_step"`
	LogPath                          string  `json:"log_file"`
	LogMaxSizeMB                     int     `json:"log_max_size_mb"`
	WorkloadLogPath                  string  `json:"workload_log_path"`
	EchoWorkloadOutput               bool    `json:"echo_workload_output"`
	InitialBaselineThroughput        float64 `json:"-"`
	MaxTicks                         int     `json:"-"`
	ProfilePath                      string  `json:"-"`
	WorkloadType                     string  `json:"-"`
}

func defaultConfig() Config {
	return Config{
		PollIntervalSec:                  2,
		SoftTemp:                         78,
		HardTemp:                         84,
		MinConcurrency:                   1,
		MaxConcurrency:                   8,
		StartConcurrency:                 4,
		ThroughputFloorRatio:             0.7,
		ThroughputSlowdownFloorRatio:     0.5,
		AdjustmentCooldownSec:            10,
		TempHysteresisC:                  2,
		ThroughputRecoveryMargin:         0.05,
		ThroughputRecoveryMaxAttempts:    3,
		ThroughputRecoveryStepMultiplier: 2,
		MemoryPressureLimit:              0.9,
		ThrottleRiskLimit:                0.85,
		TelemetryLogPath:                 "telemetry.log",
		BaselineWindowSec:                120,
		ThroughputWindowSec:              30,
		ThroughputFloorWindowSec:         30,
		AdapterStopTimeoutSec:            5,
		MaxConcurrencyStep:               1,
		LogPath:                          "guardian.log",
		LogMaxSizeMB:                     50,
		EchoWorkloadOutput:               false,
	}
}

func loadConfigFile(path string) (Config, error) {
	cfg := defaultConfig()
	if path == "" || path == mvpConfigPath {
		if b, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(b, &cfg); err != nil {
				return Config{}, err
			}
		} else if !os.IsNotExist(err) {
			return Config{}, err
		}
		return cfg, nil
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func detectConfigPath(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--config" && i+1 < len(args):
			return args[i+1]
		case strings.HasPrefix(arg, "--config="):
			parts := strings.SplitN(arg, "=", 2)
			if len(parts) == 2 {
				return parts[1]
			}
		}
	}
	return mvpConfigPath
}

func normalizeConfig(cfg *Config) error {
	if cfg.Command == "" {
		return fmt.Errorf("command is required; use --cmd")
	}
	if cfg.PollIntervalSec <= 0 {
		cfg.PollIntervalSec = 2
	}
	if cfg.SoftTemp <= 0 {
		cfg.SoftTemp = 78
	}
	if cfg.HardTemp <= 0 {
		cfg.HardTemp = 84
	}
	if cfg.MinConcurrency <= 0 {
		cfg.MinConcurrency = 1
	}
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = cfg.MinConcurrency
	}
	if cfg.MinConcurrency > cfg.MaxConcurrency {
		cfg.MinConcurrency = cfg.MaxConcurrency
	}
	if cfg.StartConcurrency < cfg.MinConcurrency {
		cfg.StartConcurrency = cfg.MinConcurrency
	}
	if cfg.StartConcurrency > cfg.MaxConcurrency {
		cfg.StartConcurrency = cfg.MaxConcurrency
	}
	if cfg.ThroughputFloorRatio <= 0 {
		cfg.ThroughputFloorRatio = 0.7
	}
	if cfg.ThroughputSlowdownFloorRatio <= 0 || cfg.ThroughputSlowdownFloorRatio > cfg.ThroughputFloorRatio {
		cfg.ThroughputSlowdownFloorRatio = 0.5
	}
	if cfg.TempHysteresisC < 0 {
		cfg.TempHysteresisC = 2
	}
	if cfg.ThroughputRecoveryMargin < 0 {
		cfg.ThroughputRecoveryMargin = 0.05
	}
	if cfg.ThroughputRecoveryMargin == 0 {
		cfg.ThroughputRecoveryMargin = 0.05
	}
	if cfg.ThroughputRecoveryMaxAttempts <= 0 {
		cfg.ThroughputRecoveryMaxAttempts = 3
	}
	if cfg.ThroughputRecoveryStepMultiplier <= 1 {
		cfg.ThroughputRecoveryStepMultiplier = 2
	}
	if cfg.MemoryPressureLimit <= 0 {
		cfg.MemoryPressureLimit = 0.9
	}
	if cfg.ThrottleRiskLimit <= 0 {
		cfg.ThrottleRiskLimit = 0.85
	}
	if cfg.TelemetryLogPath == "" {
		cfg.TelemetryLogPath = "telemetry.log"
	}
	if cfg.AdjustmentCooldownSec <= 0 {
		cfg.AdjustmentCooldownSec = 10
	}
	if cfg.BaselineWindowSec <= 0 {
		cfg.BaselineWindowSec = 120
	}
	if cfg.ThroughputWindowSec <= 0 {
		cfg.ThroughputWindowSec = 30
	}
	if cfg.ThroughputFloorWindowSec <= 0 {
		cfg.ThroughputFloorWindowSec = 30
	}
	if cfg.AdapterStopTimeoutSec <= 0 {
		cfg.AdapterStopTimeoutSec = 5
	}
	if cfg.MaxConcurrencyStep <= 0 {
		cfg.MaxConcurrencyStep = 1
	}
	if cfg.LogMaxSizeMB < 1 {
		cfg.LogMaxSizeMB = 50
	}
	return nil
}

type telemetrySampler interface {
	Sample(context.Context) telemetry.TelemetrySample
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

func applyProfileDefaults(
	ctx context.Context,
	cfg *Config,
	workloadType string,
	profilePath string,
	startConcurrencyExplicit bool,
) (bool, error) {
	return applyProfileDefaultsWithSampler(ctx, cfg, workloadType, profilePath, startConcurrencyExplicit, telemetry.NewCollector())
}

func applyProfileDefaultsWithSampler(
	ctx context.Context,
	cfg *Config,
	workloadType string,
	profilePath string,
	startConcurrencyExplicit bool,
	sampler telemetrySampler,
) (bool, error) {
	if cfg == nil {
		return false, nil
	}
	if sampler == nil {
		sampler = telemetry.NewCollector()
	}
	if strings.TrimSpace(profilePath) == "" {
		return false, nil
	}
	sample := sampler.Sample(ctx)
	gpuID := sample.GPUUUID
	if !sample.GPUUUIDValid {
		gpuID = calibration.UnknownProfileGPUID
	}

	profile, found, err := calibration.LoadProfile(profilePath, gpuID, workloadType)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}

	if profile.BaselineThroughput > 0 {
		cfg.InitialBaselineThroughput = profile.BaselineThroughput
	}
	if !startConcurrencyExplicit && profile.SafeConcurrencyCeiling > 0 {
		cfg.StartConcurrency = clampInt(profile.SafeConcurrencyCeiling, cfg.MinConcurrency, cfg.MaxConcurrency)
	}
	return true, nil
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "daemon":
		if err := runDaemon(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "daemon failed: %v\n", err)
			os.Exit(1)
		}
	case "control":
		if err := runControl(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "control failed: %v\n", err)
			os.Exit(1)
		}
	case "calibrate":
		if err := runCalibration(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "calibrate failed: %v\n", err)
			os.Exit(1)
		}
	case "report":
		if err := runReport(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "report failed: %v\n", err)
			os.Exit(1)
		}
	case "simulate":
		if err := runSimulate(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "simulate failed: %v\n", err)
			os.Exit(1)
		}
	default:
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Usage: guardian <command>")
	fmt.Println("  daemon  [--listen=<addr>]")
	fmt.Println("  control --cmd \"<command>\" [flags]")
	fmt.Println("  calibrate --cmd \"<command>\" [flags]")
	fmt.Println("  report [flags]")
	fmt.Println("  simulate --telemetry-log <path> [flags]")
}

func runDaemon(args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	listen := fs.String("listen", daemon.DefaultListenAddress, "Daemon listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	server := daemon.NewServer(*listen)
	return server.Serve()
}

func runControl(args []string) error {
	configPath := detectConfigPath(args)
	cfg, err := loadConfigFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to load config: %w", err)
	}

	fs := flag.NewFlagSet("control", flag.ContinueOnError)
	cmd := fs.String("cmd", cfg.Command, "Command to run, for example: python generate_xtts.py")
	_ = fs.String("config", configPath, "Optional JSON config path")
	poll := fs.Int("poll-interval-sec", cfg.PollIntervalSec, "Telemetry polling interval in seconds")
	softTemp := fs.Float64("soft-temp", cfg.SoftTemp, "Soft temperature threshold in °C")
	hardTemp := fs.Float64("hard-temp", cfg.HardTemp, "Hard temperature threshold in °C")
	minConc := fs.Int("min-concurrency", cfg.MinConcurrency, "Minimum concurrency")
	maxConc := fs.Int("max-concurrency", cfg.MaxConcurrency, "Maximum concurrency")
	startConc := fs.Int("start-concurrency", cfg.StartConcurrency, "Initial concurrency")
	floor := fs.Float64("throughput-floor-ratio", cfg.ThroughputFloorRatio, "Throughput floor as fraction of baseline")
	floorSlowdown := fs.Float64("throughput-slowdown-floor-ratio", cfg.ThroughputSlowdownFloorRatio, "Fallback throughput slowdown floor ratio (2x slowdown default is 0.5)")
	tempHysteresis := fs.Float64("temp-hysteresis-c", cfg.TempHysteresisC, "Temperature debounce margin before scale-up")
	tpRecovery := fs.Float64("throughput-recovery-margin", cfg.ThroughputRecoveryMargin, "Throughput recovery margin above floor before scale-up")
	tpRecoveryAttempts := fs.Int("throughput-recovery-max-attempts", cfg.ThroughputRecoveryMaxAttempts, "Max aggressive recovery attempts before pause")
	tpRecoveryMultiplier := fs.Int("throughput-recovery-step-multiplier", cfg.ThroughputRecoveryStepMultiplier, "Multiplier for aggressive recovery step")
	memLimit := fs.Float64("memory-pressure-limit", cfg.MemoryPressureLimit, "Memory pressure limit above which to reduce load")
	riskLimit := fs.Float64("throttle-risk-limit", cfg.ThrottleRiskLimit, "Throttle risk limit above which to reduce load")
	telemetryLogPath := fs.String("telemetry-log", cfg.TelemetryLogPath, "Path for timestamped telemetry samples")
	cooldown := fs.Int("adjustment-cooldown-sec", cfg.AdjustmentCooldownSec, "Action cooldown in seconds")
	blWindow := fs.Int("baseline-window-sec", cfg.BaselineWindowSec, "Baseline warmup window in seconds")
	tpWindow := fs.Int("throughput-window-sec", cfg.ThroughputWindowSec, "Throughput lookback window in seconds")
	tpFloorWindow := fs.Int("throughput-floor-window-sec", cfg.ThroughputFloorWindowSec, "Required duration below throughput floor to trigger a scale-down")
	stopTimeout := fs.Int("adapter-stop-timeout-sec", cfg.AdapterStopTimeoutSec, "Graceful stop timeout in seconds")
	maxStep := fs.Int("max-concurrency-step", cfg.MaxConcurrencyStep, "Maximum concurrency step size")
	logFile := fs.String("log-file", cfg.LogPath, "Path for structured JSON logs")
	logMaxMB := fs.Int("log-max-size-mb", cfg.LogMaxSizeMB, "Log rotation size in MB")
	workloadLog := fs.String("workload-log", cfg.WorkloadLogPath, "Path for raw workload stdout/stderr log")
	echoOutput := fs.Bool("echo-workload-output", cfg.EchoWorkloadOutput, "Echo workload output to console")
	maxTicks := fs.Int("max-ticks", 0, "Internal limit for control loop iterations (0 means unlimited)")
	profilePath := fs.String("profile-path", defaultProfilePath, "Profile store to restore workload baseline/concurrency defaults from")
	workloadType := fs.String("workload-type", defaultWorkloadType, "Workload type key used for profile restore")

	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg.Command = strings.TrimSpace(*cmd)
	cfg.PollIntervalSec = *poll
	cfg.SoftTemp = *softTemp
	cfg.HardTemp = *hardTemp
	cfg.MinConcurrency = *minConc
	cfg.MaxConcurrency = *maxConc
	cfg.StartConcurrency = *startConc
	cfg.ThroughputFloorRatio = *floor
	cfg.ThroughputSlowdownFloorRatio = *floorSlowdown
	cfg.TempHysteresisC = *tempHysteresis
	cfg.ThroughputRecoveryMargin = *tpRecovery
	cfg.ThroughputRecoveryMaxAttempts = *tpRecoveryAttempts
	cfg.ThroughputRecoveryStepMultiplier = *tpRecoveryMultiplier
	cfg.MemoryPressureLimit = *memLimit
	cfg.ThrottleRiskLimit = *riskLimit
	cfg.TelemetryLogPath = *telemetryLogPath
	cfg.AdjustmentCooldownSec = *cooldown
	cfg.MaxConcurrencyStep = *maxStep
	cfg.BaselineWindowSec = *blWindow
	cfg.ThroughputWindowSec = *tpWindow
	cfg.ThroughputFloorWindowSec = *tpFloorWindow
	cfg.AdapterStopTimeoutSec = *stopTimeout
	cfg.LogPath = *logFile
	cfg.LogMaxSizeMB = *logMaxMB
	cfg.WorkloadLogPath = *workloadLog
	cfg.EchoWorkloadOutput = *echoOutput
	cfg.MaxTicks = *maxTicks
	cfg.ProfilePath = strings.TrimSpace(*profilePath)
	cfg.WorkloadType = strings.TrimSpace(*workloadType)
	if cfg.WorkloadType == "" {
		cfg.WorkloadType = defaultWorkloadType
	}

	startConcExplicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "start-concurrency" {
			startConcExplicit = true
		}
	})

	if err := normalizeConfig(&cfg); err != nil {
		return err
	}
	loadedProfile, err := applyProfileDefaults(context.Background(), &cfg, cfg.WorkloadType, cfg.ProfilePath, startConcExplicit)
	if err != nil {
		return fmt.Errorf("load profile: %w", err)
	}
	if loadedProfile {
		// intentionally continue; these defaults are used by both daemon and local engine startup paths
	}

	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()
	defer func() {
		signal.Stop(sigCh)
		cancel()
	}()

	delegated, err := startViaDaemonIfAvailable(ctx, cfg)
	if err != nil {
		return err
	}
	if delegated {
		return nil
	}

	return runControlLocal(ctx, cfg)
}

func runControlLocal(ctx context.Context, cfg Config) error {
	log, err := logger.New(cfg.LogPath, int64(cfg.LogMaxSizeMB)*1024*1024, true)
	if err != nil {
		return fmt.Errorf("failed to initialize logger: %w", err)
	}
	defer log.Close()

	log.Info("starting control loop", map[string]interface{}{
		"command":                     cfg.Command,
		"poll_seconds":                cfg.PollIntervalSec,
		"min":                         cfg.MinConcurrency,
		"max":                         cfg.MaxConcurrency,
		"start":                       cfg.StartConcurrency,
		"soft_temp":                   cfg.SoftTemp,
		"hard_temp":                   cfg.HardTemp,
		"initial_baseline_throughput": cfg.InitialBaselineThroughput,
	})

	controlCfg := control.RuleConfig{
		SoftTemp:                         cfg.SoftTemp,
		HardTemp:                         cfg.HardTemp,
		ThroughputFloorRatio:             cfg.ThroughputFloorRatio,
		ThroughputSlowdownFloorRatio:     cfg.ThroughputSlowdownFloorRatio,
		ThroughputWindowSec:              cfg.ThroughputWindowSec,
		ThroughputFloorSec:               cfg.ThroughputFloorWindowSec,
		ThroughputRecoveryMaxAttempts:    cfg.ThroughputRecoveryMaxAttempts,
		ThroughputRecoveryStepMultiplier: cfg.ThroughputRecoveryStepMultiplier,
		TempHysteresisC:                  cfg.TempHysteresisC,
		ThroughputRecoveryMargin:         cfg.ThroughputRecoveryMargin,
		MemoryPressureLimit:              cfg.MemoryPressureLimit,
		ThrottleRiskLimit:                cfg.ThrottleRiskLimit,
		MaxConcurrencyStep:               cfg.MaxConcurrencyStep,
	}
	controller := control.NewRuleController(controlCfg)

	adapterCfg := adapter.Config{
		OutputPath:  cfg.WorkloadLogPath,
		StopTimeout: time.Duration(cfg.AdapterStopTimeoutSec) * time.Second,
		EchoOutput:  cfg.EchoWorkloadOutput,
	}
	aw := adapter.NewXttsAdapter(adapterCfg)

	telemetryCollector := telemetry.NewCollector()
	th := throughput.NewTracker(time.Duration(cfg.ThroughputWindowSec)*time.Second, time.Duration(cfg.BaselineWindowSec)*time.Second)

	engineCfg := engine.Config{
		Command:                   cfg.Command,
		PollInterval:              time.Duration(cfg.PollIntervalSec) * time.Second,
		SoftTemp:                  cfg.SoftTemp,
		HardTemp:                  cfg.HardTemp,
		MinConcurrency:            cfg.MinConcurrency,
		MaxConcurrency:            cfg.MaxConcurrency,
		StartConcurrency:          cfg.StartConcurrency,
		ThroughputFloorRatio:      cfg.ThroughputFloorRatio,
		AdjustmentCooldown:        time.Duration(cfg.AdjustmentCooldownSec) * time.Second,
		ThroughputWindow:          time.Duration(cfg.ThroughputWindowSec) * time.Second,
		ThroughputFloorWindow:     time.Duration(cfg.ThroughputFloorWindowSec) * time.Second,
		BaselineWindow:            time.Duration(cfg.BaselineWindowSec) * time.Second,
		MaxConcurrencyStep:        cfg.MaxConcurrencyStep,
		TelemetryLogPath:          cfg.TelemetryLogPath,
		MaxTicks:                  cfg.MaxTicks,
		InitialBaselineThroughput: cfg.InitialBaselineThroughput,
	}

	eng := engine.New(
		engineCfg,
		aw,
		controller,
		telemetryCollector,
		th,
		log,
	)
	if _, err := eng.Start(ctx); err != nil {
		log.Error("engine failed", map[string]interface{}{"error": err.Error()})
		return err
	}
	return nil
}

func runCalibration(args []string) error {
	cfg := defaultConfig()
	fs := flag.NewFlagSet("calibrate", flag.ContinueOnError)
	cmd := fs.String("cmd", "", "Command to run, for example: python generate_xtts.py")
	poll := fs.Int("poll-interval-sec", cfg.PollIntervalSec, "Calibration telemetry polling interval in seconds")
	minConc := fs.Int("min-concurrency", cfg.MinConcurrency, "Minimum concurrency to test")
	maxConc := fs.Int("max-concurrency", cfg.MaxConcurrency, "Maximum concurrency to test")
	step := fs.Int("concurrency-step", 1, "Concurrency increment between calibration steps")
	stepDur := fs.Int("calibration-step-duration-sec", 8, "Duration in seconds to sample each concurrency level (unless step-samples is set)")
	stepSamples := fs.Int("step-samples", 0, "Exact number of samples to collect per step (overrides step duration)")
	warmup := fs.Int("warmup-samples", 1, "Warmup samples to skip for throughput averaging per step")
	hardTemp := fs.Float64("hard-temp", cfg.HardTemp, "Hard temperature threshold for safe concurrency ceiling")
	throughputDrop := fs.Float64("throughput-drop-ratio", cfg.ThroughputFloorRatio, "Minimum throughput ratio to keep concurrency on safety frontier")
	workloadLog := fs.String("workload-log", "", "Path for raw workload stdout/stderr log")
	stopTimeout := fs.Int("adapter-stop-timeout-sec", cfg.AdapterStopTimeoutSec, "Graceful stop timeout in seconds")
	outputPath := fs.String("output", "", "Write calibration profile JSON to this path")
	profilePath := fs.String("profile-path", defaultProfilePath, "Profile store used for later profile restore")
	workloadType := fs.String("workload-type", defaultWorkloadType, "Workload type key used for profile persistence")

	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg.Command = strings.TrimSpace(*cmd)
	cfg.PollIntervalSec = *poll
	cfg.MinConcurrency = *minConc
	cfg.MaxConcurrency = *maxConc
	if cfg.Command == "" {
		return fmt.Errorf("command is required; use --cmd")
	}
	if err := normalizeConfig(&cfg); err != nil {
		return err
	}
	if *step <= 0 {
		return fmt.Errorf("concurrency-step must be greater than zero")
	}
	if *stepSamples < 0 {
		return fmt.Errorf("step-samples cannot be negative")
	}
	if *stepDur <= 0 {
		return fmt.Errorf("calibration-step-duration-sec must be greater than zero")
	}
	if *throughputDrop <= 0 || *throughputDrop > 1 {
		return fmt.Errorf("throughput-drop-ratio must be in (0,1]")
	}
	if *warmup < 0 {
		return fmt.Errorf("warmup-samples cannot be negative")
	}

	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()
	defer func() {
		signal.Stop(sigCh)
		cancel()
	}()

	adapterCfg := adapter.Config{
		OutputPath:  *workloadLog,
		StopTimeout: time.Duration(*stopTimeout) * time.Second,
	}
	aw := adapter.NewXttsAdapter(adapterCfg)
	telemetryCollector := telemetry.NewCollector()
	gpuSample := telemetryCollector.Sample(ctx)

	calCfg := calibration.Config{
		Command:             cfg.Command,
		PollInterval:        time.Duration(cfg.PollIntervalSec) * time.Second,
		MinConcurrency:      cfg.MinConcurrency,
		MaxConcurrency:      cfg.MaxConcurrency,
		ConcurrencyStep:     *step,
		StepDuration:        time.Duration(*stepDur) * time.Second,
		StepSamples:         *stepSamples,
		WarmupSamples:       *warmup,
		HardTempC:           *hardTemp,
		ThroughputDropRatio: *throughputDrop,
	}

	profile, err := calibration.Run(ctx, calCfg, aw, telemetryCollector)
	if err != nil {
		return fmt.Errorf("calibration failed: %w", err)
	}
	profile.GPUUUID = ""
	if gpuSample.GPUUUIDValid {
		profile.GPUUUID = gpuSample.GPUUUID
	}
	profile.WorkloadType = strings.TrimSpace(*workloadType)
	if profile.WorkloadType == "" {
		profile.WorkloadType = defaultWorkloadType
	}
	if err := calibration.SaveProfile(*profilePath, profile.GPUUUID, profile.WorkloadType, profile); err != nil {
		return fmt.Errorf("persist profile: %w", err)
	}

	payload, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return fmt.Errorf("serialize calibration profile: %w", err)
	}
	if *outputPath != "" {
		if err := os.WriteFile(*outputPath, payload, 0o600); err != nil {
			return fmt.Errorf("write calibration profile: %w", err)
		}
	}
	fmt.Println(string(payload))
	return nil
}

func runReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	controlLog := fs.String("control-log", "", "Path to control log JSONL file")
	telemetryLog := fs.String("telemetry-log", "", "Path to telemetry log JSONL file")
	floor := fs.Float64("throughput-floor-ratio", 0.7, "Throughput floor ratio used for recovery summary")
	outputPath := fs.String("output", "", "Write report JSON to this path")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*controlLog) == "" && strings.TrimSpace(*telemetryLog) == "" {
		return fmt.Errorf("at least one of --control-log or --telemetry-log is required")
	}

	rep, err := report.Generate(strings.TrimSpace(*controlLog), strings.TrimSpace(*telemetryLog), *floor)
	if err != nil {
		return fmt.Errorf("generate report: %w", err)
	}

	payload, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return fmt.Errorf("serialize report: %w", err)
	}
	if strings.TrimSpace(*outputPath) != "" {
		if err := os.WriteFile(*outputPath, payload, 0o600); err != nil {
			return fmt.Errorf("write report: %w", err)
		}
		return nil
	}

	fmt.Println(string(payload))
	return nil
}

func runSimulate(args []string) error {
	fs := flag.NewFlagSet("simulate", flag.ContinueOnError)
	telemetryLog := fs.String("telemetry-log", "", "Recorded telemetry JSONL log")
	controlLog := fs.String("control-log", "", "Recorded control JSONL log (optional throughput source)")
	minConc := fs.Int("min-concurrency", 1, "Minimum replay concurrency")
	maxConc := fs.Int("max-concurrency", 4, "Maximum replay concurrency")
	startConc := fs.Int("start-concurrency", 1, "Initial replay concurrency")
	floor := fs.Float64("throughput-floor-ratio", 0.7, "Throughput floor ratio for policy replay")
	floorSlowdown := fs.Float64("throughput-slowdown-floor-ratio", 0.5, "Throughput slowdown floor ratio")
	temp := fs.Float64("soft-temp", 78, "Soft temperature threshold in °C")
	hardTemp := fs.Float64("hard-temp", 84, "Hard temperature threshold in °C")
	tempHysteresis := fs.Float64("temp-hysteresis-c", 2, "Temperature debounce before scale-up")
	tpRecovery := fs.Float64("throughput-recovery-margin", 0.05, "Throughput recovery margin before scale-up")
	tpRecoveryAttempts := fs.Int("throughput-recovery-max-attempts", 3, "Max recovery attempts before pause")
	tpRecoveryMultiplier := fs.Int("throughput-recovery-step-multiplier", 2, "Multiplier for aggressive recovery step")
	memLimit := fs.Float64("memory-pressure-limit", 0.9, "Memory pressure limit above which to reduce load")
	riskLimit := fs.Float64("throttle-risk-limit", 0.85, "Throttle risk limit above which to reduce load")
	cooldown := fs.Int("adjustment-cooldown-sec", 10, "Action cooldown in seconds")
	blWindow := fs.Int("baseline-window-sec", 120, "Baseline estimation warmup window in seconds")
	tpWindow := fs.Int("throughput-window-sec", 30, "Throughput averaging window in seconds")
	tpFloorWindow := fs.Int("throughput-floor-window-sec", 30, "Required floor-duration window in seconds")
	maxStep := fs.Int("max-concurrency-step", 1, "Maximum concurrency delta per action")
	initialBaseline := fs.Float64("initial-baseline-throughput", 0, "Baseline throughput for what-if replay")
	poll := fs.Int("poll-interval-sec", 2, "Sampling fallback interval when timestamps are missing")
	outputPath := fs.String("output", "", "Write simulation summary JSON to this path")
	eventLog := fs.String("event-log", "", "Write replay tick events to this path")
	maxTicks := fs.Int("max-ticks", 0, "Limit replay to this many ticks (0 means no limit)")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*telemetryLog) == "" {
		return fmt.Errorf("telemetry log is required")
	}
	if *minConc <= 0 {
		*minConc = 1
	}
	if *maxConc < *minConc {
		*maxConc = *minConc
	}
	if *startConc < *minConc {
		*startConc = *minConc
	}
	if *startConc > *maxConc {
		*startConc = *maxConc
	}

	ctrlCfg := control.RuleConfig{
		SoftTemp:                         *temp,
		HardTemp:                         *hardTemp,
		ThroughputFloorRatio:             *floor,
		ThroughputSlowdownFloorRatio:     *floorSlowdown,
		ThroughputWindowSec:              *tpWindow,
		ThroughputFloorSec:               *tpFloorWindow,
		ThroughputRecoveryMaxAttempts:    *tpRecoveryAttempts,
		ThroughputRecoveryStepMultiplier: *tpRecoveryMultiplier,
		TempHysteresisC:                  *tempHysteresis,
		ThroughputRecoveryMargin:         *tpRecovery,
		MemoryPressureLimit:              *memLimit,
		ThrottleRiskLimit:                *riskLimit,
		MaxConcurrencyStep:               *maxStep,
	}

	res, err := simulation.Replay(simulation.ReplayConfig{
		TelemetryLogPath:           *telemetryLog,
		ControlLogPath:             *controlLog,
		MinConcurrency:             *minConc,
		MaxConcurrency:             *maxConc,
		StartConcurrency:           *startConc,
		MaxConcurrencyStep:         *maxStep,
		InitialBaselineThroughput:  *initialBaseline,
		RuleCfg:                    ctrlCfg,
		AdjustmentCooldown:        time.Duration(*cooldown) * time.Second,
		ThroughputWindow:          time.Duration(*tpWindow) * time.Second,
		BaselineWindow:            time.Duration(*blWindow) * time.Second,
		PollInterval:              time.Duration(*poll) * time.Second,
		EventLogPath:              *eventLog,
		MaxTicks:                  *maxTicks,
	})
	if err != nil {
		return fmt.Errorf("replay run failed: %w", err)
	}

	payload, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return fmt.Errorf("serialize simulation result: %w", err)
	}

	if strings.TrimSpace(*outputPath) != "" {
		return os.WriteFile(*outputPath, payload, 0o600)
	}
	fmt.Println(string(payload))
	return nil
}

func startViaDaemonIfAvailable(ctx context.Context, cfg Config) (bool, error) {
	health, err := fetchDaemonHealth()
	if err != nil {
		return false, nil
	}
	if health.Version != daemon.APIVersion {
		return false, nil
	}

	sessionID, err := startDaemonSession(cfg)
	if err != nil {
		return false, err
	}
	return monitorDaemonSession(ctx, sessionID)
}

func startDaemonSession(cfg Config) (string, error) {
	req := daemon.StartRequest{
		Command:                          cfg.Command,
		PollIntervalSec:                  cfg.PollIntervalSec,
		SoftTemp:                         cfg.SoftTemp,
		HardTemp:                         cfg.HardTemp,
		MinConcurrency:                   cfg.MinConcurrency,
		MaxConcurrency:                   cfg.MaxConcurrency,
		StartConcurrency:                 cfg.StartConcurrency,
		ThroughputFloorRatio:             cfg.ThroughputFloorRatio,
		ThroughputSlowdownFloorRatio:     cfg.ThroughputSlowdownFloorRatio,
		AdjustmentCooldownSec:            cfg.AdjustmentCooldownSec,
		ThroughputRecoveryMaxAttempts:    cfg.ThroughputRecoveryMaxAttempts,
		ThroughputRecoveryStepMultiplier: cfg.ThroughputRecoveryStepMultiplier,
		TelemetryLogPath:                 cfg.TelemetryLogPath,
		BaselineWindowSec:                cfg.BaselineWindowSec,
		ThroughputWindowSec:              cfg.ThroughputWindowSec,
		ThroughputFloorWindowSec:         cfg.ThroughputFloorWindowSec,
		MaxConcurrencyStep:               cfg.MaxConcurrencyStep,
		AdapterStopTimeoutSec:            cfg.AdapterStopTimeoutSec,
		LogPath:                          cfg.LogPath,
		LogMaxSizeMB:                     cfg.LogMaxSizeMB,
		WorkloadLogPath:                  cfg.WorkloadLogPath,
		EchoWorkloadOutput:               cfg.EchoWorkloadOutput,
		MaxTicks:                         cfg.MaxTicks,
		InitialBaselineThroughput:        cfg.InitialBaselineThroughput,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("failed to serialize daemon start request: %w", err)
	}

	r, err := http.Post(daemonBaseURL+"/v1/sessions", "application/json", strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(r.Body)
		return "", fmt.Errorf("daemon start failed: %s", strings.TrimSpace(string(b)))
	}
	var startResp daemon.SessionResponse
	if err := json.NewDecoder(r.Body).Decode(&startResp); err != nil {
		return "", err
	}
	return startResp.SessionID, nil
}

func monitorDaemonSession(ctx context.Context, sessionID string) (bool, error) {
	client := &http.Client{}
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/v1/sessions/%s/stop", daemonBaseURL, sessionID), nil)
			resp, err := client.Do(req)
			if err == nil {
				_ = resp.Body.Close()
			}
			return true, nil
		case <-ticker.C:
			req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/v1/sessions/%s", daemonBaseURL, sessionID), nil)
			if err != nil {
				return true, err
			}
			resp, err := client.Do(req)
			if err != nil {
				return true, err
			}
			if resp.StatusCode != http.StatusOK {
				_ = resp.Body.Close()
				return true, fmt.Errorf("daemon session query failed: status=%d", resp.StatusCode)
			}
			var session daemon.SessionState
			if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
				_ = resp.Body.Close()
				return true, err
			}
			_ = resp.Body.Close()
			if !session.Running {
				return true, nil
			}
		}
	}
}

func fetchDaemonHealth() (*daemon.HealthResponse, error) {
	client := &http.Client{Timeout: 300 * time.Millisecond}
	resp, err := client.Get(daemonBaseURL + "/v1/health")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("daemon health status %d", resp.StatusCode)
	}
	var out daemon.HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}
