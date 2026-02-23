# GPU Stress Guardian (MVP)

Linux/NVIDIA MVP CLI for local workload regulation during long GPU runs.

## What this MVP does

`guardian` runs a command (for example XTTS), polls GPU telemetry, and applies a simple
rule-based control loop to adjust concurrency.

- Polls telemetry with `nvidia-smi` every 2 seconds (configurable)
- Measures throughput from workload output growth
- Reduces concurrency on:
  - Hard temperature threshold breach
  - Rising soft temperature breach
  - Sustained throughput floor violation
- Increases concurrency when conditions are healthy and below max cap
- Logs structured decisions and telemetry

## Current capabilities

- Daemon mode (local API)
- `calibrate` mode for concurrency/thermal/VRAM baseline sweeps
- `npm/` CLI wrapper package + optional JS client integration
- `python/` packaging scaffold for `pip` wrapper + Python API client
- `simulate` mode for deterministic offline replay/what-if policy runs
- Shared API contract file for wrappers: `ecosystem_client_api_contract.json`
- No RL
- No multi-GPU or multi-OS support

## Build

```bash
go build ./cmd/guardian
```

## Run

```bash
guardian control --cmd "python generate_xtts.py"
```

```bash
guardian calibrate --cmd "python generate_xtts.py"
```

```bash
guardian report --control-log guardian.log --telemetry-log telemetry.log --throughput-floor-ratio 0.7 --output report.json
```

```bash
guardian simulate --telemetry-log telemetry.log --control-log guardian.log --initial-baseline-throughput 120 --max-concurrency 8 --output simulate.json
```

```bash
guardian observe --session-id default
```

All flags:

- `--cmd` command to run
- `--poll-interval-sec`
- `--soft-temp`
- `--hard-temp`
- `--temp-hysteresis-c`
- `--min-concurrency`, `--max-concurrency`, `--start-concurrency`
- `--throughput-recovery-margin`
- `--throughput-floor-ratio`
- `--adjustment-cooldown-sec`
- `--memory-pressure-limit`
- `--throttle-risk-limit`
- `--throughput-slowdown-floor-ratio`
- `--throughput-recovery-max-attempts`
- `--throughput-recovery-step-multiplier`
- `--telemetry-log`
- `--baseline-window-sec`
- `--throughput-window-sec`
- `--throughput-floor-window-sec`
- `--max-concurrency-step`
- `--adapter-stop-timeout-sec`
- `--log-file`, `--log-max-size-mb`
- `--workload-log`
- `--echo-workload-output`
- `--max-ticks` (test/benchmark helper; runs loop for finite iterations)
- `--calibration-step-duration-sec` (calibrate mode)
- `--min-concurrency`, `--max-concurrency`, `--concurrency-step` (calibrate mode)
- `--step-samples`, `--warmup-samples` (calibrate mode)
- `--throughput-drop-ratio`, `--hard-temp`, `--output` (calibrate mode)
- `--config <path>` (optional JSON config; values are defaults with overrides)
- `--profile-path` (shared profile persistence path, default `.guardian-profiles.json`)
- `--workload-type` (profile namespace key for `control` resume and `calibrate` persistence)
- `--control-log`, `--telemetry-log`, `--throughput-floor-ratio`, `--output` (report mode)
- `--telemetry-log`, `--control-log`, `--initial-baseline-throughput`, `--event-log`, `--output` (simulate mode)
  - `--session-id`, `--all`, `--telemetry`, `--output` (observe mode)

### JSON config

Save defaults to `.guardian-mvp.json` (or pass `--config`):

```json
{
  "poll_interval_sec": 2,
  "soft_temp": 78,
  "hard_temp": 84,
  "min_concurrency": 1,
  "max_concurrency": 8,
  "start_concurrency": 4,
  "temp_hysteresis_c": 2,
  "throughput_recovery_margin": 0.05,
  "throughput_slowdown_floor_ratio": 0.5,
  "memory_pressure_limit": 0.9,
  "throttle_risk_limit": 0.85,
  "throughput_recovery_max_attempts": 3,
  "throughput_recovery_step_multiplier": 2,
  "telemetry_log_path": "telemetry.log",
  "throughput_floor_ratio": 0.7,
  "adjustment_cooldown_sec": 10,
  "max_concurrency_step": 1,
  "baseline_window_sec": 120,
  "throughput_window_sec": 30,
  "throughput_floor_window_sec": 30,
  "adapter_stop_timeout_sec": 5,
  "log_file": "guardian.log",
  "log_max_size_mb": 50,
  "workload_log_path": ""
}
```

## Logging

`--log-file` writes structured JSON lines with decision and telemetry fields.
`--telemetry-log` writes timestamped raw telemetry samples to a JSONL log (one sample per line), useful for offline analysis and reporting.

State estimation fields are now included in run state and logs (`engine_tick`):

- `temp_slope_c_per_sec`
- `throughput_trend`
- `throttle_risk_score`
- `stability_index`
- `estimate_confidence`
- `action_reason`
- `action_signals` (explainability inputs that triggered the decision)

## Tests

- `go test ./...` runs unit tests
- `go test ./... -tags=integration` runs the e2e integration test using a stub `nvidia-smi`
- `go test ./... -tags=e2e` runs integration-style tests in e2e package mode
- `go test ./... -tags=regression` keeps regression checks if package tags are used

## Notes

- Workload concurrency is injected through `CONCURRENCY` and `XTTS_CONCURRENCY` environment variables.
- Control loop actions are restart-based: when it needs to change concurrency, the workload process is
  restarted with the new concurrency level.
