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

1. **[ ] FR-1 (Go canonical engine)**
   - Implement/confirm engine package boundaries and entrypoint contracts in Go as the single source of truth.
   - Deliverable: `internal/engine` with explicit state transitions and policy orchestration.

2. **[ ] FR-10 + FR-8 + FR-9 (Versioned adapter contract + XTTS adapter in v1)**
   - Define adapter interface with: `Start`, `Pause`, `Resume`, `UpdateParameters`, `GetThroughput`, `GetProgress`.
   - Implement stable XTTS adapter that satisfies v1 of this interface.
   - Deliverable: interface version tag and adapter compatibility checks.

3. **[ ] DR-1 + DR-2 + DR-3 + DR-4 + DR-5 + CLI-1 + CLI-3 (Daemon/API + versioning + CLI route selection)**
   - Local daemon process with versioned API namespace.
   - Endpoints at minimum: health, metrics, start/stop/control, session/telemetry/query.
   - Default localhost binding.
   - CLI automatically uses daemon when available; falls back to standalone mode only if daemon unavailable.
   - Deliverable: service contract with compatibility guarantees inside one major version line.

4. **[ ] FR-2 (Session/state modes + FR-19 recovery-safe lifecycle)**
   - Add explicit session model (`state`, `goal`, `last_action`, `last_sample`, `retries`, `errors`, checkpoints).
   - Include stateless/ephemeral mode and persisted session/profile mode.
   - Implement pause/safe-stop behavior when recovery fails by policy.

5. **[ ] FR-14 + FR-15 + FR-16 + FR-20 (Engine control behavior)**
   - Run policy loop at configurable frequency.
   - Base decisions on thermal + throughput + memory + throttle risk inputs.
   - Support control adjustments for concurrency and related levers (batch/chunk when available).
   - Enforce anti-oscillation at engine level (minimum dwell, hysteresis, bounded deltas).

6. **[ ] FR-17 + FR-18 + FR-19 (Throughput floor + recovery fallback)**
   - Implement configurable throughput floor logic (70–80% baseline OR ≤2× sustained slowdown fallback).
   - Aggressive reduction/recovery path after floor violation beyond grace window.
   - Safe job pause + preserve state if repeated recovery fails.

## P1 — Telemetry and state estimation completeness

1. **[ ] FR-3 + FR-4 + FR-5 (Telemetry breadth, persistence, resilience)**
   - Ensure collection includes: temperature, utilization, VRAM used/total, power, clocks, throttle indicators.
   - Persist timestamped telemetry samples.
   - Missing fields must be non-fatal; degrade policy and logs safely.

2. **[ ] FR-6 + FR-7 (Derived state estimation)**
   - Add: dT/dt, throughput trend, throttle risk score, stability index.
   - Add temporal smoothing to avoid control churn.

3. **[ ] FR-15 + FR-20 (Risk-aware policy input quality)**
   - Ensure policy decisions are driven by estimates, not raw spikes.
   - Apply hysteresis and cooldown logic based on estimated state and confidence.

4. **[ ] FR-2 (Persisted calibration/session profile integration)**
   - Move startup and checkpoint defaults to profile-backed initialization.
   - Ensure behavior is deterministic across restarts.

## P2 — Calibration, reporting, simulation, and traceability

1. **[ ] FR-11 + FR-12 (Calibration mode implementation)**
   - Add one-click calibration flow for target workload + GPU.
   - Compute: baseline throughput, safe concurrency ceiling, thermal saturation curve, VRAM per load unit.

2. **[ ] FR-13 (Profile persistence)**
   - Persist profiles keyed by `gpu_id + workload_type`.
   - Load profile automatically on next session start.

3. **[ ] FR-21 + FR-22 (Action/telemetry/decision logging + reports)**
   - Enforce structured logs for telemetry, actions, parameter updates, throughput, decisions.
   - Generate session report with: worst slowdown, time-below-floor, thermal profile, recovery metrics.

4. **[ ] FR-23 + FR-24 (Replay and what-if mode)**
   - Add offline replay mode using recorded telemetry.
   - Add deterministic policy what-if runs without GPU hardware.

## P3 — Runtime usability and CLI command parity

1. **[ ] CLI-2 (`observe` / `control` / `calibrate` / `simulate` / `report`)**
   - Expand CLI commands and argument schemas to match SRS intent.
   - Ensure output format is consistent for human + machine consumption.

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
