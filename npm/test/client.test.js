const assert = require("node:assert");
const http = require("node:http");

const {
  GuardianClient,
  DEFAULT_DAEMON_BASE_URL,
} = require("../lib/client");

function createServer(routes, onRequest) {
  return new Promise((resolve, reject) => {
    const server = http.createServer((req, res) => {
      const bodyHandler = (cb) => {
        let raw = "";
        req.on("data", (chunk) => (raw += chunk));
        req.on("end", () => cb(raw));
      };

      bodyHandler((raw) => {
        let payload = {};
        if (req.headers["content-type"] === "application/json" && raw.length > 0) {
          try {
            payload = JSON.parse(raw);
          } catch {
            payload = { raw };
          }
        }
        onRequest({ req, res, payload: payload === null ? {} : payload });
      });
    });

    server.on("error", reject);
    server.listen(0, () => {
      const address = server.address();
      resolve({ server, port: address.port });
    });
  });
}

async function main() {
  let lastCall = null;
  const routes = new Map();
  routes.set("/v1/health", { method: "GET", status: 200, body: { status: "ok" } });
  routes.set("/v1/metrics", { method: "GET", status: 200, body: { sessions_total: 0 } });
  routes.set("/v1/sessions", { method: "GET", status: 200, body: [] });
  routes.set("/v1/sessions/default", {
    method: "GET",
    status: 200,
    body: { id: "default" },
  });
  routes.set("/v1/sessions/default/telemetry", {
    method: "GET",
    status: 200,
    body: { session_id: "default", session: {} },
  });

  const { server, port } = await createServer(routes, ({ req, res, payload }) => {
    const route = routes.get(req.url);
    if (req.url === "/v1/sessions" && req.method === "POST") {
      lastCall = { url: req.url, method: req.method, payload };
      res.writeHead(200, { "Content-Type": "application/json" });
      res.end(JSON.stringify({ session_id: "default", reason: "started" }));
      return;
    }
    if (req.url === "/v1/sessions/default/stop" && req.method === "POST") {
      lastCall = { url: req.url, method: req.method, payload };
      res.writeHead(200, { "Content-Type": "application/json" });
      res.end(JSON.stringify({ session_id: "default", reason: "stopped" }));
      return;
    }
    if (req.url === "/v1/control" && req.method === "POST") {
      lastCall = { url: req.url, method: req.method, payload };
      res.writeHead(202, { "Content-Type": "application/json" });
      res.end(JSON.stringify({ session_id: "default", reason: "stopped" }));
      return;
    }

    if (!route || route.method !== req.method) {
      res.writeHead(404, { "Content-Type": "application/json" });
      res.end(JSON.stringify({ error: "not found" }));
      return;
    }
    res.writeHead(route.status, { "Content-Type": "application/json" });
    res.end(JSON.stringify(route.body));
  });

  const client = new GuardianClient({ baseUrl: `http://127.0.0.1:${port}/v1` });
  assert.ok(
    DEFAULT_DAEMON_BASE_URL.startsWith("http://127.0.0.1"),
    "expected localhost default base URL"
  );

  const health = await client.health();
  assert.deepStrictEqual(health, { status: "ok" });

  const metrics = await client.metrics();
  assert.deepStrictEqual(metrics, { sessions_total: 0 });

  const sessions = await client.listSessions();
  assert.deepStrictEqual(sessions, []);

  const session = await client.getSession();
  assert.deepStrictEqual(session, { id: "default" });

  const telemetry = await client.getSessionTelemetry();
  assert.deepStrictEqual(telemetry, { session_id: "default", session: {} });

  await client.startSession({ command: "python generate_xtts.py" });
  assert.deepStrictEqual(lastCall, {
    url: "/v1/sessions",
    method: "POST",
    payload: { command: "python generate_xtts.py" },
  });

  await client.stopSession();
  assert.deepStrictEqual(lastCall, {
    url: "/v1/sessions/default/stop",
    method: "POST",
    payload: {},
  });

  await client.control("stop");
  assert.deepStrictEqual(lastCall, {
    url: "/v1/control",
    method: "POST",
    payload: { action: "stop" },
  });

  server.close();
  console.log("ok - guardian js client");
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
