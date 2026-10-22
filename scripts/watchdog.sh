#!/usr/bin/env bash
# Keep the local app and quick tunnel alive; secrets are sourced only from vault files.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUN_DIR="${RUN_DIR:-$HOME/sdrstream-run}"
STATE_DIR="${SDRSTREAM_STATE_DIR:-$HOME/sdrstream-state}"
: "${SDRSTREAM_UI_PASSWORD:=$(cat "$HOME/.vault/sdrstream-demo-ui-94f577/password")}" 
: "${SDRSTREAM_NIM_API_KEY:=$(cat "$HOME/.vault/nvidia-nim-api-key-4ab10a/password")}" 
export SDRSTREAM_UI_PASSWORD SDRSTREAM_NIM_API_KEY SDRSTREAM_STATE_DIR="$STATE_DIR"
mkdir -p "$RUN_DIR" "$STATE_DIR"
while :; do
  if ! "$here/run.sh" status >/dev/null 2>&1; then
    "$here/run.sh" stop >>"$RUN_DIR/watchdog.log" 2>&1 || true
    "$here/run.sh" start >>"$RUN_DIR/watchdog.log" 2>&1 || true
  fi
  sleep 20
done
