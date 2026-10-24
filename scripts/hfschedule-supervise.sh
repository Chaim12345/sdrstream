#!/bin/sh
# Supervisor for the NAVTEX 490/4209.5 recovery lane.
# - restarts hfschedule-ext on any exit
# - removes a stale scheduler.lock when no hfschedule-ext is running
# - liveness backstop OUTSIDE the Go runtime: a capture attempt still
#   marked in-flight ("interrupted") 15 min after its start is wedged
#   (post-freeze timer/select loss); SIGQUIT for a dump, then SIGKILL.
cd /home/sandbox/sdrstream-ext || exit 1
OUT=/home/sandbox/navtex-ext
STATE=$OUT/run1/state.json
LOCK=$OUT/run1/scheduler.lock
while true; do
  if ! pgrep -f 'hfschedule-ext -source' >/dev/null 2>&1 && [ -d "$LOCK" ]; then
    rmdir "$LOCK" 2>/dev/null && echo "$(date -u +%FT%TZ) removed stale scheduler.lock" >> "$OUT/supervisor.log"
  fi
  /tmp/hfschedule-ext -source both -extra examples/hfschedule/eibi-extra-navtex-490-4209.txt -match 'Oostende NAVTEX,Portpatrick NAVTEX,Niton NAVTEX,Pinneberg NAVTEX,Cullercoats NAVTEX,Malin Head NAVTEX,Istanbul NAVTEX,Heraklion NAVTEX,Serapeum NAVTEX' -receivers "$OUT/receivers.json" -run -listen 570s -poll 15s -out "$OUT/run1" >> "$OUT/run.log" 2>&1 &
  child=$!
  child_start=$(date -u +%Y-%m-%dT%H:%M:%S)
  while kill -0 "$child" 2>/dev/null; do
    sleep 30
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
    if [ -n "$wedged" ]; then
      echo "$(date -u +%FT%TZ) backstop: attempt $wedged wedged >15min; SIGQUIT pid $child" >> "$OUT/supervisor.log"
      kill -QUIT "$child" 2>/dev/null
      sleep 3
      kill -9 "$child" 2>/dev/null
    fi
  done
  wait "$child"
  echo "$(date -u +%FT%TZ) hfschedule-ext exited rc=$?; restarting in 5s" >> "$OUT/supervisor.log"
  sleep 5
done
