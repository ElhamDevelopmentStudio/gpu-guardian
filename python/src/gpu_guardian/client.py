import json
import os
from urllib import error, request

DEFAULT_DAEMON_BASE_URL = "http://127.0.0.1:8090/v1"
DEFAULT_TIMEOUT_SECONDS = 5.0


class ClientError(RuntimeError):
  pass


class GuardianClient:
  def __init__(self, base_url=None, timeout_seconds=DEFAULT_TIMEOUT_SECONDS):
    self.base_url = (
      base_url
      or os.environ.get("GUARDIAN_DAEMON_BASE_URL")
      or DEFAULT_DAEMON_BASE_URL
    )
    self.timeout_seconds = timeout_seconds

  def _request(self, method, path, payload=None):
    if not self.base_url:
      raise ClientError("base URL is required")
    normalized_base = self.base_url.rstrip("/")
    normalized_path = path.lstrip("/")
    url = f"{normalized_base}/{normalized_path}"

    data = None
    headers = {}
    if payload is not None:
      data = json.dumps(payload).encode("utf-8")
      headers["Content-Type"] = "application/json"
      headers["Content-Length"] = str(len(data))

    req = request.Request(
      url,
      data=data,
      headers=headers,
      method=method,
    )

    try:
      with request.urlopen(req, timeout=self.timeout_seconds) as response:
        raw = response.read()
    except error.HTTPError as exc:
      body = exc.read().decode("utf-8", errors="ignore")
      raise ClientError(
        f"daemon request failed: {exc.code} {exc.reason} {body}"
      ) from exc
    except error.URLError as exc:
      raise ClientError(f"daemon request failed: {exc}") from exc

    if not raw:
      return {}
    try:
      return json.loads(raw.decode("utf-8"))
    except json.JSONDecodeError as exc:
      raise ClientError(f"invalid JSON response from daemon: {exc}") from exc

  def health(self):
    return self._request("GET", "/health")

  def metrics(self):
    return self._request("GET", "/metrics")

  def list_sessions(self):
    return self._request("GET", "/sessions")

  def get_session(self, session_id="default"):
    return self._request("GET", f"/sessions/{session_id}")

  def get_session_telemetry(self, session_id="default"):
    return self._request("GET", f"/sessions/{session_id}/telemetry")

  def start_session(self, payload=None):
    return self._request("POST", "/sessions", payload or {})

  def stop_session(self, session_id="default"):
    return self._request("POST", f"/sessions/{session_id}/stop")

  def control(self, action):
    return self._request("POST", "/control", {"action": action})
