#!/usr/bin/env bash
# Install the iPhone remote-control dependency into the skill's own runtime venv.
# uv owns the interpreter; the venv never points at Homebrew's Python.
set -euo pipefail
SKILL="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if [[ ! -x "$SKILL/.runtime/bin/python" ]]; then
  uv venv --python 3.13 "$SKILL/.runtime"
fi
uv pip install --python "$SKILL/.runtime/bin/python" --quiet 'pymobiledevice3==11.21.0'
"$SKILL/.runtime/bin/python" -c 'from importlib.metadata import version; assert version("pymobiledevice3")=="11.21.0"; print("Remote dependency ready: pymobiledevice3 11.21.0")'
