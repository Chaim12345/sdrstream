#!/usr/bin/env bash
# Orchestrates the two QA harnesses and owns the job verdict.
# The emulator-runner executes each script: line as a SEPARATE sh -c call, so
# shell variables cannot carry state between workflow lines - all verdict
# logic must live in this single bash process. Usage: android-ui-qa-run.sh <apk>
set -u
APK="${1:-fat.apk}"
QA1=0; QA2=0
timeout 700 bash -x qa-src/qa/android-ui-qa.sh "$APK" ui > /tmp/qa1.log 2>&1 || QA1=$?
cat /tmp/qa1.log | tee -a qa-results-trace.txt
if [ "$QA1" -eq 0 ]; then
  timeout 600 bash qa-src/qa/android-func-audit.sh cgo1 200 "$APK" > /tmp/qa2.log 2>&1 || QA2=$?
  cat /tmp/qa2.log | tee -a qa-results-trace.txt
else
  echo "skipping android-func-audit.sh: ui-qa failed (exit $QA1); device state unusable" | tee -a qa-results-trace.txt
fi
echo "verdict: android-ui-qa.sh exit=$QA1 (gated); android-func-audit.sh exit=$QA2 (informational - no verdict semantics in that script yet)"
[ "$QA1" -eq 0 ]
