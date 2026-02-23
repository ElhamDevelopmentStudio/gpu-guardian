# @elhamdev/gpu-guardian

Node wrapper for the `gpu-guardian` binary.

The package exposes a `guardian` binary that delegates to the local `gpu-guardian`
executable and includes a small JS client for daemon communication.

## Install

```bash
npm install @elhamdev/gpu-guardian
```

If the native binary is not already present, the wrapper resolves and downloads
it from GitHub releases on first invocation.

## Usage

```bash
guardian control --cmd "python generate_xtts.py"
guardian daemon
guardian observe --session-id default --telemetry
```

## Environment variables

- `GUARDIAN_BIN_PATH`: explicit absolute/relative path to a local `guardian` binary.
- `GUARDIAN_RELEASE_VERSION`: override release version used for binary download.
- `GUARDIAN_SKIP_DOWNLOAD`: set to `1` to prevent automatic downloads.
- `GUARDIAN_BINARY_URL`: override exact binary URL.
- `GUARDIAN_DAEMON_BASE_URL`: override daemon base URL for the JS client
  (default: `http://127.0.0.1:8090/v1`).

Clients use the shared wrapper contract from `ecosystem_client_api_contract.json`:
`/v1/health`, `/v1/metrics`, `/v1/sessions`, `/v1/control`.

## JS API

```js
const { GuardianClient } = require("@elhamdev/gpu-guardian");

const client = new GuardianClient();
const health = await client.health();
```

```js
const { GuardianClient } = require("@elhamdev/gpu-guardian");

(async () => {
  const client = new GuardianClient();
  const sessions = await client.listSessions();
  console.log(sessions);
})();
```
