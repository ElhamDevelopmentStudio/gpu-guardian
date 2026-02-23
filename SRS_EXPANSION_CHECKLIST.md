# GPU Stress Guardian — SRS Expansion Checklist

Reference: [SRS.md](SRS.md)

## Goal
Create a complete implementation path from current MVP toward the SRS-defined engine-first platform.

## Notes on current position
The existing codebase already covers a Linux/NVIDIA MVP control loop (`guardian control --cmd ...`) with basic telemetry parsing, throughput estimation, cooldown, and safety adjustments. The checklist below is for all *remaining* SRS requirements needed for full scope.

## Priority legend
- **P0**: Blocking architecture foundations (must be completed to claim SRS-aligned core)
- **P1**: Required functional behavior for controlled production operation
- **P2**: Required supporting functionality and operator tooling
- **P3**: Distribution and cross-ecosystem requirements
- **P4**: Non-functional, reliability, and release hardening
- **P5**: Optional/advanced extensions from SRS future scope

## P0 — Core Engine + API surface foundations

1. **[x] FR-1 (Go canonical engine)**
   - Implement/confirm engine package boundaries and entrypoint contracts in Go as the single source of truth.
   - Deliverable: `internal/engine` with explicit state transitions and policy orchestration.

2. **[x] FR-10 + FR-8 + FR-9 (Versioned adapter contract + XTTS adapter in v1)**
   - Define adapter interface with: `Start`, `Pause`, `Resume`, `UpdateParameters`, `GetThroughput`, `GetProgress`.
   - Implement stable XTTS adapter that satisfies v1 of this interface.
   - Deliverable: interface version tag and adapter compatibility checks.

3. **[x] DR-1 + DR-2 + DR-3 + DR-4 + DR-5 + CLI-1 + CLI-3 (Daemon/API + versioning + CLI route selection)**
   - Local daemon process with versioned API namespace.
   - Endpoints at minimum: health, metrics, start/stop/control, session/telemetry/query.
   - Default localhost binding.
   - CLI automatically uses daemon when available; falls back to standalone mode only if daemon unavailable.
   - Deliverable: service contract with compatibility guarantees inside one major version line.

4. **[x] FR-2 (Session/state modes + FR-19 recovery-safe lifecycle)** *(done)*
   - Implemented: `internal/daemon/daemon.go` now maintains explicit session state (`mode`, `goal`, `retries`, `errors`, `last_action`, `last_sample`), supports `stateless`/`stateful` modes, persists state checkpoints to `checkpoint_path`, and performs pause/safe-stop after configured recovery failures.
   - Add explicit session model (`state`, `goal`, `last_action`, `last_sample`, `retries`, `errors`, checkpoints).
   - Include stateless/ephemeral mode and persisted session/profile mode.
   - Implement pause/safe-stop behavior when recovery fails by policy.

5. **[x] FR-14 + FR-15 + FR-16 + FR-20 (Engine control behavior)**
  - Implemented in `internal/control` (`RuleController`) and `internal/engine`.
  - Control loop remains frequency-driven via `poll_interval_sec` and now includes throttle-risk and memory-aware inputs.
  - Telemetry now collects power and clock signals (with graceful fallback to core fields) and derives memory pressure + throttle risk.
  - Engine now supports dynamic cooldown windows (policy override + directional anti-oscillation), bounded concurrency deltas, and per-adjustment logging.
  - Control knobs added to config/daemon request path for hysteresis and adjustment behavior.

6. **[x] FR-17 + FR-18 + FR-19 (Throughput floor + recovery fallback)**
   - Implemented configurable floor control in `internal/control` and daemon-aware pause behavior:
     - Configurable floor ratio (`throughput_floor_ratio`) and fallback slowdown ratio (`throughput_slowdown_floor_ratio`) with defaults `0.7` and `0.5`.
     - Aggressive recovery path after sustained floor violations and bounded recovery attempts (`throughput_recovery_max_attempts`).
     - Escalation to `pause` action when recovery attempts are exhausted.
   - Implemented `engine` pause action handling and daemon session `goal` transition to `paused` when controller requests pause, preserving stop/error context in session state.

## P1 — Telemetry and state estimation completeness

1. **[x] FR-3 + FR-4 + FR-5 (Telemetry breadth, persistence, resilience)**
   - ✅ Implemented telemetry breadth with throttling indicators via `clocks_throttle_reasons.active` when supported.
   - ✅ Added timestamped telemetry persistence through a JSONL sample store and CLI/configurable path (`--telemetry-log`, `telemetry_log_path`, engine `TelemetryLogPath`).
   - ✅ Missing/unsupported extended telemetry fields now degrade to core fields and are still sampled without crashing policy loop.
   - ✅ Added tests:
     - `internal/telemetry/telemetry_test.go` (full extended parse, fallback behavior, malformed output resiliency, sample store append)
     - `internal/engine/engine_test.go` (engine writes telemetry samples to configured sample log)
     - `cmd/guardian/main_test.go` integration assertion for telemetry log generation

