#!/usr/bin/env bash
# Install the iPhone remote-control dependency into iph's own venv in its state dir,
# so the install dir stays read-only. uv owns the interpreter; the venv never points
# at Homebrew's Python.
set -euo pipefail
VENV="${IPH_STATE:-$HOME/.cache/iphone-ops}/remote-venv"

if [[ ! -x "$VENV/bin/python" ]]; then
  uv venv --python 3.13 "$VENV"
fi
uv pip install --python "$VENV/bin/python" --quiet 'pymobiledevice3==11.21.0'
"$VENV/bin/python" -c 'from importlib.metadata import version; assert version("pymobiledevice3")=="11.21.0"; print("Remote dependency ready: pymobiledevice3 11.21.0")'
