#!/usr/bin/env bash
# Keep SDRStream and its quick tunnel healthy. App restarts only when local app is down.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUN_DIR="${RUN_DIR:-$HOME/sdrstream-run}"
STATE_DIR="${SDRSTREAM_STATE_DIR:-$HOME/sdrstream-state}"
CLOUDFLARED="${CLOUDFLARED:-$HOME/toolchain/cloudflared}"
# Secrets come from the environment or from vault files (written by `tools vault write-file`).
UI_PASSWORD_FILE="${SDRSTREAM_UI_PASSWORD_FILE:-$HOME/.vault/sdrstream-demo-ui-94f577/password}"
NIM_KEY_FILE="${SDRSTREAM_NIM_API_KEY_FILE:-$HOME/.vault/nvidia-nim-api-key-4ab10a/password}"
need_secret() { # var file
  local var="$1" file="$2"
  [ -n "${!var:-}" ] && return 0
  if [ ! -r "$file" ]; then
    echo "watchdog: $var is not set and its vault file is missing or unreadable: $file" >&2
    echo "watchdog: export $var, or recreate the file (vault write-file) and set ${var}_FILE if the path differs." >&2
    exit 2
  fi
  printf -v "$var" '%s' "$(cat "$file")"
}
need_secret SDRSTREAM_UI_PASSWORD "$UI_PASSWORD_FILE"
need_secret SDRSTREAM_NIM_API_KEY "$NIM_KEY_FILE"
EXTRA_ARGS="${EXTRA_ARGS:---scan-start-connections 12 --scan-max-connections 12 --scan-ramp 12 --scan-open-directory --scan-hf-server http://websdr.ewi.utwente.nl:8901,http://websdr.fi:8080,f4kiy.ddns.net:8073 --scan-wspr}"
export SDRSTREAM_UI_PASSWORD SDRSTREAM_NIM_API_KEY SDRSTREAM_STATE_DIR="$STATE_DIR" EXTRA_ARGS RUN_DIR
mkdir -p "$RUN_DIR" "$STATE_DIR"
# Single instance: a second watchdog would race on app.pid / the tunnel.
exec 9>"$RUN_DIR/watchdog.lock"
if ! flock -n 9; then
  echo "watchdog: another instance already holds $RUN_DIR/watchdog.lock; exiting" >&2
  exit 3
fi
echo $$ >"$RUN_DIR/watchdog.pid"
trap 'rm -f "$RUN_DIR/watchdog.pid"' EXIT
app_up() {
  local pid code
  if [ ! -f "$RUN_DIR/app.pid" ]; then return 1; fi
  pid="$(cat "$RUN_DIR/app.pid")"
  kill -0 "$pid" 2>/dev/null || return 1
  code="$(curl -s --max-time 3 -o /dev/null -w '%{http_code}' "http://127.0.0.1:${PORT:-8000}/login" || true)"
  [ "$code" = 200 ]
}
start_tunnel() {
  : >"$RUN_DIR/tunnel.log"
  nohup "$CLOUDFLARED" tunnel --no-autoupdate --url "http://127.0.0.1:${PORT:-8000}" >"$RUN_DIR/tunnel.log" 2>&1 &
  echo $! >"$RUN_DIR/tunnel.pid"
  for _ in $(seq 1 30); do
    url="$(grep -o 'https://[a-z0-9-]*\.trycloudflare\.com' "$RUN_DIR/tunnel.log" 2>/dev/null | head -1 || true)"
    if [ -n "$url" ]; then
      printf '%s\n' "$url" >"$RUN_DIR/tunnel.url.tmp" && mv "$RUN_DIR/tunnel.url.tmp" "$RUN_DIR/tunnel.url" && chmod 600 "$RUN_DIR/tunnel.url"
      return 0
    fi
    sleep 1
  done
  return 1
}
while :; do
  if ! app_up; then
    "$here/run.sh" start >>"$RUN_DIR/watchdog.log" 2>&1 || true
  fi
  # Avoid rotating a working URL on a single transient failure.
  failures=0
  for _ in 1 2 3; do
    url="$(cat "$RUN_DIR/tunnel.url" 2>/dev/null || true)"
    if [ -z "$url" ]; then
      url="$(grep -o 'https://[a-z0-9-]*\.trycloudflare\.com' "$RUN_DIR/tunnel.log" 2>/dev/null | tail -1 || true)"
    fi
    if [ -n "$url" ] && curl -fsS --max-time 8 -o /dev/null "$url/login"; then
      failures=0; break
    fi
    failures=$((failures + 1))
    [ "$failures" -lt 3 ] && sleep 20
  done
  if [ "$failures" -ge 3 ]; then
    old="$(cat "$RUN_DIR/tunnel.url" 2>/dev/null || true)"
    if [ -f "$RUN_DIR/tunnel.pid" ]; then
      pid="$(cat "$RUN_DIR/tunnel.pid")"
      kill -0 "$pid" 2>/dev/null && kill -TERM "$pid" || true
      for _ in $(seq 1 10); do kill -0 "$pid" 2>/dev/null || break; sleep 1; done
      kill -0 "$pid" 2>/dev/null && kill -KILL "$pid" || true
    fi
    start_tunnel || true
    new="$(cat "$RUN_DIR/tunnel.url" 2>/dev/null || true)"
    if [ -n "$new" ] && [ "$new" != "$old" ]; then
      printf '%s %s\n' "$(date -Is)" "$new" >>"$RUN_DIR/tunnel-url-changes.log"
    fi
  fi
  sleep 20
done
