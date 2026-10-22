#!/usr/bin/env bash
# Keep SDRStream and its quick tunnel healthy. App restarts only when local app is down.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUN_DIR="${RUN_DIR:-$HOME/sdrstream-run}"
STATE_DIR="${SDRSTREAM_STATE_DIR:-$HOME/sdrstream-state}"
CLOUDFLARED="${CLOUDFLARED:-$HOME/toolchain/cloudflared}"
: "${SDRSTREAM_UI_PASSWORD:=$(cat "$HOME/.vault/sdrstream-demo-ui-94f577/password")}" 
: "${SDRSTREAM_NIM_API_KEY:=$(cat "$HOME/.vault/nvidia-nim-api-key-4ab10a/password")}" 
EXTRA_ARGS="${EXTRA_ARGS:---scan-start-connections 12 --scan-max-connections 12 --scan-ramp 12 --scan-open-directory --scan-hf-server https://websdr.ewi.utwente.nl:8901 --scan-wspr}"
export SDRSTREAM_UI_PASSWORD SDRSTREAM_NIM_API_KEY SDRSTREAM_STATE_DIR="$STATE_DIR" EXTRA_ARGS RUN_DIR
mkdir -p "$RUN_DIR" "$STATE_DIR"
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
