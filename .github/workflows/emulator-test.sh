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
  adb shell pm grant com.chaim12345.sdrstream android.permission.POST_NOTIFICATIONS || true
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
  # Dismiss any leftover dialogs and let the WebView settle, then final shot.
  adb shell input keyevent KEYCODE_BACK || true
  sleep 3
  adb shell am start -n com.chaim12345.sdrstream/.MainActivity || true
  sleep 8
  adb exec-out screencap -p > "$OUT/$LABEL-final.png" || true

  # The app may be in the background here (the KEYCODE_BACK above reaches
  # onBackPressed -> moveTaskToBack when no dialog is showing). Relaunch and
  # wait until sdrstream actually has window focus before dumping the UI.
  for i in $(seq 1 10); do
    adb shell am start -n com.chaim12345.sdrstream/.MainActivity >/dev/null 2>&1 || true
    sleep 2
    FOCUS=$(adb shell dumpsys window 2>/dev/null | grep -o 'mCurrentFocus[^}]*' || true)
    case "$FOCUS" in *com.chaim12345.sdrstream*) break;; esac
  done
  echo "focus after relaunch: $FOCUS" | tee "$OUT/$LABEL-focus.txt"

  # Menu-reachability proof (the 0.1.1 bug: the options menu existed in code
  # but no on-screen control could open it). Find a UI node by attribute
  # value in the uiautomator dump, tap its center, and screenshot the result.
  tap_node() { # $1=attr name, $2=attr value, $3=screenshot label
    ATTR="$1"; VAL="$2"; SHOT="$3"
    adb shell uiautomator dump /sdcard/ui.xml >/dev/null 2>&1 || true
    adb pull /sdcard/ui.xml "$OUT/$LABEL-ui.xml" >/dev/null 2>&1 || true
    BOUNDS=$(grep -o "$ATTR=\"$VAL\"[^>]*bounds=\"\[[0-9]*,[0-9]*\]\[[0-9]*,[0-9]*\]\"" "$OUT/$LABEL-ui.xml" \
      | head -1 | grep -o '\[[0-9]*,[0-9]*\]\[[0-9]*,[0-9]*\]' || true)
    if [ -z "$BOUNDS" ]; then
      echo "node $ATTR=$VAL NOT FOUND" | tee "$OUT/$LABEL-$SHOT-missing.txt"
      return 1
    fi
    XY=$(echo "$BOUNDS" | tr -cs '0-9' ' ')
    set -- $XY
    adb shell input tap $(( ($1 + $3) / 2 )) $(( ($2 + $4) / 2 )) || true
    sleep 2
    adb exec-out screencap -p > "$OUT/$LABEL-$SHOT.png" || true
    echo "tapped $ATTR=$VAL at $XY"
    return 0
  }
  # A late dialog (battery-exemption prompt reappears on every cold start)
  # can own the focused window and hide the menu node from the dump. Dismiss
  # and retry a few times before declaring the control missing.
  for i in 1 2 3; do
    if tap_node content-desc Menu menu-open; then
      break
    fi
    adb shell input keyevent KEYCODE_BACK >/dev/null 2>&1 || true
    sleep 2
    adb shell am start -n com.chaim12345.sdrstream/.MainActivity >/dev/null 2>&1 || true
    sleep 3
  done
  # Open the overflow menu via its always-visible button, then open the
  # password dialog. The password shown is generated fresh on the ephemeral
  # CI emulator - not the user's device password.
  if [ -f "$OUT/$LABEL-menu-open.png" ]; then
    tap_node text "Show UI password" password-dialog || true
    adb shell input keyevent KEYCODE_BACK || true
  fi
fi
adb logcat -d -v threadtime > "$OUT/$LABEL-logcat.txt" 2>&1 || true
grep -iE 'sdrstream|AndroidRuntime|FATAL EXCEPTION' "$OUT/$LABEL-logcat.txt" | tail -120 > "$OUT/$LABEL-logcat-matches.txt" || true
echo "== summary =="
cat "$OUT/$LABEL-install.txt" 2>/dev/null
cat "$OUT/$LABEL-healthz-70s.txt" 2>/dev/null
cat "$OUT/$LABEL-root.txt" 2>/dev/null
cat "$OUT/$LABEL-logcat-matches.txt" 2>/dev/null
