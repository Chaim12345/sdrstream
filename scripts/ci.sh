#!/usr/bin/env bash
# CI check: gofmt, go vet, go build, go test (-count=1, 5m timeout), go test -race,
# skip summary. Exit non-zero on any failure.
#
# Env:
#   STRICT_FMT=1        (default) unformatted files fail the run; STRICT_FMT=0 only warns.
#   REQUIRE_FIXTURES=1  fail if any test skipped because a recording/fixture is missing.
#   REQUIRE_RACE=1      fail (instead of warn) when the race detector is unavailable.
#   RACE_PKGS           override the package list for -race (default: all packages).
set -uo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
. "$here/env.sh" || exit 1
cd "$here/../go"
fail=0
step() { echo "== $*"; }
STRICT_FMT="${STRICT_FMT:-1}"
TEST_FLAGS=(-count=1 -timeout 5m)
log="$(mktemp)"; trap 'rm -f "$log"' EXIT

step "go version: $(go version)"
step gofmt
unformatted="$(gofmt -l .)"
if [ -n "$unformatted" ]; then
  echo "needs gofmt:"; echo "$unformatted"
  [ "$STRICT_FMT" = 1 ] && fail=1
fi
step "go vet"; go vet ./... || fail=1
step "go build"; go build ./... || fail=1

step "go test"
go test "${TEST_FLAGS[@]}" -v ./... >"$log" 2>&1
rc=$?
# Print package results and failures, not the whole -v stream.
grep -E '^(ok|FAIL|---\s*FAIL|panic|\?)' "$log" || true
[ $rc -ne 0 ] && { fail=1; grep -E -B2 -A15 -- '--- FAIL' "$log" | head -200; }

step "skipped tests"
# The reason is the t.Skip log line (file.go:N: text) printed just before '--- SKIP'.
skips="$(awk '/^[[:space:]]+[A-Za-z0-9_.]+\.go:[0-9]+:/{last=$0; sub(/^[[:space:]]+[A-Za-z0-9_.]+\.go:[0-9]+: */,"",last)} /^[[:space:]]*--- SKIP:/{print $3 ": " last}' "$log")"
nskip="$(printf '%s' "$skips" | grep -c . || true)"
echo "skipped: $nskip"
[ -n "$skips" ] && printf '%s\n' "$skips"
if [ "${REQUIRE_FIXTURES:-0}" = 1 ]; then
  missing="$(printf '%s\n' "$skips" | grep -Ei 'fixture|recording|wav|samples|absent|not present|no model' || true)"
  if [ -n "$missing" ]; then
    echo "REQUIRE_FIXTURES=1: tests skipped for missing fixtures:"; echo "$missing"; fail=1
  fi
fi

step "go test -race"
if [ "$(go env CGO_ENABLED)" = 1 ] && command -v "$(go env CC)" >/dev/null 2>&1; then
  # shellcheck disable=SC2086
  go test -race "${TEST_FLAGS[@]}" ${RACE_PKGS:-./...} || fail=1
else
  echo "race detector unavailable (needs CGO_ENABLED=1 and a C compiler)"
  [ "${REQUIRE_RACE:-0}" = 1 ] && fail=1
fi

if [ $fail -ne 0 ]; then echo "CI FAILED"; exit 1; fi
echo "CI OK"
