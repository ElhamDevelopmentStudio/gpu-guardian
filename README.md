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

## MVP Scope (this implementation)

- No daemon mode
- No npm/pip packages
- No simulation mode
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

All flags:

- `--cmd` command to run
- `--poll-interval-sec`
- `--soft-temp`
- `--hard-temp`
- `--min-concurrency`, `--max-concurrency`, `--start-concurrency`
- `--throughput-floor-ratio`
- `--adjustment-cooldown-sec`
- `--baseline-window-sec`
- `--throughput-window-sec`
- `--throughput-floor-window-sec`
- `--adapter-stop-timeout-sec`
- `--log-file`, `--log-max-size-mb`
- `--workload-log`
- `--echo-workload-output`
- `--max-ticks` (test/benchmark helper; runs loop for finite iterations)
- `--config <path>` (optional JSON config; values are defaults with overrides)

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
  "throughput_floor_ratio": 0.7,
  "adjustment_cooldown_sec": 10,
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

## Tests

- `go test ./...` runs unit tests
- `go test ./... -tags=integration` runs the e2e integration test using a stub `nvidia-smi`

## Notes

- Workload concurrency is injected through `CONCURRENCY` and `XTTS_CONCURRENCY` environment variables.
- Control loop actions are restart-based: when it needs to change concurrency, the workload process is
  restarted with the new concurrency level.
