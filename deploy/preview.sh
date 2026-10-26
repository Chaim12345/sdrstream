#!/bin/sh
# deploy/preview.sh - the only supported way to run the shared public demo.
#
# Hard-codes -preview so a hand-typed restart cannot drop the guard, and
# starts the cloudflare tunnel ONLY after the guarded server is confirmed
# healthy - a non-preview or failed server never gets a tunnel from here.
#
# Usage: SDRSTREAM_UI_PASSWORD comes from the vault (never put it in this
# script or on the command line - it is exported, not echoed):
#   export SDRSTREAM_UI_PASSWORD="$(cat /path/from/vault)"
#   deploy/preview.sh [binary] &
# Then read the tunnel URL from the cloudflared log and update the vault
# item's site list.
set -eu

BIN="${1:-./sdrstream}"
PORT="${PORT:-18801}"
CAPTURES="${CAPTURES:-/tmp/uia-captures}"
LEDGER="${LEDGER:-}"
LOG="${LOG:-/tmp/preview-server.log}"
CFLOG="${CFLOG:-/tmp/preview-cloudflared.log}"

if [ -z "${SDRSTREAM_UI_PASSWORD:-}" ]; then
  echo "preview.sh: SDRSTREAM_UI_PASSWORD is not set; refusing to start a shared preview without auth" >&2
  exit 1
fi

# Start the server in preview mode; -preview makes it refuse to boot if the
# password were somehow empty, and denies receiver start/tune/scan/decode
# regardless of environment.
"$BIN" web -preview -host 127.0.0.1 -port "$PORT" -captures-dir "$CAPTURES" \
  ${LEDGER:+-catalog-import "$LEDGER"} >"$LOG" 2>&1 &
SERVER_PID=$!

# Wait for the guarded server to answer locally before any tunnel exists.
i=0
while [ "$i" -lt 30 ]; do
  if ! kill -0 "$SERVER_PID" 2>/dev/null; then
    echo "preview.sh: server exited during startup; see $LOG - tunnel NOT started" >&2
    exit 1
  fi
  if curl -sf -o /dev/null "http://127.0.0.1:$PORT/healthz"; then
    break
  fi
  i=$((i + 1))
  sleep 1
done
if [ "$i" -ge 30 ]; then
  echo "preview.sh: server did not become healthy; tunnel NOT started" >&2
  kill "$SERVER_PID" 2>/dev/null || true
  exit 1
fi

# Unauthenticated API must be denied before exposure; if it is not, stop
# everything rather than open a tunnel to an unguarded server.
if [ "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/api/status.json")" != "401" ]; then
  echo "preview.sh: unauthenticated API did not return 401; killing server, tunnel NOT started" >&2
  kill "$SERVER_PID" 2>/dev/null || true
  exit 1
fi

echo "preview.sh: guarded server healthy on 127.0.0.1:$PORT (pid $SERVER_PID); starting tunnel"
# CF_CMD exists so the guard logic is testable without churning a live
# tunnel; production leaves it at the default.
CF_CMD="${CF_CMD:-cloudflared tunnel --url http://127.0.0.1:$PORT}"
$CF_CMD >"$CFLOG" 2>&1 &
echo "preview.sh: tunnel launcher pid $! - tunnel URL appears in $CFLOG; update the vault item's site"
wait
