import os
from pathlib import Path
from tempfile import TemporaryDirectory

from gpu_guardian.resolver import resolve_binary_path


def run(name, fn):
  try:
    fn()
    print(f"ok - {name}")
  except Exception as err:
    print(f"fail - {name}")
    raise


def test_resolve_uses_explicit_bin_path():
  with TemporaryDirectory() as tmpdir:
    bin_name = "guardian.exe" if os.name == "nt" else "guardian"
    binary = Path(tmpdir) / bin_name
    binary.write_bytes(b"#!/bin/sh\necho hello\n")
    binary.chmod(0o755)

    original_path = os.environ.get("GUARDIAN_BIN_PATH")
    original_skip = os.environ.get("GUARDIAN_SKIP_DOWNLOAD")
    os.environ["GUARDIAN_BIN_PATH"] = str(binary)
    os.environ["GUARDIAN_SKIP_DOWNLOAD"] = "1"
    try:
      resolved = resolve_binary_path()
      assert resolved == str(binary)
    finally:
      if original_path is None:
        del os.environ["GUARDIAN_BIN_PATH"]
      else:
        os.environ["GUARDIAN_BIN_PATH"] = original_path
      if original_skip is None:
        del os.environ["GUARDIAN_SKIP_DOWNLOAD"]
      else:
        os.environ["GUARDIAN_SKIP_DOWNLOAD"] = original_skip


def test_resolve_falls_back_to_path():
  with TemporaryDirectory() as tmpdir:
    bin_name = "guardian.exe" if os.name == "nt" else "guardian"
    binary = Path(tmpdir) / bin_name
    binary.write_bytes(b"#!/bin/sh\necho hello\n")
    binary.chmod(0o755)

    original_path = os.environ.get("PATH")
    original_env = os.environ.get("GUARDIAN_BIN_PATH")
    original_skip = os.environ.get("GUARDIAN_SKIP_DOWNLOAD")
    try:
      os.environ["PATH"] = f"{tmpdir}{os.pathsep}{original_path}"
      os.environ["GUARDIAN_SKIP_DOWNLOAD"] = "1"
      os.environ["GUARDIAN_BIN_PATH"] = ""
      resolved = resolve_binary_path()
      assert resolved == str(binary)
    finally:
      os.environ["PATH"] = original_path
      if original_env is None:
        del os.environ["GUARDIAN_BIN_PATH"]
      else:
        os.environ["GUARDIAN_BIN_PATH"] = original_env
      if original_skip is None:
        del os.environ["GUARDIAN_SKIP_DOWNLOAD"]
      else:
        os.environ["GUARDIAN_SKIP_DOWNLOAD"] = original_skip


def main():
  run("resolve_binary_path uses GUARDIAN_BIN_PATH", test_resolve_uses_explicit_bin_path)
  run("resolve_binary_path falls back to PATH", test_resolve_falls_back_to_path)


if __name__ == "__main__":
  main()
