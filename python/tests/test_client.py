import json
from pathlib import Path
import threading
import os
from http.server import BaseHTTPRequestHandler, HTTPServer

from gpu_guardian.client import DEFAULT_DAEMON_BASE_URL, GuardianClient

CONTRACT_PATH = Path(__file__).resolve().parents[1].parent / "ecosystem_client_api_contract.json"
with open(CONTRACT_PATH, "r", encoding="utf-8") as fp:
    CONTRACT = json.load(fp)


class _Handler(BaseHTTPRequestHandler):
  routes = {}
  last_call = None
  expected_auth = None

  def _is_authorized(self):
    if self.__class__.expected_auth is None:
      return True
    return self.headers.get("Authorization") == self.__class__.expected_auth

  def _send_json(self, status, payload):
    body = json.dumps(payload).encode("utf-8")
    self.send_response(status)
    self.send_header("Content-Type", "application/json")
    self.send_header("Content-Length", str(len(body)))
    self.end_headers()
    self.wfile.write(body)

  def do_GET(self):
    if not self._is_authorized():
      self._send_json(401, {"error": "unauthorized"})
      return
    route = self.routes.get((self.command, self.path))
    if route is None:
      self._send_json(404, {"error": "not found"})
      return
    self._send_json(route["status"], route["body"])

  def do_POST(self):
    if not self._is_authorized():
      self._send_json(401, {"error": "unauthorized"})
      return
    length = int(self.headers.get("Content-Length", "0") or 0)
    raw = self.rfile.read(length).decode("utf-8") if length else "{}"
    payload = {}
    if raw:
      try:
        payload = json.loads(raw)
      except json.JSONDecodeError:
        payload = {"raw": raw}

    if self.path == "/v1/sessions" and self.command == "POST":
      self.__class__.last_call = {"url": self.path, "method": self.command, "payload": payload}
      self._send_json(200, {"session_id": "default", "reason": "started"})
      return
    if self.path == "/v1/sessions/default/stop" and self.command == "POST":
      self.__class__.last_call = {"url": self.path, "method": self.command, "payload": payload}
      self._send_json(200, {"session_id": "default", "reason": "stopped"})
      return
    if self.path == "/v1/control" and self.command == "POST":
      self.__class__.last_call = {"url": self.path, "method": self.command, "payload": payload}
      self._send_json(202, {"session_id": "default", "reason": "stopped"})
      return

    route = self.routes.get((self.command, self.path))
    if route is None:
      self._send_json(404, {"error": "not found"})
      return
    self.__class__.last_call = {"url": self.path, "method": self.command, "payload": payload}
    self._send_json(route["status"], route["body"])

  def log_message(self, format, *args):
    return


def _contract_path(routes, name, **kwargs):
  route_path = routes[name]["path"]
  for key, value in kwargs.items():
    route_path = route_path.replace(f"{{{key}}}", str(value))
  return route_path


def _build_server():
  routes = {
    ("GET", "/v1/health"): {"status": 200, "body": {"status": "ok"}},
    ("GET", "/v1/metrics"): {"status": 200, "body": {"sessions_total": 0}},
    ("GET", "/v1/sessions"): {"status": 200, "body": []},
    ("GET", "/v1/sessions/default"): {"status": 200, "body": {"id": "default"}},
    ("GET", "/v1/sessions/default/telemetry"): {
      "status": 200,
      "body": {"session_id": "default", "session": {}},
    },
  }

  handler = type(
    "Handler",
    (_Handler,),
    {"routes": routes, "last_call": None},
  )

  server = HTTPServer(("127.0.0.1", 0), handler)
  port = server.server_address[1]
  thread = threading.Thread(target=server.serve_forever)
  thread.daemon = True
  thread.start()
  return server, port, handler


def run(name, fn):
  try:
    fn()
    print(f"ok - {name}")
  except Exception:
    print(f"fail - {name}")
    raise


def test_client_contract():
  server, port, handler = _build_server()
  try:
    api = CONTRACT["daemon_api"]
    assert DEFAULT_DAEMON_BASE_URL == api["default_base_url"]

    os.environ["GUARDIAN_DAEMON_BASE_URL"] = f"http://127.0.0.1:{port}/v1"
    env_client = GuardianClient()
    assert env_client.base_url == os.environ["GUARDIAN_DAEMON_BASE_URL"]

    client = GuardianClient(base_url=f"http://127.0.0.1:{port}/v1")

    assert client.health() == {"status": "ok"}
    assert client.metrics() == {"sessions_total": 0}
    assert client.list_sessions() == []
    assert client.get_session() == {"id": "default"}
    assert client.get_session_telemetry() == {"session_id": "default", "session": {}}

    assert client.start_session({"command": "python generate_xtts.py"}) == {
      "session_id": "default",
      "reason": "started",
    }
    assert handler.last_call == {
      "url": _contract_path(api["routes"], "start_session"),
      "method": "POST",
      "payload": {"command": "python generate_xtts.py"},
    }

    assert client.stop_session() == {
      "session_id": "default",
      "reason": "stopped",
    }
    assert handler.last_call == {
      "url": _contract_path(api["routes"], "stop_session", session="default"),
      "method": "POST",
      "payload": {},
    }

    assert client.control("stop") == {"session_id": "default", "reason": "stopped"}
    assert handler.last_call == {
      "url": _contract_path(api["routes"], "control"),
      "method": "POST",
      "payload": {"action": "stop"},
    }
    assert handler.last_call["method"] == api["routes"]["control"]["method"]
    assert api["routes"]["metrics"]["path"] == f"/{api['api_version']}/metrics"
    assert api["routes"]["health"]["method"] == "GET"

    api_token = "client-secret"
    handler.expected_auth = f"Bearer {api_token}"
    os.environ["GUARDIAN_DAEMON_API_TOKEN"] = api_token
    auth_client = GuardianClient(base_url=f"http://127.0.0.1:{port}/v1")
    assert auth_client.health() == {"status": "ok"}
  finally:
    server.shutdown()
    server.server_close()
    os.environ.pop("GUARDIAN_DAEMON_BASE_URL", None)
    os.environ.pop("GUARDIAN_DAEMON_API_TOKEN", None)


def main():
  run("python client default base + daemon methods", test_client_contract)


if __name__ == "__main__":
  main()
