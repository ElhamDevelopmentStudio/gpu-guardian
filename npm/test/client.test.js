const assert = require("node:assert");
const http = require("node:http");
const fs = require("node:fs");
const path = require("node:path");

const {
  GuardianClient,
  DEFAULT_DAEMON_BASE_URL,
} = require("../lib/client");

const contract = JSON.parse(
  fs.readFileSync(
    path.join(__dirname, "..", "..", "ecosystem_client_api_contract.json"),
    "utf8"
  )
);

const api = contract.daemon_api;
function contractPath(name, vars = {}) {
  return Object.entries(vars).reduce(
    (value, [key, next]) => value.replace(`{${key}}`, String(next)),
    api.routes[name].path
  );
}

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
  let requiredAuthHeader = "";

  const { server, port } = await createServer(routes, ({ req, res, payload }) => {
    if (
      requiredAuthHeader &&
      req.headers["authorization"] !== requiredAuthHeader
    ) {
      res.writeHead(401, { "Content-Type": "application/json" });
      res.end(JSON.stringify({ error: "unauthorized" }));
      return;
    }

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
    DEFAULT_DAEMON_BASE_URL === api.default_base_url,
    `expected default base URL ${api.default_base_url}`
  );
  assert.ok(
    DEFAULT_DAEMON_BASE_URL.endsWith(`/${api.api_version}`),
    "default base URL should target v1 API"
  );

  process.env.GUARDIAN_DAEMON_BASE_URL = `http://127.0.0.1:${port}/v1`;
  const envClient = new GuardianClient();
  assert.strictEqual(
    envClient.baseUrl,
    process.env.GUARDIAN_DAEMON_BASE_URL,
    "environment override should be honored"
  );

  assert.strictEqual(
    api.routes.health.path,
    "/v1/health",
    "contract must require health route under v1"
  );
  assert.strictEqual(
    api.routes.control.method,
    "POST",
    "contract should require POST for control"
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
  assert.strictEqual(lastCall.url, api.routes.start_session.path);
  assert.strictEqual(lastCall.method, api.routes.start_session.method);

  await client.stopSession();
  assert.deepStrictEqual(lastCall, {
    url: contractPath("stop_session", { session: "default" }),
    method: "POST",
    payload: {},
  });
  assert.strictEqual(lastCall.url, contractPath("stop_session", { session: "default" }));
  assert.strictEqual(lastCall.method, api.routes.stop_session.method);

  await client.control("stop");
  assert.deepStrictEqual(lastCall, {
    url: "/v1/control",
    method: "POST",
    payload: { action: "stop" },
  });
  assert.strictEqual(lastCall.url, api.routes.control.path);
  assert.strictEqual(lastCall.method, api.routes.control.method);
  assert.strictEqual(
    api.routes.metrics.path,
    `/${api.api_version}/metrics`
  );

  const apiToken = "client-secret";
  requiredAuthHeader = `Bearer ${apiToken}`;
  process.env.GUARDIAN_DAEMON_API_TOKEN = apiToken;
  const authClient = new GuardianClient({ baseUrl: `http://127.0.0.1:${port}/v1` });
  const authenticatedHealth = await authClient.health();
  assert.deepStrictEqual(authenticatedHealth, { status: "ok" });
  delete process.env.GUARDIAN_DAEMON_API_TOKEN;
  requiredAuthHeader = "";

  delete process.env.GUARDIAN_DAEMON_BASE_URL;

  server.close();
  console.log("ok - guardian js client");
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
