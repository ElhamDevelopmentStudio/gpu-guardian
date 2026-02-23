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
	"github.com/elhamdev/gpu-guardian/internal/control"
	"github.com/elhamdev/gpu-guardian/internal/daemon"
	"github.com/elhamdev/gpu-guardian/internal/engine"
	"github.com/elhamdev/gpu-guardian/internal/logger"
	"github.com/elhamdev/gpu-guardian/internal/telemetry"
	"github.com/elhamdev/gpu-guardian/internal/throughput"
)

const mvpConfigPath = ".guardian-mvp.json"

var daemonBaseURL = "http://" + daemon.DefaultListenAddress

type Config struct {
	Command                  string  `json:"command"`
	PollIntervalSec          int     `json:"poll_interval_sec"`
	SoftTemp                 float64 `json:"soft_temp"`
	HardTemp                 float64 `json:"hard_temp"`
	MinConcurrency           int     `json:"min_concurrency"`
	MaxConcurrency           int     `json:"max_concurrency"`
	StartConcurrency         int     `json:"start_concurrency"`
	ThroughputFloorRatio     float64 `json:"throughput_floor_ratio"`
	AdjustmentCooldownSec    int     `json:"adjustment_cooldown_sec"`
	BaselineWindowSec        int     `json:"baseline_window_sec"`
	ThroughputWindowSec      int     `json:"throughput_window_sec"`
	ThroughputFloorWindowSec int     `json:"throughput_floor_window_sec"`
	AdapterStopTimeoutSec    int     `json:"adapter_stop_timeout_sec"`
	LogPath                  string  `json:"log_file"`
	LogMaxSizeMB             int     `json:"log_max_size_mb"`
	WorkloadLogPath          string  `json:"workload_log_path"`
	EchoWorkloadOutput       bool    `json:"echo_workload_output"`
	MaxTicks                 int     `json:"-"`
}

func defaultConfig() Config {
	return Config{
		PollIntervalSec:          2,
		SoftTemp:                 78,
		HardTemp:                 84,
		MinConcurrency:           1,
		MaxConcurrency:           8,
		StartConcurrency:         4,
		ThroughputFloorRatio:     0.7,
		AdjustmentCooldownSec:    10,
		BaselineWindowSec:        120,
		ThroughputWindowSec:      30,
		ThroughputFloorWindowSec: 30,
		AdapterStopTimeoutSec:    5,
		LogPath:                  "guardian.log",
		LogMaxSizeMB:             50,
		EchoWorkloadOutput:       false,
	}
}

