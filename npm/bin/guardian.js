#!/usr/bin/env node

const path = require("node:path");
const { spawnSync } = require("node:child_process");
const { resolveBinaryPath } = require("../lib/resolve");

async function main() {
  const binary = await resolveBinaryPath();
  const result = spawnSync(
    path.normalize(binary),
    process.argv.slice(2),
    {
      stdio: "inherit",
    }
  );

  if (result.error) {
    console.error(result.error.message);
    process.exitCode = 1;
    return;
  }
  process.exitCode = result.status ?? 0;
}

main().catch((err) => {
  console.error("guardian wrapper failed:", err.message);
  process.exit(1);
});
