import os
import shutil
import stat
import sys
from pathlib import Path
import platform as platform_module
from urllib import error, request

from . import __version__ as package_version

VERSION = f"v{package_version}"
DEFAULT_BINARY_NAME = "guardian.exe" if os.name == "nt" else "guardian"


def platform_name():
  if sys.platform == "win32":
    return "win32"
  if sys.platform == "darwin":
    return "darwin"
  return "linux"


def normalize_arch(arch):
  mapping = {
    "x86_64": "amd64",
    "AMD64": "amd64",
    "i386": "386",
    "i686": "386",
    "arm64": "arm64",
    "aarch64": "arm64",
  }
  return mapping.get(arch, arch)


def is_executable(file_path):
  try:
    path = Path(file_path)
    if not path.exists():
      return False
    if os.name == "nt":
      return path.is_file()
    return os.access(file_path, os.X_OK)
  except OSError:
    return False


def candidate_paths():
  platform_key = platform_name()
  arch = normalize_arch(platform_module.machine())
  base = Path(__file__).resolve().parent.parent
  return [base / "binaries" / f"{platform_key}-{arch}" / DEFAULT_BINARY_NAME]


def binary_urls(version_tag):
  override = os.environ.get("GUARDIAN_BINARY_URL")
  if override:
    return [override]

  platform_key = platform_name()
  arch = normalize_arch(platform_module.machine())
  release = version_tag or VERSION
  root = (
    f"https://github.com/elhamdev/gpu-guardian/releases/download/{release}"
  )
  filename = f"{platform_key}-{arch}"
  suffixes = ["" if sys.platform != "win32" else ".exe"]
  return [f"{root}/guardian-{filename}{ext}" for ext in suffixes]


def download_binary(url, dest_path):
  if not url.startswith("http://") and not url.startswith("https://"):
    raise ValueError("binary URL must use http or https")

  try:
    with request.urlopen(url) as response:
      if response.status in (301, 302, 303):
        redirect = response.headers.get("Location")
        if not redirect:
          raise RuntimeError(f"redirect without destination: {url}")
        return download_binary(redirect, dest_path)
      if response.status != 200:
        raise RuntimeError(f"binary download failed ({response.status}): {url}")
      dest_path.parent.mkdir(parents=True, exist_ok=True)
      data = response.read()
      dest_path.write_bytes(data)
      if os.name != "nt":
        current = dest_path.stat().st_mode
        dest_path.chmod(current | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)
      return dest_path
  except error.URLError as exc:
    raise RuntimeError(f"binary download failed: {url}") from exc


def resolve_binary_path():
  env_path = os.environ.get("GUARDIAN_BIN_PATH")
  if env_path:
    explicit = str(Path(env_path).expanduser())
    if is_executable(explicit):
      return explicit

  for candidate in candidate_paths():
    if is_executable(candidate):
      return str(candidate)

  local = shutil.which("guardian")
  if local and is_executable(local):
    return local

  if os.environ.get("GUARDIAN_SKIP_DOWNLOAD") == "1":
    raise FileNotFoundError(
      "GUARDIAN_BIN_PATH was not set and no embedded/local binary is available"
    )

  platform_key = platform_name()
  arch = normalize_arch(platform_module.machine())
  cache_root = Path.home() / ".cache" / "gpu-guardian" / "bin"
  target = cache_root / f"{platform_key}-{arch}" / DEFAULT_BINARY_NAME
  release = os.environ.get("GUARDIAN_RELEASE_VERSION", VERSION)
  last_error = None
  for url in binary_urls(release):
    try:
      return str(download_binary(url, target))
    except Exception as exc:
      last_error = exc
  raise last_error or RuntimeError("unable to resolve guardian binary")
