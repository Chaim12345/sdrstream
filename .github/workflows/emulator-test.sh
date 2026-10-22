#!/usr/bin/env bash
# Drives the sdrstream app on a booted emulator: install, launch, watch the
# engine come up, capture screenshots + logcat + on-device logs.
# Usage: emulator-test.sh <apk> <label> [fallback-apk]
set -ux
APK="$1"; LABEL="$2"; FALLBACK="${3:-}"
OUT=emu-results; mkdir -p "$OUT"

install_apk() {
  adb install -r "$1" 2>&1 | tee "$OUT/$LABEL-install.txt"
  grep -q "Success" "$OUT/$LABEL-install.txt"
}

INSTALL_OK=0
if install_apk "$APK"; then
  INSTALL_OK=1
elif [ -n "$FALLBACK" ]; then
  echo "primary APK failed to install; trying fallback $FALLBACK"
  if install_apk "$FALLBACK"; then INSTALL_OK=1; echo "fallback-used" > "$OUT/$LABEL-fallback.txt"; fi
fi
echo "install_ok=$INSTALL_OK"

if [ "$INSTALL_OK" = 1 ]; then
  adb shell am start -n com.chaim12345.sdrstream/.MainActivity
  sleep 20
  adb exec-out screencap -p > "$OUT/$LABEL-20s.png" || true
  adb forward tcp:18080 tcp:8080
  (curl -sS -m 8 http://127.0.0.1:18080/healthz && echo) > "$OUT/$LABEL-healthz-20s.txt" 2>&1 || true
  sleep 50
  adb exec-out screencap -p > "$OUT/$LABEL-70s.png" || true
  (curl -sS -m 8 http://127.0.0.1:18080/healthz && echo) > "$OUT/$LABEL-healthz-70s.txt" 2>&1 || true
  curl -sS -m 8 -o /dev/null -w "root http_code=%{http_code}\n" http://127.0.0.1:18080/ > "$OUT/$LABEL-root.txt" 2>&1 || true
  adb shell run-as com.chaim12345.sdrstream sh -c 'ls -la files/; echo ---; tail -60 files/engine.log 2>/dev/null; echo ---; cat files/crash.log 2>/dev/null' > "$OUT/$LABEL-ondevice-logs.txt" 2>&1 || true
  adb exec-out screencap -p > "$OUT/$LABEL-final.png" || true
fi
adb logcat -d -v threadtime > "$OUT/$LABEL-logcat.txt" 2>&1 || true
grep -iE 'sdrstream|AndroidRuntime|FATAL EXCEPTION' "$OUT/$LABEL-logcat.txt" | tail -120 > "$OUT/$LABEL-logcat-matches.txt" || true
echo "== summary =="
cat "$OUT/$LABEL-install.txt" 2>/dev/null
cat "$OUT/$LABEL-healthz-70s.txt" 2>/dev/null
cat "$OUT/$LABEL-root.txt" 2>/dev/null
cat "$OUT/$LABEL-logcat-matches.txt" 2>/dev/null
