const fs = require("node:fs");
const path = require("node:path");
const os = require("node:os");
const { spawnSync } = require("node:child_process");
const { URL } = require("node:url");
const https = require("node:https");
const http = require("node:http");

const { version } = require("../package.json");
const VERSION = `v${version}`;
const FILE_NAME = os.platform() === "win32" ? "guardian.exe" : "guardian";

function normalizeArch(arch) {
  switch (arch) {
    case "x64":
      return "amd64";
    case "ia32":
    case "x86":
      return "386";
    default:
      return arch;
  }
}

function isExecutable(file) {
  try {
    fs.accessSync(file, fs.constants.F_OK);
    if (os.platform() === "win32") {
      return true;
    }
    fs.accessSync(file, fs.constants.X_OK);
    return true;
  } catch {
    return false;
  }
}

function candidatePaths() {
  const base = path.resolve(__dirname, "..");
  const platform = `${os.platform()}-${normalizeArch(os.arch())}`;
  const embedded = path.join(base, "binaries", platform, FILE_NAME);
  return [embedded];
}

function resolveFromPath() {
  const finder = os.platform() === "win32" ? "where" : "which";
  const out = spawnSync(finder, ["guardian"], { encoding: "utf8" });
  if (out.status === 0 && out.stdout && out.stdout.trim()) {
    return out.stdout.split(/\r?\n/)[0].trim();
  }
  return null;
}

function binaryUrls(versionTag) {
  const platform = os.platform();
  const arch = normalizeArch(os.arch());
  const override = process.env.GUARDIAN_BINARY_URL;
  if (override) {
    return [override];
  }

  const tag = versionTag || VERSION;
  const file = `${platform}-${arch}`;
  const root = `https://github.com/elhamdev/gpu-guardian/releases/download/${tag}`;
  const candidates = [`${root}/guardian-${file}`, `${root}/guardian-${file}.exe`];
  return candidates.filter((url) =>
    os.platform() === "win32" ? url.endsWith(".exe") : !url.endsWith(".exe")
  );
}

async function downloadBinary(url, destPath) {
  const parsed = new URL(url);
  const transport = parsed.protocol === "http:" ? http : https;

  return new Promise((resolve, reject) => {
    const req = transport.get(url, (resp) => {
      if (resp.statusCode === 302 || resp.statusCode === 301) {
        return resolve(downloadBinary(resp.headers.location, destPath));
      }
      if (resp.statusCode !== 200) {
        resp.resume();
        return reject(new Error(`binary download failed (${resp.statusCode}): ${url}`));
      }

      fs.mkdirSync(path.dirname(destPath), { recursive: true });
      const out = fs.createWriteStream(destPath, {
        mode: os.platform() === "win32" ? 0o644 : 0o755,
      });
      resp.pipe(out);

      out.on("finish", () => {
        out.close();
        if (os.platform() !== "win32") {
          fs.chmodSync(destPath, 0o755);
        }
        resolve(destPath);
      });
      out.on("error", (err) => {
        out.close();
        try {
          fs.unlinkSync(destPath);
        } catch {
          // best effort cleanup
        }
        reject(err);
      });
    });
    req.on("error", (err) => {
      try {
        fs.unlinkSync(destPath);
      } catch {
        // best effort cleanup
      }
      reject(err);
    });
  });
}

async function resolveBinaryPath() {
  const envPath = process.env.GUARDIAN_BIN_PATH;
  if (envPath) {
    const explicit = path.resolve(envPath);
    if (isExecutable(explicit)) {
      return explicit;
    }
  }

  for (const candidate of candidatePaths()) {
    if (isExecutable(candidate)) {
      return candidate;
    }
  }

  const local = resolveFromPath();
  if (local && isExecutable(local)) {
    return local;
  }

  if (process.env.GUARDIAN_SKIP_DOWNLOAD === "1") {
    throw new Error(
      "GUARDIAN_BIN_PATH was not set and no embedded/local binary is available"
    );
  }

  const target = path.join(
    __dirname,
    "..",
    "binaries",
    `${os.platform()}-${normalizeArch(os.arch())}`,
    FILE_NAME
  );
  const releaseTag = process.env.GUARDIAN_RELEASE_VERSION || VERSION;
  const urls = binaryUrls(releaseTag);
  let lastErr;
  for (const url of urls) {
    try {
      return await downloadBinary(url, target);
    } catch (err) {
      lastErr = err;
    }
  }
  throw lastErr || new Error("unable to resolve guardian binary");
}

module.exports = {
  resolveBinaryPath,
  candidatePaths,
  binaryUrls,
  isExecutable,
  normalizeArch,
};
