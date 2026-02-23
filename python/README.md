# gpu-guardian Python wrapper

Package skeleton for publishing gpu-guardian as a pip wrapper.

## Install

```bash
pip install .
```

## Usage

```bash
guardian --help
guardian control --cmd "python generate_xtts.py"
```

## Environment variables

- `GUARDIAN_BIN_PATH`: explicit binary path
- `GUARDIAN_RELEASE_VERSION`: release tag override
- `GUARDIAN_SKIP_DOWNLOAD`: set to `1` to avoid automatic download
- `GUARDIAN_BINARY_URL`: explicit binary URL override
- `GUARDIAN_DAEMON_BASE_URL`: daemon base URL override (defaults to `http://127.0.0.1:8090/v1`)

## Python API

```python
from gpu_guardian import GuardianClient

client = GuardianClient()
status = client.health()
```
