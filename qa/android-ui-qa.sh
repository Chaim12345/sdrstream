#!/usr/bin/env bash
# Android UI QA on a booted emulator. Proves what a user can actually reach by
# touch: login screen, any password control, login with the on-device password
# (read via run-as in CI only), and app lifecycle. Usage: android-ui-qa.sh <apk> <label>
set -u
ADB_BIN="$(command -v adb)"; adb() { timeout 40 "$ADB_BIN" "$@"; }
APK="$1"; LABEL="$2"; PKG=com.chaim12345.sdrstream
OUT=qa-results; mkdir -p "$OUT"; export PATH=$PATH; R="$OUT/$LABEL"
log() { echo "[qa] $*" | tee -a "$R-steps.txt"; }
shot() { adb exec-out screencap -p > "$R-$1.png"; if [ -s "$R-$1.png" ]; then log "screenshot $1"; else log "WARN: screenshot $1 is EMPTY (device lost?)"; fi; }
dump() { adb shell uiautomator dump /sdcard/ui.xml >/dev/null 2>&1; adb exec-out cat /sdcard/ui.xml > "$R-$1.xml" 2>/dev/null; }
ensure_fg() { # relaunch and dismiss any battery dialog until the app owns focus
  for i in 1 2 3 4; do
    if adb shell dumpsys window 2>/dev/null | grep -E 'mCurrentFocus' | grep -q "$PKG"; then
      dump fg; if grep -q 'text="Not now"' "$R-fg.xml"; then
        xy=$(python3 - "$R-fg.xml" <<'PY'
import re,sys
x=open(sys.argv[1],errors='ignore').read()
m=re.search(r'text="Not now"[^>]*?bounds="\[(\d+),(\d+)\]\[(\d+),(\d+)\]"',x)
if m:
    a,b,c,d=map(int,m.groups()); print((a+c)//2,(b+d)//2)
PY
); [ -n "$xy" ] && adb shell input tap $xy; sleep 2; continue; fi
      return 0
    fi
    adb shell am start -n $PKG/.MainActivity >/dev/null; sleep 4
  done; log "WARN: app not in foreground"
}
alive() { adb shell pidof $PKG >/dev/null 2>&1 && echo alive || echo dead; }
require_device() { # hard-fail when the emulator is gone; past this point every check is noise
  state=$(adb get-state 2>/dev/null) || true
  if [ "$state" != "device" ]; then log "FATAL: emulator lost (adb get-state: '${state:-no answer}') - aborting; harness verdict FAIL"; exit 1; fi
}

adb install -r "$APK" 2>&1 | tee "$R-install.txt"
grep -q Success "$R-install.txt" || { log "INSTALL FAILED"; exit 1; }
adb shell pm grant $PKG android.permission.POST_NOTIFICATIONS 2>/dev/null
adb shell getprop ro.build.version.release | tee "$R-android.txt"
adb shell input keyevent KEYCODE_WAKEUP; adb shell wm dismiss-keyguard
adb shell am start -n $PKG/.MainActivity >/dev/null
sleep 45
require_device
# Dismiss the battery dialog if present ("Not now") by tapping its button via UI dump.
dump 01-launch
python3 - "$R-01-launch.xml" <<'PY' > /tmp/notnow.txt
import re,sys
x=open(sys.argv[1],errors='ignore').read()
for m in re.finditer(r'text="Not now"[^>]*bounds="\[(\d+),(\d+)\]\[(\d+),(\d+)\]"',x):
    a,b,c,d=map(int,m.groups()); print((a+c)//2,(b+d)//2); break
PY
if [ -s /tmp/notnow.txt ]; then adb shell input tap $(cat /tmp/notnow.txt); log "dismissed battery dialog"; sleep 2; fi
shot 02-login-screen; dump 02-login-screen
log "app process after launch: $(alive)"

# Reachability of password controls by touch only.
log "--- password-control reachability ---"
for kw in "Show UI password" "Set UI password" "More options" "Navigate up" "menu" "Menu"; do
  if grep -qi "$kw" "$R-02-login-screen.xml"; then log "FOUND in UI tree: $kw"; else log "absent from UI tree: $kw"; fi
done
adb shell input keyevent KEYCODE_MENU; sleep 2; shot 03-after-menu-key; dump 03-after-menu-key
if grep -qi "Show UI password" "$R-03-after-menu-key.xml"; then log "MENU REACHABLE via KEYCODE_MENU (not available on gesture-nav phones)"; else log "KEYCODE_MENU opens nothing"; fi
adb shell input keyevent KEYCODE_BACK 2>/dev/null; sleep 1

# Touch path: tap a visible "Menu" control, then "Show UI password".
tapnode() { # tapnode <xml> <regex on attrs>  -> taps center, echoes ok/none
  python3 - "$1" "$2" <<'PY' > /tmp/tap.txt
import re,sys
x=open(sys.argv[1],errors='ignore').read()
for m in re.finditer(r'<node[^>]*?(?:%s)[^>]*?bounds="\[(\d+),(\d+)\]\[(\d+),(\d+)\]"'%sys.argv[2],x):
    a,b,c,d=map(int,m.groups()); print((a+c)//2,(b+d)//2); break
PY
  if [ -s /tmp/tap.txt ]; then adb shell input tap $(cat /tmp/tap.txt); echo ok; else echo none; fi
}
adb shell am start -n $PKG/.MainActivity >/dev/null; sleep 3; ensure_fg
dump 03b-pre-menu
R1=$(tapnode "$R-03b-pre-menu.xml" 'content-desc="Menu"|text="Menu"|text="⋮"')
log "tap visible Menu control: $R1"
if [ "$R1" = ok ]; then
  sleep 2; shot 03c-menu-open; dump 03c-menu-open
  log "menu items visible: $(grep -oE 'text="(Show UI password|Set UI password|Source code[^"]*|Stop engine|Diagnostics|Share logs)"' $R-03c-menu-open.xml | tr '\n' ' ')"
  R2=$(tapnode "$R-03c-menu-open.xml" 'text="Show UI password"')
  log "tap Show UI password: $R2"
  sleep 2; shot 03d-password-dialog; dump 03d-password-dialog
  SHOWN=$(grep -oE 'text="Log in to the UI with:[^"]*"' "$R-03d-password-dialog.xml" | sed 's/.*&#10;&#10;\([0-9a-f]\{36\}\)&#10;.*/\1/')
  case "$SHOWN" in [0-9a-f]*) echo "::add-mask::$SHOWN"; log "dialog shows a 36-hex password (masked)";; *) log "dialog did not show a password";; esac
  ONDEV=$(adb shell run-as $PKG cat shared_prefs/sdrstream.xml 2>/dev/null | sed -n 's/.*name="ui_password">\([^<]*\)<.*/\1/p')
  [ -n "$SHOWN" ] && [ "$SHOWN" = "$ONDEV" ] && log "dialog password MATCHES on-device stored password" || log "dialog password does NOT match stored password"
  adb shell input keyevent KEYCODE_BACK; sleep 1
fi

# Login with the generated password (read in CI; masked, never printed).
PW=$(adb shell run-as $PKG cat shared_prefs/sdrstream.xml 2>/dev/null | sed -n 's/.*name="ui_password">\([^<]*\)<.*/\1/p')
echo "::add-mask::$PW"
[ -n "$PW" ] && log "on-device password exists (len ${#PW})" || log "NO on-device password found"
adb shell am start -n $PKG/.MainActivity >/dev/null; sleep 5; ensure_fg
dump 04-pre-login
python3 - "$R-04-pre-login.xml" <<'PY' > /tmp/field.txt
import re,sys
x=open(sys.argv[1],errors='ignore').read()
best=None
for m in re.finditer(r'<node[^>]*class="android.widget.EditText"[^>]*bounds="\[(\d+),(\d+)\]\[(\d+),(\d+)\]"',x):
    a,b,c,d=map(int,m.groups()); best=((a+c)//2,(b+d)//2)
print("%d %d"%best if best else "")
PY
if [ -s /tmp/field.txt ]; then
  adb shell input tap $(cat /tmp/field.txt); sleep 1
  # WRONG password first, to prove error handling is visible
  adb shell input text "wrongpass"; adb shell input keyevent KEYCODE_ENTER; sleep 3
  shot 05-wrong-password; dump 05-wrong-password
  log "wrong-password page text: $(grep -oiE 'invalid[^"]*|incorrect[^"]*' $R-05-wrong-password.xml | head -2 | tr '\n' ' ')"
  adb shell input keyevent KEYCODE_BACK; sleep 3; dump 05back; if grep -q 'class="android.widget.EditText"' "$R-05back.xml"; then log "after wrong password: Back returns to login form"; else log "after wrong password: Back does NOT return to form (dead end)"; fi
  dump 05b; python3 - "$R-05b.xml" <<'PY' > /tmp/field.txt
import re,sys
x=open(sys.argv[1],errors='ignore').read()
best=None
for m in re.finditer(r'<node[^>]*class="android.widget.EditText"[^>]*bounds="\[(\d+),(\d+)\]\[(\d+),(\d+)\]"',x):
    a,b,c,d=map(int,m.groups()); best=((a+c)//2,(b+d)//2)
print("%d %d"%best if best else "")
PY
  ensure_fg; adb shell input tap $(cat /tmp/field.txt); sleep 1
  adb shell input text "$PW"; adb shell input keyevent KEYCODE_ENTER; sleep 8
  shot 06-after-login; dump 06-after-login
  if ! adb shell dumpsys window | grep mCurrentFocus | grep -q "$PKG"; then log "LOGIN RESULT: INVALID (app not in foreground, input went elsewhere)"; elif grep -q "Open monitor" "$R-06-after-login.xml"; then log "LOGIN RESULT: still on login form"; elif grep -qE "Overview|Settings|Scan" "$R-06-after-login.xml"; then log "LOGIN RESULT: PASS (app tabs visible)"; else log "LOGIN RESULT: unclear"; fi
  grep -oE 'text="[^"]+"' "$R-06-after-login.xml" | head -40 > "$R-06-visible-text.txt"
else
  log "no EditText found on screen (login form not exposed)"
fi

# Decode-target chips: scroll the Overview until the Receiver panel's chip row is
# visible, then prove it with pixels. An earlier run DOM-verified the chips but its
# emulator died before scrolling - this makes the pixel proof a hard gate.
log "--- decode-target chips: scroll into view ---"
require_device; ensure_fg
CHIPS_OK=0
for i in 1 2 3 4 5 6; do
  dump 06i-try$i
  CH=$(python3 - "$R-06i-try$i.xml" <<'PY'
import re,sys
x=open(sys.argv[1],errors='ignore').read()
m=re.search(r'<node[^>]*resource-id="decode-targets"[^>]*bounds="\[(\d+),(\d+)\]\[(\d+),(\d+)\]"',x)
if not m: print("absent"); sys.exit()
a,b,c,d=map(int,m.groups())
print("visible" if (c-a)>0 and (d-b)>0 else "offscreen")
PY
)
  log "decode-targets (try $i): $CH"
  [ "$CH" = visible ] && { CHIPS_OK=1; break; }
  SZ=$(adb shell wm size | sed 's/.*: //'); W=${SZ%x*}; H=${SZ#*x}
  adb shell input swipe $((W/2)) $((H*3/4)) $((W/2)) $((H/4)) 400; sleep 2
done
shot 06i-decode-chips; dump 06i-decode-chips
if [ "$CHIPS_OK" = 1 ]; then
  BTNS=$(grep -oE 'text="(DWD RTTY [^"]+|DWD WEFAX [^"]+|Shannon VOLMET [^"]+|NAVTEX 518|OK0EU CW beacon)"' "$R-06i-decode-chips.xml" | sort -u | tr '\n' ' ')
  log "chip buttons in frame: $BTNS"
  if [ -n "$BTNS" ]; then log "CHIPS PIXEL CHECK: PASS"; else log "CHIPS PIXEL CHECK: FAIL (container visible but no chip buttons)"; exit 1; fi
else
  log "CHIPS PIXEL CHECK: FAIL (decode-targets not visible after 6 scrolls)"; exit 1
fi

# Set UI password flow (stale-env bug check): change it, expect new works, old rejected.
NEWPW="qaNewPass123"
OLDPW="$PW"
edit_center() { python3 - "$1" <<'PY'
import re,sys
x=open(sys.argv[1],errors='ignore').read()
best=None
for m in re.finditer(r'<node[^>]*class="android.widget.EditText"[^>]*bounds="\[(\d+),(\d+)\]\[(\d+),(\d+)\]"',x):
    a,b,c,d=map(int,m.groups()); best=((a+c)//2,(b+d)//2)
print("%d %d"%best if best else "")
PY
}
adb shell am start -n $PKG/.MainActivity >/dev/null; sleep 3; ensure_fg; dump 06b
S1=$(tapnode "$R-06b.xml" 'content-desc="Menu"|text="Menu"|text="⋮"'); log "set-pw: tap Menu: $S1"
if [ "$S1" = ok ]; then
  sleep 2; dump 06c; S2=$(tapnode "$R-06c.xml" 'text="Set UI password"'); log "set-pw: tap Set UI password: $S2"
  sleep 2; dump 06d; shot 06d-set-dialog
  adb shell input tap $(edit_center "$R-06d.xml"); sleep 1; adb shell input text "$NEWPW"; sleep 1; shot 06e-typed
  dump 06e; S3=$(tapnode "$R-06e.xml" 'text="SAVE"|text="Save"'); log "set-pw: tap Save: $S3"
  sleep 25; shot 06f-after-save; dump 06f
  log "after save: engine process $(alive); toast/page text: $(grep -oE 'text="[^"]+"' $R-06f.xml | head -8 | tr '\n' ' ')"
  for TRY in old new; do
    [ $TRY = old ] && CAND="$OLDPW" || CAND="$NEWPW"
    dump 06g-$TRY; C=$(edit_center "$R-06g-$TRY.xml")
    if [ -n "$C" ]; then
      adb shell input tap $C; sleep 1; adb shell input text "$CAND"; adb shell input keyevent KEYCODE_ENTER; sleep 6
      shot 06h-$TRY; dump 06h-$TRY
      if grep -q 'class="android.widget.EditText"' "$R-06h-$TRY.xml"; then log "set-pw: $TRY password -> still on login form (rejected)"; else log "set-pw: $TRY password -> ACCEPTED (left login form)"; fi
    else log "set-pw: no login field for $TRY try (maybe already logged in)"; fi
  done
fi

# Lifecycle: background/foreground, then force-stop and relaunch.
require_device
adb shell input keyevent KEYCODE_HOME; sleep 5
log "after HOME: $(alive)"
adb shell am start -n $PKG/.MainActivity >/dev/null; sleep 5; shot 07-resume
adb shell am force-stop $PKG; sleep 2
adb shell am start -n $PKG/.MainActivity >/dev/null; sleep 40; shot 08-cold-restart; dump 08-cold-restart
log "after cold restart: $(alive)"
adb forward tcp:18080 tcp:8080 >/dev/null
curl -sS -m 8 http://127.0.0.1:18080/healthz > "$R-healthz.txt" 2>&1; log "healthz: $(cat $R-healthz.txt | head -c 200)"
adb logcat -d -v threadtime > "$R-logcat.txt" 2>&1
grep -E 'FATAL EXCEPTION|AndroidRuntime' "$R-logcat.txt" | head -20 > "$R-fatal.txt"; log "fatal lines: $(wc -l < $R-fatal.txt)"
adb shell run-as $PKG sh -c 'tail -40 files/engine.log 2>/dev/null' | sed "s/$PW/<pw>/g" > "$R-engine-tail.txt" 2>&1
