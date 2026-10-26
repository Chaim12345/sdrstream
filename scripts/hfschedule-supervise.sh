#!/bin/sh
# Supervisor for the NAVTEX 490/4209.5 recovery lane.
# - restarts hfschedule-ext on any exit
# - removes a stale scheduler.lock when no hfschedule-ext is running
# - capture backstop OUTSIDE the Go runtime: a capture attempt still
#   marked in-flight ("interrupted") 15 min after its start is wedged
#   (post-freeze timer/select loss); SIGQUIT for a dump, then SIGKILL.
# - liveness backstop: a schedule slot that is due (3-6 min past its
#   start) with NO attempt record under a fresh heartbeat means the
#   poll select (service.go:93) is wedged between attempts, which the
#   capture backstop cannot see; SIGQUIT for a dump, then SIGKILL.
#   Tightness: fires only while a slot is actually due, never on an
#   idle schedule, never under a stale heartbeat, never during a
#   receiver failure cooldown, and only after two confirming cycles.
#   2026-10-25 correction: the event that motivated this guard (15:50
#   UTC "service.go:93 wedge") was reclassified - no such slot exists
#   in the schedule (Serapeum transmits only 0750/1150 UTC here); the
#   service was healthy-idle and was killed in error. A healthy idle
#   scheduler shows the same surface: no attempt record, zero CPU,
#   goroutine 1 in a short untimed park on the poll ticker (true wedges
#   show a timed park, e.g. "[select, 3 minutes]"). This guard keys
#   only on slots present in the schedule file, and it correctly stayed
#   silent at 19:50 UTC the same day. Slot times must always come from
#   the schedule file, never from assumption.
cd /home/sandbox/sdrstream-ext || exit 1
OUT=/home/sandbox/navtex-ext
STATE=$OUT/run1/state.json
LOCK=$OUT/run1/scheduler.lock
SCHED=examples/hfschedule/eibi-extra-navtex-490-4209.txt
while true; do
  if ! pgrep -f 'hfschedule-ext -source' >/dev/null 2>&1 && [ -d "$LOCK" ]; then
    rmdir "$LOCK" 2>/dev/null && echo "$(date -u +%FT%TZ) removed stale scheduler.lock" >> "$OUT/supervisor.log"
  fi
  /tmp/hfschedule-ext -source both -extra examples/hfschedule/eibi-extra-navtex-490-4209.txt -match 'Oostende NAVTEX,Portpatrick NAVTEX,Niton NAVTEX,Pinneberg NAVTEX,Cullercoats NAVTEX,Malin Head NAVTEX,Istanbul NAVTEX,Heraklion NAVTEX,Serapeum NAVTEX' -receivers "$OUT/receivers.json" -run -listen 570s -poll 15s -out "$OUT/run1" >> "$OUT/run.log" 2>&1 &
  child=$!
  child_start=$(date -u +%Y-%m-%dT%H:%M:%S)
  child_epoch=$(date -u +%s)
  pending=""
  pending_since=0
  live_pending=""
  live_pending_since=0
  while kill -0 "$child" 2>/dev/null; do
    sleep 30
    # heartbeat-freshness guard: a stale heartbeat means the whole
    # sandbox is frozen or just thawing. A frozen attempt is not wedged -
    # its slot_end watchdog fires at thaw. Only a runtime that is alive
    # (fresh heartbeat) while an attempt stays interrupted is a wedge.
    hb="$OUT/heartbeat"
    hb_age=99999
    [ -f "$hb" ] && hb_age=$(( $(date -u +%s) - $(date -u -r "$hb" +%s) ))
    if [ "$hb_age" -gt 90 ]; then
      pending=""; pending_since=0
      live_pending=""; live_pending_since=0
      continue
    fi
    wedged=$(python3 - "$STATE" "$child_start" <<'PY'
import json,sys,time
try:
    s=json.load(open(sys.argv[1]))
except Exception:
    sys.exit(0)
child_start=sys.argv[2]
horizon=time.strftime('%Y-%m-%dT%H:%M:%S', time.gmtime(time.time()-900))
for k,a in s.get('Attempts',{}).items():
    st=a.get('Started','')[:19]
    # only attempts this child actually started: stale "interrupted"
    # records from dead processes are not this process's to die for
    if a.get('Status')=='interrupted' and child_start <= st < horizon:
        print(k)
PY
)
    if [ -z "$wedged" ]; then
      pending=""; pending_since=0
    else
      now=$(date -u +%s)
      if [ "$pending" = "$wedged" ] && [ $(( now - pending_since )) -ge 30 ]; then
        # same attempt still stuck across >=30s of live runtime: wedge
        echo "$(date -u +%FT%TZ) backstop: attempt $wedged wedged >15min under live heartbeat; SIGQUIT pid $child" >> "$OUT/supervisor.log"
        kill -QUIT "$child" 2>/dev/null
        sleep 3
        kill -9 "$child" 2>/dev/null
        pending=""; pending_since=0
        live_pending=""; live_pending_since=0
        continue
      else
        pending="$wedged"; pending_since=$now
      fi
    fi
    # scheduler-liveness guard: slot due 3-6 min ago, no attempt record.
    live_due=$(python3 - "$STATE" "$SCHED" "$child_epoch" <<'PY'
import calendar,json,sys,time
state_path,sched_path=sys.argv[1],sys.argv[2]
child_epoch=float(sys.argv[3])
now=time.time()
# startup grace: a fresh child may legitimately have no records yet
if now-child_epoch < 120:
    sys.exit(0)
slots=[]
try:
    for line in open(sched_path):
        p=line.split()
        if len(p)<3:
            continue
        t=p[1]
        if len(t)!=9 or t[4]!='-':
            continue
        try:
            slots.append((int(t[0:2])*60+int(t[2:4]), int(t[5:7])*60+int(t[7:9])))
        except ValueError:
            continue
except Exception:
    sys.exit(0)
try:
    st=json.load(open(state_path))
except Exception:
    sys.exit(0)
gm=time.gmtime(now)
today=time.strftime('%Y%m%d', gm)
nowmin=gm.tm_hour*60+gm.tm_min+gm.tm_sec/60.0
keys=st.get('Attempts',{}).keys()
for smin,emin in slots:
    if smin+3 <= nowmin <= min(smin+6, emin):
        suffix='%sT%02d%02dZ' % (today, smin//60, smin%60)
        if any(k.endswith(suffix) for k in keys):
            continue
        # receiver failure cooldown: a benched receiver legitimately
        # starts nothing; never fire on it
        cool=False
        for mm in st.get('Metrics',{}).values():
            lf=mm.get('last_failure',''); ls=mm.get('last_success','')
            if lf and lf>ls:
                try:
                    ft=calendar.timegm(time.strptime(lf[:19],'%Y-%m-%dT%H:%M:%S'))
                    if now-ft < 3600:
                        cool=True
                except Exception:
                    pass
        if not cool:
            print(suffix)
PY
)
    if [ -z "$live_due" ]; then
      live_pending=""; live_pending_since=0
    else
      now=$(date -u +%s)
      if [ "$live_pending" = "$live_due" ] && [ $(( now - live_pending_since )) -ge 30 ]; then
        echo "$(date -u +%FT%TZ) liveness: slot $live_due due but no attempt created under live heartbeat; SIGQUIT pid $child" >> "$OUT/supervisor.log"
        kill -QUIT "$child" 2>/dev/null
        sleep 3
        kill -9 "$child" 2>/dev/null
        live_pending=""; live_pending_since=0
        pending=""; pending_since=0
        continue
      else
        live_pending="$live_due"; live_pending_since=$now
      fi
    fi
  done
  wait "$child"
  echo "$(date -u +%FT%TZ) hfschedule-ext exited rc=$?; restarting in 5s" >> "$OUT/supervisor.log"
  sleep 5
done
