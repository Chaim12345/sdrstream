#!/usr/bin/env bash
# Start sdrstream web under nohup plus a cloudflared quick tunnel, then health check.
#   SDRSTREAM_UI_PASSWORD   required (never written to disk or logs)
#   PORT (8000) RUN_DIR ($HOME/sdrstream-run) STATE_DIR ($HOME/sdrstream-state)
#   EXTRA_ARGS (more `sdrstream web` flags)
# Optional env: SDRSTREAM_AIS_KEY, SDRSTREAM_WHISPER, SDRSTREAM_WHISPER_MODEL
# Usage: scripts/run.sh start|stop|status
set -uo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
. "$here/env.sh" || exit 1
PORT="${PORT:-8000}"
RUN_DIR="${RUN_DIR:-$HOME/sdrstream-run}"
STATE_DIR="${SDRSTREAM_STATE_DIR:-$HOME/sdrstream-state}"
CLOUDFLARED="${CLOUDFLARED:-$SDR_TOOLCHAIN/cloudflared}"
mkdir -p "$RUN_DIR" "$RUN_DIR/captures" "$STATE_DIR"
SDRSTREAM_STATE_DIR="$STATE_DIR"
export SDRSTREAM_STATE_DIR
APP_PID="$RUN_DIR/app.pid"; TUN_PID="$RUN_DIR/tunnel.pid"
alive() { [ -f "$1" ] && kill -0 "$(cat "$1")" 2>/dev/null; }

stop_one() { # only stops processes this script started (pid files)
  if alive "$1"; then
    local pid; pid="$(cat "$1")"
    kill -TERM "$pid"
    for _ in $(seq 1 15); do kill -0 "$pid" 2>/dev/null || break; sleep 1; done
    kill -0 "$pid" 2>/dev/null && kill -KILL "$pid" || true
  fi
  rm -f "$1"
}
health() {
  local code
  code="$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/login")"
  [ "$code" = 200 ] || { echo "health: /login returned $code"; return 1; }
  code="$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/api/status.json")"
  [ "$code" = 401 ] || { echo "health: unauthenticated /api/status.json returned $code, expected 401"; return 1; }
  [ -n "${SDRSTREAM_UI_PASSWORD:-}" ] || { echo "health: SDRSTREAM_UI_PASSWORD missing"; return 1; }
  local cookie formcode
  cookie="$RUN_DIR/health.cookie"
  formcode="$(curl -s -c "$cookie" -b "$cookie" -o /dev/null -w '%{http_code}' -X POST --data-urlencode "password=$SDRSTREAM_UI_PASSWORD" "http://127.0.0.1:$PORT/login")"
  [ "$formcode" = 303 ] || { rm -f "$cookie"; echo "health: login POST returned $formcode, expected 303"; return 1; }
  code="$(curl -s -b "$cookie" -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/api/status.json")"
  rm -f "$cookie"
  [ "$code" = 200 ] || { echo "health: authenticated /api/status.json returned $code, expected 200"; return 1; }
  echo "health: ok (login 200, API gated 401, session-authenticated API 200)"
}
tunnel_url() { grep -o 'https://[a-z0-9-]*\.trycloudflare\.com' "$RUN_DIR/tunnel.log" 2>/dev/null | head -1; }

case "${1:-start}" in
stop) stop_one "$TUN_PID"; stop_one "$APP_PID"; echo stopped; exit 0 ;;
status)
  alive "$APP_PID" && echo "app pid $(cat "$APP_PID")" || echo "app not running"
  alive "$TUN_PID" && echo "tunnel pid $(cat "$TUN_PID") $(tunnel_url)" || echo "tunnel not running"
  health || exit $?
  url="$(tunnel_url)"
  if [ -n "$url" ] && alive "$TUN_PID"; then
    code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 "$url/login" || true)"
    [ "$code" = 200 ] || { echo "health: tunnel login returned $code"; exit 1; }
  fi
  exit 0 ;;
start) ;;
*) echo "usage: $0 start|stop|status" >&2; exit 2 ;;
esac

[ -n "${SDRSTREAM_UI_PASSWORD:-}" ] || { echo "SDRSTREAM_UI_PASSWORD must be set" >&2; exit 1; }
export SDRSTREAM_UI_PASSWORD
(cd "$here/../go" && go build -o "$RUN_DIR/sdrstream" ./cmd/sdrstream) || exit 1

if ! alive "$APP_PID"; then
  # shellcheck disable=SC2086
  nohup "$RUN_DIR/sdrstream" web --host 127.0.0.1 --port "$PORT" \
    --captures-dir "$RUN_DIR/captures" ${EXTRA_ARGS:-} >"$RUN_DIR/app.log" 2>&1 &
  echo $! >"$APP_PID"
fi
for _ in $(seq 1 30); do health >/dev/null 2>&1 && break; sleep 1; done
health || { echo "see $RUN_DIR/app.log"; exit 1; }

if [ -x "$CLOUDFLARED" ] && ! alive "$TUN_PID"; then
  : >"$RUN_DIR/tunnel.log"
  nohup "$CLOUDFLARED" tunnel --no-autoupdate --url "http://127.0.0.1:$PORT" >"$RUN_DIR/tunnel.log" 2>&1 &
  echo $! >"$TUN_PID"
  for _ in $(seq 1 30); do [ -n "$(tunnel_url)" ] && break; sleep 1; done
elif [ ! -x "$CLOUDFLARED" ]; then
  echo "cloudflared not found at $CLOUDFLARED (see docs/toolchain.md); app is local only"
fi
url="$(tunnel_url)"
if [ -n "$url" ]; then
  code=000
  for _ in $(seq 1 12); do  # new quick-tunnel hostnames take a few seconds to resolve
    code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 "$url/login")"
    [ "$code" = 200 ] && break; sleep 5
  done
  echo "tunnel: $url (login $code)"
fi
echo "logs: $RUN_DIR/app.log $RUN_DIR/tunnel.log"
