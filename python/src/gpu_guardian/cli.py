import os
import subprocess
import sys

from .resolver import resolve_binary_path


def main():
  binary = resolve_binary_path()
  args = [binary] + sys.argv[1:]
  completed = subprocess.run(args, env=os.environ)
  raise SystemExit(completed.returncode)
