# hfschedule-supervise.sh - supervisor with an outside-the-runtime liveness backstop

Proven in the NAVTEX 490/4209.5 recovery lane, 2026-10-24/25. Written for the
sandbox layout in the script (paths are absolute; adjust for other lanes).

## Why it exists: the freeze/thaw timer wedge

The sandbox suspends the whole container between agent activity and resumes
it later (behavior consistent with checkpoint/restore: long-idle processes
reappear with new pids, parented to init). After some resumes, Go runtime
timer and select wakeups are permanently lost: a goroutine parked in
`select` on ctx.Done()+timer+channels stays parked even after every case is
ready (observed: goroutine 1 "[select, 4 minutes]" in websdr.Client.Record
with the wall-clock cap minutes in the past; same signature on the earlier
expansion run, "[select, 7+ minutes]"). The in-process wall-clock watchdog
(bc0049a) cannot rescue this: its own ticker is wedged by the same loss.
Only SIGQUIT/SIGKILL clears the process.

## What it does

1. Restarts hfschedule-ext on any exit (5s pause, logged).
2. Removes a stale mkdir scheduler.lock when no hfschedule-ext is running
   (SIGQUITed processes leave it behind; otherwise every restart exit-loops).
3. Liveness backstop, in shell, outside the Go runtime: every 30s it reads
   state.json and SIGQUITs (then SIGKILLs) the child when one of ITS OWN
   attempts is still "interrupted" 15 minutes after start (570s listen +
   margin). The dump lands in run.log for diagnosis.

## The launch-timestamp guard (hard-won)

First version treated any stale "interrupted" record as a wedged attempt of
the live child. Killed processes leave such records behind (the runner keeps
them for retry), so the backstop SIGQUITed two healthy services in a row.
Fix: the supervisor records the child's launch time and only watches
attempts Started at or after it. A process only dies for its own captures.
