#!/usr/bin/env bash
# QA matrix: for each remote branch, check out a throwaway worktree, run ci.sh,
# then re-run go test with -race -count=3 to flag flaky tests.
# Usage: scripts/qa-matrix.sh [branch ...]   (default: integration + feature/*)
set -uo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
git fetch -q origin
branches=("$@")
[ ${#branches[@]} -eq 0 ] && branches=(sdrstream-team-integration $(git for-each-ref --format='%(refname:short)' refs/remotes/origin/feature | sed 's#^origin/##'))
out="${QA_OUT:-/tmp/qa}"; mkdir -p "$out"
for b in "${branches[@]}"; do
  wt="$(mktemp -d)"; safe="${b//\//_}"
  sha="$(git rev-parse --short "origin/$b")"
  git worktree add -q --detach "$wt" "origin/$b" || continue
  ci=FAIL; flake=FAIL
  timeout 600 bash "$wt/scripts/ci.sh" >"$out/$safe.ci.log" 2>&1 && ci=OK
  if [ "$ci" = OK ]; then
    . "$wt/scripts/env.sh"
    (cd "$wt/go" && timeout 900 go test -race -count=3 ./... >"$out/$safe.race.log" 2>&1) && flake=OK
  fi
  echo "$b $sha ci=$ci race3=$flake"
  git worktree remove --force "$wt"
done