func loadConfigFile(path string) (Config, error) {
	cfg := defaultConfig()
	if path == "" {
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
	if cfg.LogMaxSizeMB < 1 {
		cfg.LogMaxSizeMB = 50
	}
	return nil
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
	default:
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Usage: guardian <command>")
	fmt.Println("  daemon  [--listen=<addr>]")
	fmt.Println("  control --cmd \"<command>\" [flags]")
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
	cooldown := fs.Int("adjustment-cooldown-sec", cfg.AdjustmentCooldownSec, "Action cooldown in seconds")
	blWindow := fs.Int("baseline-window-sec", cfg.BaselineWindowSec, "Baseline warmup window in seconds")
	tpWindow := fs.Int("throughput-window-sec", cfg.ThroughputWindowSec, "Throughput lookback window in seconds")
	tpFloorWindow := fs.Int("throughput-floor-window-sec", cfg.ThroughputFloorWindowSec, "Required duration below throughput floor to trigger a scale-down")
	stopTimeout := fs.Int("adapter-stop-timeout-sec", cfg.AdapterStopTimeoutSec, "Graceful stop timeout in seconds")
	logFile := fs.String("log-file", cfg.LogPath, "Path for structured JSON logs")
	logMaxMB := fs.Int("log-max-size-mb", cfg.LogMaxSizeMB, "Log rotation size in MB")
	workloadLog := fs.String("workload-log", cfg.WorkloadLogPath, "Path for raw workload stdout/stderr log")
	echoOutput := fs.Bool("echo-workload-output", cfg.EchoWorkloadOutput, "Echo workload output to console")
	maxTicks := fs.Int("max-ticks", 0, "Internal limit for control loop iterations (0 means unlimited)")

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
	cfg.AdjustmentCooldownSec = *cooldown
	cfg.BaselineWindowSec = *blWindow
	cfg.ThroughputWindowSec = *tpWindow
	cfg.ThroughputFloorWindowSec = *tpFloorWindow
	cfg.AdapterStopTimeoutSec = *stopTimeout
	cfg.LogPath = *logFile
	cfg.LogMaxSizeMB = *logMaxMB
	cfg.WorkloadLogPath = *workloadLog
	cfg.EchoWorkloadOutput = *echoOutput
	cfg.MaxTicks = *maxTicks

	if err := normalizeConfig(&cfg); err != nil {
		return err
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
		"command":      cfg.Command,
		"poll_seconds": cfg.PollIntervalSec,
		"min":          cfg.MinConcurrency,
		"max":          cfg.MaxConcurrency,
		"start":        cfg.StartConcurrency,
		"soft_temp":    cfg.SoftTemp,
		"hard_temp":    cfg.HardTemp,
	})

	controlCfg := control.RuleConfig{
		SoftTemp:             cfg.SoftTemp,
		HardTemp:             cfg.HardTemp,
		ThroughputFloorRatio: cfg.ThroughputFloorRatio,
		ThroughputWindowSec:  cfg.ThroughputWindowSec,
		ThroughputFloorSec:   cfg.ThroughputFloorWindowSec,
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
		Command:               cfg.Command,
		PollInterval:          time.Duration(cfg.PollIntervalSec) * time.Second,
		SoftTemp:              cfg.SoftTemp,
		HardTemp:              cfg.HardTemp,
		MinConcurrency:        cfg.MinConcurrency,
		MaxConcurrency:        cfg.MaxConcurrency,
		StartConcurrency:      cfg.StartConcurrency,
		ThroughputFloorRatio:  cfg.ThroughputFloorRatio,
		AdjustmentCooldown:    time.Duration(cfg.AdjustmentCooldownSec) * time.Second,
		ThroughputWindow:      time.Duration(cfg.ThroughputWindowSec) * time.Second,
		ThroughputFloorWindow: time.Duration(cfg.ThroughputFloorWindowSec) * time.Second,
		BaselineWindow:        time.Duration(cfg.BaselineWindowSec) * time.Second,
		MaxTicks:              cfg.MaxTicks,
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
		Command:                  cfg.Command,
		PollIntervalSec:          cfg.PollIntervalSec,
		SoftTemp:                 cfg.SoftTemp,
		HardTemp:                 cfg.HardTemp,
		MinConcurrency:           cfg.MinConcurrency,
		MaxConcurrency:           cfg.MaxConcurrency,
		StartConcurrency:         cfg.StartConcurrency,
		ThroughputFloorRatio:     cfg.ThroughputFloorRatio,
		AdjustmentCooldownSec:    cfg.AdjustmentCooldownSec,
		BaselineWindowSec:        cfg.BaselineWindowSec,
		ThroughputWindowSec:      cfg.ThroughputWindowSec,
		ThroughputFloorWindowSec: cfg.ThroughputFloorWindowSec,
		AdapterStopTimeoutSec:    cfg.AdapterStopTimeoutSec,
		LogPath:                  cfg.LogPath,
		LogMaxSizeMB:             cfg.LogMaxSizeMB,
		WorkloadLogPath:          cfg.WorkloadLogPath,
		EchoWorkloadOutput:       cfg.EchoWorkloadOutput,
		MaxTicks:                 cfg.MaxTicks,
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
