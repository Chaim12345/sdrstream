#!/usr/bin/env bash
# sdrstream QA gate. Run from the repo root:  tools/qa/qa_check.sh
# Fails (exit 1) if any gate fails. Nothing here touches public receivers.
set -u
cd "$(dirname "$0")/../.." || exit 1
PASS=0; FAIL=0
ok()   { PASS=$((PASS+1)); echo "PASS  $1"; }
bad()  { FAIL=$((FAIL+1)); echo "FAIL  $1"; }
check(){ if eval "$2"; then ok "$1"; else bad "$1"; fi; }

GO="${GO:-go}"
echo "== 0. Toolchain =="
V="$($GO version 2>/dev/null)"
if echo "$V" | grep -q 'go1\.2[3-9]'; then ok "real Go toolchain ($V)"; else bad "Go toolchain broken or too old ('$V') - the /usr/local/go shim can exit 0 silently; use a user-space toolchain"; fi

echo "== 1. Build / vet / unit tests =="
check "go build ./..."  "(cd go && $GO build ./...) >/dev/null 2>&1"
check "go vet ./..."    "(cd go && $GO vet ./...)   >/dev/null 2>&1"
check "go test ./..."   "(cd go && $GO test ./...)  >/dev/null 2>&1"
# The SSTV reference fixture must carry VIS 0x28 (Martin M2). If the fixture
# was regenerated, gen_test_wavs.py writes /tmp/test_sstv.wav; it must be
# copied to tests/data/ref_sstv.wav (the script does NOT do this itself).
check "SSTV fixture is Martin M2 (VIS 0x28)" "(cd go && $GO test ./internal/decode/ -run 'TestSSTVReference|TestSSTVImage' ) >/dev/null 2>&1"

echo "== 2. Build the web binary and start a local server (no receivers) =="
TMP="$(mktemp -d)"; PORT=18765
(cd go && $GO build -o "$TMP/sdrstream" ./cmd/sdrstream) || { bad "binary build"; echo "abort"; exit 1; }
SDRSTREAM_UI_PASSWORD='qa-gate-pw' "$TMP/sdrstream" web --host 127.0.0.1 --port $PORT --captures-dir "$TMP/cap" >"$TMP/server.log" 2>&1 &
SRV=$!
for i in $(seq 1 30); do curl -s -o /dev/null "http://127.0.0.1:$PORT/login" && break; sleep 0.3; done
JAR="$TMP/cookies"; BASE="http://127.0.0.1:$PORT"

echo "== 3. Auth gate (password set) =="
check "GET / redirects to /login when unauthenticated"  "curl -s -o /dev/null -w '%{http_code}' $BASE/ | grep -qE '30[23]'"
check "GET /api/status.json unauthenticated is 401"     "curl -s -o /dev/null -w '%{http_code}' $BASE/api/status.json | grep -q 401"
check "GET /api/gallery.json unauthenticated is 401"    "curl -s -o /dev/null -w '%{http_code}' $BASE/api/gallery.json | grep -q 401"
check "GET /api/scans.json unauthenticated is 401"      "curl -s -o /dev/null -w '%{http_code}' $BASE/api/scans.json | grep -q 401"
check "audio route unauthenticated is 401"              "curl -s -o /dev/null -w '%{http_code}' $BASE/audio/live/x.wav | grep -q 401"
check "wrong password is 401"                            "curl -s -o /dev/null -w '%{http_code}' -X POST -d 'password=nope' $BASE/login | grep -q 401"
check "right password redirects and sets session cookie" "curl -s -c $JAR -o /dev/null -w '%{http_code}' -X POST -d 'password=qa-gate-pw' $BASE/login | grep -q 303 && grep -q sdrstream_session $JAR"
check "session cookie is HttpOnly+Secure+SameSite=Strict" "curl -s -D - -o /dev/null -X POST -d 'password=qa-gate-pw' $BASE/login | grep -i 'set-cookie' | grep -qi 'httponly' && curl -s -D - -o /dev/null -X POST -d 'password=qa-gate-pw' $BASE/login | grep -i 'set-cookie' | grep -qi 'secure' && curl -s -D - -o /dev/null -X POST -d 'password=qa-gate-pw' $BASE/login | grep -i 'set-cookie' | grep -qi 'samesite=strict'"
check "authenticated GET / is 200 with tab markup"      "curl -s -b $JAR $BASE/ | grep -q 'id=\"view-overview\"'"
check "authenticated /api/status.json is 200"           "curl -s -b $JAR -o /dev/null -w '%{http_code}' $BASE/api/status.json | grep -q 200"

