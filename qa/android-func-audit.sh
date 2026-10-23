#!/usr/bin/env bash
# Whole-app functional audit on an emulator running the app's native engine.
# Logs in over the forwarded port with the on-device password (read in CI,
# masked), sweeps every /api endpoint, taps every bottom-nav tab with
# screenshots, and re-sweeps after a settle period to see whether live data
# ever appears. Usage: android-func-audit.sh <label> [settle_seconds]
set -u
adb() { timeout 40 command adb "$@"; }
LABEL="$1"; SETTLE="${2:-240}"; APK="${3:-}"; PKG=com.chaim12345.sdrstream
OUT=qa-results; mkdir -p "$OUT"; R="$OUT/$LABEL"
log() { echo "[audit] $*" | tee -a "$R-audit.txt"; }
if [ -n "$APK" ]; then
  adb uninstall $PKG >/dev/null 2>&1
  adb install -r "$APK" 2>&1 | tail -1 | tee -a "$R-audit.txt"
  adb shell pm grant $PKG android.permission.POST_NOTIFICATIONS 2>/dev/null
  adb shell input keyevent KEYCODE_WAKEUP
  adb shell am start -n $PKG/.MainActivity >/dev/null
  sleep 60
fi
PW=$(adb shell run-as $PKG cat shared_prefs/sdrstream.xml 2>/dev/null | sed -n 's/.*name="ui_password">\([^<]*\)<.*/\1/p')
echo "::add-mask::$PW"
adb forward tcp:18081 tcp:8080 >/dev/null
BASE=http://127.0.0.1:18081; JAR=/tmp/jar
curl -sS -m 10 -c $JAR -o /dev/null -w "login http=%{http_code}\n" -H "Origin: $BASE" -H "Referer: $BASE/login" --data-urlencode "password=$PW" $BASE/login | tee -a "$R-audit.txt"
ENDPOINTS="status scans scanlog activity live spectrum captures satellites observations gallery feeds ledger metrics sources bandconditions stations map tune"
sweep() { # sweep <tag>
  for e in $ENDPOINTS; do
    f="$R-$1-$e.json"
    code=$(curl -sS -m 20 -b $JAR -o "$f" -w "%{http_code}" "$BASE/api/$e.json" 2>/dev/null)
    size=$(wc -c < "$f" 2>/dev/null || echo 0)
    shape=$(python3 - "$f" <<'PY' 2>/dev/null
import json,sys
try: d=json.load(open(sys.argv[1]))
except Exception as ex: print("not-json"); sys.exit()
def desc(v):
    if isinstance(v,list): return "list[%d]"%len(v)
    if isinstance(v,dict): return "{"+",".join("%s:%s"%(k,desc(x) if isinstance(x,(list,dict)) else repr(x)[:24]) for k,x in list(v.items())[:12])+"}"
    return repr(v)[:40]
print(desc(d)[:400])
PY
)
    log "$1 /api/$e.json http=$code bytes=$size $shape"
  done
}
log "== sweep t0 (just after login) =="; sweep t0
adb shell input keyevent KEYCODE_WAKEUP
adb shell am start -n $PKG/.MainActivity >/dev/null; sleep 5
tabs="Overview Map Activity Decodes Passes Gallery Captures Scan Stations Satellites Live_data Settings"
for t in $tabs; do
  adb shell uiautomator dump /sdcard/u.xml >/dev/null 2>&1; adb exec-out cat /sdcard/u.xml > /tmp/u.xml 2>/dev/null
  lbl=${t//_/ }
  xy=$(python3 - /tmp/u.xml "$lbl" <<'PY'
import re,sys
x=open(sys.argv[1],errors='ignore').read(); l=sys.argv[2]
for m in re.finditer(r'<node[^>]*?(?:text|content-desc)="[^"]*%s[^"]*"[^>]*?bounds="\[(\d+),(\d+)\]\[(\d+),(\d+)\]"'%re.escape(l),x):
    a,b,c,d=map(int,m.groups())
    if c-a>0: print((a+c)//2,(b+d)//2); break
PY
)
  if [ -n "$xy" ]; then adb shell input tap $xy; sleep 4; adb exec-out screencap -p > "$R-tab-$t.png"; adb shell uiautomator dump /sdcard/u.xml >/dev/null 2>&1; adb exec-out cat /sdcard/u.xml > "$R-tab-$t.xml"; log "tab $t: tapped, $(grep -oE 'text="[^"]{3,60}"' $R-tab-$t.xml | wc -l) text nodes"; else log "tab $t: NO tappable node found"; fi
done
log "== settling ${SETTLE}s for live data =="; sleep "$SETTLE"
log "== sweep t1 (after settle) =="; sweep t1
adb exec-out screencap -p > "$R-after-settle.png"
MON="${MONITOR_SERVER:-jukebox.spdns.eu:6166}"
post() { curl -sS -m 40 -b $JAR -o /tmp/p.out -w "%{http_code}" -H "Origin: $BASE" -H "Referer: $BASE/" -X POST --data "$1" $BASE/api/monitor/control; }
log "== monitor error path: bogus receiver =="
c=$(post "action=start&kind=openwebrx&server=127.0.0.1:1&label=qa-bogus"); log "start bogus -> http=$c body=$(head -c 160 /tmp/p.out | tr '\n' ' ')"
c=$(post "action=start&kind=bad&server=x.example:1"); log "start bad kind -> http=$c body=$(head -c 120 /tmp/p.out | tr '\n' ' ')"
log "== monitor start on public receiver $MON (single bounded connection) =="
# Real UI taps: Overview -> receiver address -> start.
adb shell am start -n $PKG/.MainActivity >/dev/null; sleep 3
node_xy() { adb shell uiautomator dump /sdcard/u.xml >/dev/null 2>&1; adb exec-out cat /sdcard/u.xml > /tmp/u.xml 2>/dev/null
  python3 - /tmp/u.xml "$1" <<'PY'
import re,sys
x=open(sys.argv[1],errors='ignore').read(); k=sys.argv[2]
for m in re.finditer(r'<node[^>]*?(?:text|content-desc|hint)="%s"[^>]*?bounds="\[(\d+),(\d+)\]\[(\d+),(\d+)\]"'%k,x):
    a,b,c,d=map(int,m.groups())
    if d>b and c>a: print((a+c)//2,(b+d)//2); break
PY
}
UIOK=0
for sw in 0 1 2 3 4 5; do
  xy=$(node_xy 'Receiver address')
  [ -n "$xy" ] && break
  adb shell input swipe 360 1200 360 500 300; sleep 1
done
if [ -n "$xy" ]; then
  adb shell input tap $xy; sleep 1; adb shell input text "$MON"; sleep 1; adb shell input keyevent KEYCODE_BACK; sleep 1
  sxy=$(node_xy 'start')
  if [ -n "$sxy" ]; then adb shell input tap $sxy; UIOK=1; log "UI: typed receiver and tapped start"; sleep 3; adb exec-out screencap -p > "$R-monitor-ui-start.png"; fi
else log "UI: receiver address field not found after scrolling"; fi
if [ "$UIOK" = 1 ]; then c=200; sleep 5; else c=$(post "action=start&kind=openwebrx&server=$MON&label=qa-audit"); log "UI path failed; HTTP start -> http=$c"; fi
if [ "$c" = 200 ]; then
  sleep 90
  for e in status live spectrum activity captures; do
    curl -sS -m 20 -b $JAR -o "$R-mon-$e.json" -w "mon /api/$e.json http=%{http_code} bytes=%{size_download}\n" $BASE/api/$e.json | tee -a "$R-audit.txt"
  done
  curl -sS -m 15 -b $JAR --max-time 12 -o "$R-live-audio.bin" -w "live audio http=%{http_code} bytes=%{size_download} type=%{content_type}\n" $BASE/audio/live/ | tee -a "$R-audit.txt"
  adb exec-out screencap -p > "$R-monitor-running.png"
  c=$(post "action=stop"); log "stop -> http=$c body=$(head -c 120 /tmp/p.out | tr '\n' ' ')"
fi
adb shell run-as $PKG sh -c 'tail -60 files/engine.log 2>/dev/null' | sed "s/$PW/<pw>/g" > "$R-engine-tail.txt"
adb shell run-as $PKG sh -c 'ls -la files/ files/state ../captures 2>&1 | head -40' > "$R-files.txt" 2>&1
adb shell top -b -n 1 | grep -iE 'sdrstream|libsdr' | head -3 | tee -a "$R-audit.txt"
adb logcat -d -v threadtime | grep -E 'FATAL EXCEPTION|AndroidRuntime' | head > "$R-fatal.txt"; log "fatal lines: $(wc -l < $R-fatal.txt)"