2. **[ ] FR-6 + FR-7 (Derived state estimation)**
   - ✅ Added `internal/control.StateEstimate` with dT/dt (`temp_slope_c_per_sec`), throughput trend (`throughput_trend`), throttle risk score (`throttle_risk_score`), stability index (`stability_index`), and confidence.
   - ✅ Added `control.StateEstimator` with EMA-style temporal smoothing for noise control.
   - ✅ Threaded estimates into run state (`RunState.Estimate`) and CLI/engine logging.
   - ✅ Added unit coverage:
     - `internal/control/estimator_test.go` (`TestStateEstimatorComputesDerivedSignals`, `TestStateEstimatorAppliesSmoothing`, `TestStateEstimatorHandlesMissingData`)
     - `internal/engine/engine_test.go` (`TestEngineReportsStateEstimate`)

3. **[x] FR-15 + FR-20 (Risk-aware policy input quality)**
  - ✅ Implemented estimate-driven control signals in `internal/control` (`RuleController`):
    - New estimate gates in `shouldIncrease(...)`: requires estimate confidence and stability before scale-up.
    - Decrease decisions now prefer smoothed estimate-derived signals:
      - estimate throttle risk (`throttle_risk_score`) in preference to raw throttle risk,
      - max temp slope (`temp_slope_c_per_sec`) floor,
      - throughput trend (`throughput_trend`) drop limit,
      - estimate confidence hold condition.
    - Added new config fields in rule configuration API: `estimate_confidence_min`, `max_temp_slope_c_per_sec`, `min_stability_index_for_increase`, `throughput_trend_drop_limit` (with sane defaults in `defaults()`).
  - Added unit tests in `internal/control/controller_test.go`:
    - `TestRuleController_UsesEstimateConfidenceForHold`
    - `TestRuleController_UsesEstimateToBlockIncrease`
    - `TestRuleController_UsesEstimateRiskAndTrendSignals`
  - Verified behavior via full test matrix:
    - `go test ./...`
    - `go test ./... -tags=integration`
    - `go test ./... -tags=e2e`
    - `go test ./... -tags=regression`

4. **[x] FR-2 (Persisted calibration/session profile integration)**
  - ✅ Implemented stateful checkpoint-backed startup defaults in daemon session startup:
    - Added `StartRequest.InitialBaselineThroughput` and checkpoint restore path in `internal/daemon/daemon.go`.
    - Added checkpoint ingestion helper:
      - `applyStatefulCheckpointDefaults(...)` reads `SessionState` from `checkpoint_path`.
      - Restores start concurrency (clamped to min/max) from checkpointed `state.current_concurrency`.
      - Restores baseline throughput from checkpointed `state.baseline_throughput`.
    - Threaded restored baseline into engine through `StartRequestToEngineConfig(...)` and `internal/engine.Config.InitialBaselineThroughput`.
    - Added `throughput.Tracker.RestoreBaseline(...)` and used in engine startup so restored sessions begin with deterministic baseline.
  - Added tests:
    - `internal/daemon/daemon_test.go` (`TestStatefulSessionAppliesCheckpointDefaults`)
    - `internal/engine/engine_test.go` (`TestEngineRestoresInitialBaseline`)
    - `internal/throughput/throughput_test.go` (`TestTrackerRestoreBaseline`)
  - Verified behavior via full test matrix:
    - `go test ./...`
    - `go test ./... -tags=integration`
    - `go test ./... -tags=e2e`
    - `go test ./... -tags=regression`

## P2 — Calibration, reporting, simulation, and traceability

1. **[x] FR-11 + FR-12 (Calibration mode implementation)**
   - Add one-click calibration flow for target workload + GPU.
   - Compute: baseline throughput, safe concurrency ceiling, thermal saturation curve, VRAM per load unit.
   - Implemented in `cmd/guardian` as `calibrate` command:
     - `runCalibration` performs a bounded concurrency sweep using existing `XTTSAdapter` + `nvidia-smi` telemetry collector.
     - `internal/calibration` computes:
       - `baseline_throughput`
       - `safe_concurrency_ceiling`
       - `thermal_saturation_curve`
       - `vram_per_load_unit_mb`
     - Outputs profile JSON to optional `--output` path (console always prints JSON).

2. **[x] FR-13 (Profile persistence)**
   - Implemented: profiles are now persisted by GPU UUID + workload type.
   - Implemented:
     - Calibration writes profile payload to `--profile-path` (default `.guardian-profiles.json`).
     - Control resolves active profile from telemetry GPU UUID and applies:
       - `safe_concurrency_ceiling` -> `start_concurrency` when not explicitly set
       - `baseline_throughput` -> engine initial baseline
   - Added tests:
     - `internal/calibration/store_test.go` (`TestLoadProfileReturnsMissingForNoStore`, `TestSaveAndLoadProfileByGPUAndWorkload`, `TestSaveProfileDefaultsUnknownKeys`)
     - `internal/telemetry/telemetry_test.go` (UUID parsing + fallback validation updates)
     - `cmd/guardian/main_test.go` (`TestControlLoadsProfileDefaults`)

