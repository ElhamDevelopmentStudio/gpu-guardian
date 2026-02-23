const assert = require("node:assert");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { mkdtempSync } = require("node:fs");

const {
  resolveBinaryPath,
  isExecutable,
  candidatePaths,
} = require("../lib/resolve");

function run(name, fn) {
  return Promise.resolve()
    .then(fn)
    .then(() => console.log(`ok - ${name}`));
}

async function testResolveBinaryPathPrefersExplicitEnv() {
  const tmpDir = mkdtempSync(path.join(os.tmpdir(), "guardian-node-"));
  const binPath = path.join(tmpDir, "guardian");
  fs.writeFileSync(binPath, "#!/bin/sh\necho explicit\n", { mode: 0o755 });

  const original = process.env.GUARDIAN_BIN_PATH;
  const skipDownload = process.env.GUARDIAN_SKIP_DOWNLOAD;
  try {
    process.env.GUARDIAN_BIN_PATH = binPath;
    process.env.GUARDIAN_SKIP_DOWNLOAD = "1";

    const resolved = await resolveBinaryPath();
    assert.strictEqual(path.resolve(binPath), path.resolve(resolved));
  } finally {
    delete process.env.GUARDIAN_BIN_PATH;
    if (skipDownload === undefined) {
      delete process.env.GUARDIAN_SKIP_DOWNLOAD;
    } else {
      process.env.GUARDIAN_SKIP_DOWNLOAD = skipDownload;
    }
    if (original === undefined) {
      delete process.env.GUARDIAN_BIN_PATH;
    } else {
      process.env.GUARDIAN_BIN_PATH = original;
    }
  }
}

async function testResolveBinaryPathFallsBackToPath() {
  const tmpDir = mkdtempSync(path.join(os.tmpdir(), "guardian-node-"));
  const binPath = path.join(tmpDir, os.platform() === "win32" ? "guardian.exe" : "guardian");
  fs.writeFileSync(binPath, "#!/bin/sh\necho path\n", { mode: 0o755 });
  const originalPath = process.env.PATH;
  const skipDownload = process.env.GUARDIAN_SKIP_DOWNLOAD;
  const explicit = process.env.GUARDIAN_BIN_PATH;

  try {
    process.env.GUARDIAN_BIN_PATH = "";
    process.env.PATH = `${tmpDir}${path.delimiter}${originalPath}`;
    process.env.GUARDIAN_SKIP_DOWNLOAD = "1";

    const candidates = candidatePaths();
    for (const candidate of candidates) {
      if (fs.existsSync(candidate)) {
        fs.unlinkSync(candidate);
      }
    }

    const resolved = await resolveBinaryPath();
    assert.strictEqual(path.resolve(binPath), path.resolve(resolved));
  } finally {
    process.env.PATH = originalPath;
    if (skipDownload === undefined) {
      delete process.env.GUARDIAN_SKIP_DOWNLOAD;
    } else {
      process.env.GUARDIAN_SKIP_DOWNLOAD = skipDownload;
    }
    if (explicit === undefined) {
      delete process.env.GUARDIAN_BIN_PATH;
    } else {
      process.env.GUARDIAN_BIN_PATH = explicit;
    }
  }
}

function testIsExecutableReturnsTrueForReadableFile(filePath) {
  fs.writeFileSync(filePath, "#!/bin/sh\necho test\n", { mode: 0o755 });
  assert.strictEqual(isExecutable(filePath), true);
}

async function main() {
  const tmp = mkdtempSync(path.join(os.tmpdir(), "guardian-node-"));
  const filePath = path.join(tmp, "dummy");

  await run(
    "resolveBinaryPath prefers GUARDIAN_BIN_PATH",
    testResolveBinaryPathPrefersExplicitEnv
  );
  await run("resolveBinaryPath falls back to PATH", testResolveBinaryPathFallsBackToPath);
  testIsExecutableReturnsTrueForReadableFile(filePath);
  console.log("ok - isExecutable on existing local file");

  await Promise.resolve();
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
