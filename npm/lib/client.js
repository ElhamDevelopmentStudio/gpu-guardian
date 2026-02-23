const http = require("node:http");
const https = require("node:https");
const { URL } = require("node:url");

const DEFAULT_DAEMON_BASE_URL = "http://127.0.0.1:8090/v1";

class GuardianClient {
  constructor(options = {}) {
    this.baseUrl =
      options.baseUrl ||
      process.env.GUARDIAN_DAEMON_BASE_URL ||
      DEFAULT_DAEMON_BASE_URL;
    this.timeoutMs = options.timeoutMs || 5000;
  }

  async _request(method, pathname, body) {
    const target = this.baseUrl.replace(/\/$/, "") + "/" + pathname.replace(/^\//, "");
    const url = new URL(target);
    const transport = url.protocol === "http:" ? http : https;

    const payload =
      body === undefined ? null : JSON.stringify(body);
    const headers = {};
    if (payload !== null) {
      headers["content-type"] = "application/json";
      headers["content-length"] = Buffer.byteLength(payload);
    }

    return new Promise((resolve, reject) => {
      const req = transport.request(
        {
          method,
          protocol: url.protocol,
          hostname: url.hostname,
          port: url.port,
          path: url.pathname,
          headers,
        },
        (resp) => {
          let raw = "";
          resp.on("data", (chunk) => {
            raw += chunk;
          });
          resp.on("end", () => {
            if (resp.statusCode < 200 || resp.statusCode >= 300) {
              const err = new Error(
                `daemon request failed: ${resp.statusCode} ${resp.statusMessage}`
              );
              err.statusCode = resp.statusCode;
              err.body = raw;
              return reject(err);
            }
            if (!raw) {
              return resolve({});
            }
            try {
              resolve(JSON.parse(raw));
            } catch (parseErr) {
              parseErr.message = `invalid JSON response from daemon: ${parseErr.message}`;
              reject(parseErr);
            }
          });
        }
      );
      req.on("timeout", () => {
        req.destroy();
        reject(new Error("daemon request timeout"));
      });
      req.on("error", reject);
      if (payload !== null) {
        req.write(payload);
      }
      req.end();
      req.setTimeout(this.timeoutMs);
    });
  }

  async health() {
    return this._request("GET", "/health");
  }

  async metrics() {
    return this._request("GET", "/metrics");
  }

  async listSessions() {
    return this._request("GET", "/sessions");
  }

  async getSession(id = "default") {
    return this._request("GET", `/sessions/${encodeURIComponent(id)}`);
  }

  async getSessionTelemetry(id = "default") {
    return this._request(
      "GET",
      `/sessions/${encodeURIComponent(id)}/telemetry`
    );
  }

  async startSession(payload) {
    return this._request("POST", "/sessions", payload || {});
  }

  async stopSession(id = "default") {
    return this._request("POST", `/sessions/${encodeURIComponent(id)}/stop`);
  }

  async control(action) {
    return this._request("POST", "/control", { action });
  }
}

module.exports = {
  GuardianClient,
  DEFAULT_DAEMON_BASE_URL,
};