3. **[x] FR-21 + FR-22 (Action/telemetry/decision logging + reports)**
   - [x] Added `internal/report` with `SessionReport`, `ThermalProfile`, and `RecoveryMetrics`.
   - [x] Added report generation from structured logs via `report.Generate(...)` with defaults for throughput floor.
   - [x] Added CLI entrypoint `guardian report` with flags:
     - `--control-log`
     - `--telemetry-log`
     - `--throughput-floor-ratio`
     - `--output`
   - [x] Added unit tests:
     - `internal/report/report_test.go` (`TestGenerateReportFromControlLog`, `TestGenerateReportFromTelemetryFallback`)
   - [x] Added integration test:
     - `cmd/guardian/main_test.go` (`TestReportCommand`)

4. **[x] FR-23 + FR-24 (Replay and what-if mode)**
   - Add offline replay mode using recorded telemetry.
   - Add deterministic policy what-if runs without GPU hardware.
   - Implemented as `guardian simulate` command.
   - Added `internal/simulation` replay engine with telemetry/control replay ingestion and deterministic policy replay.
 - Added unit tests in `internal/simulation/replay_test.go`.
 - Added integration test for CLI in `cmd/guardian/main_test.go` (`TestSimulateCommand`).

## P3 — Runtime usability and CLI command parity

1. **[x] CLI-2 (`observe` / `control` / `calibrate` / `simulate` / `report`)**
   - Expand CLI commands and argument schemas to match SRS intent.
   - Ensure output format is consistent for human + machine consumption.
   - Added `observe` command with both single-session and full-session inspection modes.
   - `observe` defaults to fetching `/v1/sessions/default`, and supports `--all` to list all sessions.
   - Added unit/integration coverage in `cmd/guardian/main_test.go`:
     - `TestObserveCommandReadsSessionJSON`
     - `TestObserveCommandFailsWithoutDaemon`

2. **[ ] DR-2/4 + CLI consistency (session and telemetry status contract)**
   - Return aligned field names across CLI and API responses (state, policy version, last action, confidence).

3. **[ ] FR-20 + FR-21 UX hooks (operator visibility)**
   - Add explainability in outputs: why action was taken and what signal triggered it.

## P4 — Cross-ecosystem distribution and clients

1. **[ ] NPM-1 + NPM-2 + NPM-3**
   - Implement npm package wrapper that does not require Go at install/runtime.
   - OS/arch detection + binary fetch/install strategy.
   - CLI passthrough and optional JS client API.

2. **[ ] PIP-1 + PIP-2 + PIP-3**
   - Implement pip package with binary packaging/fetch path.
   - Include CLI entry point and Python API client.

3. **[ ] CLI-1 + DR-3 (ecosystem clients integration policy)**
   - Clients communicate to daemon on localhost by default using same API contract.

## P5 — Non-functional, reliability, and release hardening

1. **[ ] Performance (NFR-8.1)**
   - Telemetry overhead ≤ 1%.
   - Typical decision latency ≤ 50ms.
   - Add benchmark tests + measurement gating.

2. **[ ] Reliability (NFR-8.2)**
   - Harden for long-duration stability (72h target).
   - Implement recovery logic for transient telemetry/process failures.

3. **[ ] Safety (NFR-8.3)**
   - Enforce hard temperature ceiling and VRAM ceiling in regulator.
   - Add hard-stop invariants and assertion checks.

4. **[ ] Security + privacy (NFR-8.4)**
   - Keep local-only defaults.
   - No outbound telemetry by default.
   - Optional auth when binding outside localhost.

5. **[ ] Portability + build/delivery (Section 8.5, DIST-1..4)**
   - Linux-first support with optional Windows later.
   - Multi-arch/multi-platform binaries produced by CI.
   - No runtime Go dependency for artifacts.
   - Semantic versioning and release automation.

6. **[ ] Success criteria gates (Section 11)**
   - No sustained 4–5× slowdown.
   - ≥ 95% runtime above throughput floor.
   - Zero thermal ceiling violations.
   - Stable daemon API.
   - npm + pip usability without Go.

## P6 — Optional advanced extensions (from future section)

1. Multi-GPU support.
2. Adapter ecosystem growth.
3. Kubernetes integration.
4. WASI support.
5. Predictive thermal modeling.
6. Cluster/aggregate guardian.
7. FR-25/FR-26 RL policy experiment path behind hard guardrails.

## Implementation order (one item at a time)

1. P0-1, P0-2: Lock engine and adapter contracts.
2. P0-3: Stand up daemon/API and API versioning.
3. P0-4, P0-5, P0-6: Implement deterministic state engine + policy loop + anti-oscillation.
4. P1-1, P1-2: Complete telemetry schema + estimator outputs.
5. P2-1, P2-2: Add calibration mode and profile persistence.
6. P2-3, P2-4: Add reports and simulation/replay mode.
7. P3 and then P4: CLI parity and distribution packages.
8. P5 + release gates: hardening, performance, safety, and success criteria.

## Tracking convention

- Mark each item as `[x]` only after end-to-end validation in the codebase state.
- For each future session, continue from the first unchecked item.
- Any new architecture or API change should be appended with a requirement mapping line pointing to the relevant FR/DR/CLI/NPM/PIP/NFR/DIST item.
