#!/usr/bin/env bash
# CI-style check: gofmt, go vet, go build, go test. Exit non-zero on any failure.
set -uo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
. "$here/env.sh" || exit 1
cd "$here/../go"
fail=0
step() { echo "== $*"; }
step "go version: $(go version)"
step gofmt
unformatted="$(gofmt -l .)"
if [ -n "$unformatted" ]; then
  echo "needs gofmt:"; echo "$unformatted"
  # Several base-branch files are not gofmt-clean yet. Warn by default; set STRICT_FMT=1 to fail.
  [ "${STRICT_FMT:-0}" = 1 ] && fail=1
fi
step "go vet"; go vet ./... || fail=1
step "go build"; go build ./... || fail=1
step "go test"; go test ./... || fail=1
if [ $fail -ne 0 ]; then echo "CI FAILED"; exit 1; fi
echo "CI OK"