echo "== 4. Rate limit (5 failures per 15 min per IP) =="
for i in 1 2 3 4 5; do curl -s -o /dev/null -X POST -d 'password=wrong' $BASE/login; done
check "6th bad login attempt is 429"                    "curl -s -o /dev/null -w '%{http_code}' -X POST -d 'password=wrong' $BASE/login | grep -q 429"
check "429 carries Retry-After"                         "curl -s -D - -o /dev/null -X POST -d 'password=wrong' $BASE/login | grep -qi 'retry-after'"

echo "== 5. Security headers =="
H="$(curl -s -D - -o /dev/null $BASE/login)"
check "X-Content-Type-Options nosniff"  "echo \"\$H\" | grep -qi 'x-content-type-options: nosniff'"
check "X-Frame-Options DENY"            "echo \"\$H\" | grep -qi 'x-frame-options: DENY'"
check "Content-Security-Policy present" "echo \"\$H\" | grep -qi 'content-security-policy'"

echo "== 6. Live vs fixture separation =="
check "test-fixtures endpoint labels items as test-only" "curl -s -b $JAR $BASE/api/test-fixtures.json | grep -qi 'test'"
check "live gallery contains no fixture entries"         "! curl -s -b $JAR $BASE/api/gallery.json | grep -qi 'fixture'"

echo "== 7. Logout =="
check "GET /logout is 405 (POST-only, anti-CSRF)"  "curl -s -b $JAR -o /dev/null -w '%{http_code}' $BASE/logout | grep -q 405"
check "POST logout clears the session" "curl -s -b $JAR -c $JAR -o /dev/null -w '%{http_code}' -X POST $BASE/logout | grep -q 303 && curl -s -b $JAR -o /dev/null -w '%{http_code}' $BASE/api/status.json | grep -q 401"

kill $SRV 2>/dev/null; wait $SRV 2>/dev/null
echo
echo "== STATIC politeness review (read the output, confirm by eye) =="
rg -n 'scan-start-connections|scan-max-connections|scan-ramp|scan-advance-error-rate|scan-backoff-refusal-rate|scan-advance-load-ratio|scan-backoff-load-ratio|scan-bandwidth-limit-mbps|scan-stream-kbps' go/cmd/sdrstream/main.go | sed 's/^/  configurable ramp: /'
grep -n 'defer func() { source.Close()' go/cmd/sdrstream/main.go | sed 's/^/  clean disconnect: /'
echo "  team ceiling: 200 concurrent connections, each to a DIFFERENT station"
echo "  split ceiling: C max 100, integrator scanner max 80, D max 20"
echo "  start at 20; step 20 -> 50 -> 80 (configured stages capped and deduplicated)"
echo "  ramp gate <15% refusal+timeout and load <70% cores; backoff >30% refusals or >90% load; estimated bandwidth <=20 Mbit/s"
echo "  verify /api/scans.json exposes ramp_step, active_connections, refusals, timeouts, load_average, estimated bandwidth"
echo
echo "== MANUAL gates still required before user demo =="
echo "  [ ] Screenshots at 390x844 (phone) and desktop of every main view; no overflow/overlap"
echo "  [ ] Every live-gallery item has: station, frequency, mode, UTC time, and passed the text quality gate"
echo "  [ ] No fixture content anywhere under the Live tab"
echo "  [ ] Tunnel URL serves the login page, not the open UI (SDRSTREAM_UI_PASSWORD set; Serve refuses a public bind without it)"
echo "  [ ] Server started under nohup so it survives the session"
echo "  [ ] Team budget respected: <=200 concurrent, all different stations (C<=100, scanner<=80, D<=20)"
echo "  [ ] Ramp honored: step starts at 20; current step <=20/50/100/200 and integrator max 80, stepped only per gates"
echo
echo "RESULT: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
