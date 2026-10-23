# HF continuous-capture run1 - deployment handoff (paused 2026-10-23 03:47 UTC)

Operational pause pending sustainable hosting. Not a cancellation. All state
preserved; no metrics were reset and no cooldowns cleared.

## Verified results (run1, 2026-10-22 20:40 - 2026-10-23 03:40 UTC)

59 ledger attempts over ~7 wall hours under a sandbox that suspends between
agent wakes: 45 recorded, 14 failed. **17 verified live decodes** (gate-passed
at capture time): 11x NAVTEX 518 kHz (Gislovshammar, Niton, Rogaland, Jeloy,
Orlandet, Portpatrick, Oostende, Malin Head) and 6x DWD Pinneberg RTTY
4583 kHz (anchor DDK2/DDH7/DDK9; 03:05 capture two-engine cross-verified
against minimodem, the strongest evidence class). Proof ledger:
run1/proof-ledger.jsonl; audio + decode text per attempt in run1/<key>.*.

Gate rejects (recorded, unverified): 10x "service anchor missing" (weak or
idle NAVTEX slots), 8x WEFAX no tone lock, 5x too short, 4x no-backend, 1x
low text quality. Failures: 6x TCP i/o timeout, 5x context deadline/cancel,
3x short capture - the post-02:50 ones are sandbox-stall-associated.

## How to resume

Branch `run/hfschedule-continuous` @ 3cd59b4 (binary deployed was ee295cc +
test-only 3cd59b4). Toolchain: `. scripts/env.sh` (private Go in ~/toolchain;
system go shim is broken). Launch:

```
SDRSTREAM_REFDEC_DIR=/home/sandbox/refdec/root \
  /tmp/hfschedule -source both \
  -extra examples/hfschedule/eibi-extra.txt \
  -match 'DWD 1 Pinneberg RTTY,DWD Pinneberg Met Fax,NAVTEX,RWM,WWV' \
  -receivers examples/hfschedule/receivers-health.json \
  -run -listen 240s -poll 15s -out /home/sandbox/hf/run1
```

supervise.sh (while-true restart) and heartbeat.sh wrap it; rebuild with
`cd go && go build ./cmd/hfschedule`. minimodem 0.24 (DWD cross-check) lives
in /home/sandbox/refdec/root (scripts/refdec.sh).

## Receiver state at pause (do not clear silently)

Twente WebSDR benched until ~04:15 UTC (consec 1, stall-shortened RWM);
kiwisdr.johnnorton.co.uk:8073 benched until ~05:35 UTC (consec 2, stall-
associated TCP timeouts). Benches expire on their own.

## Known limitations (honest labels)

- Sandbox suspends between agent wakes; captures spanning a suspend die on
  transport deadlines and currently count as receiver faults, so unattended
  operation self-benches within ~30 min. A 25-min agent-present window
  (02:50-03:15 UTC) completed 3/3 captures cleanly, 2 verified.
- Wall-clock watchdog (804744a) bounds post-resume overrun; which of
  watchdog vs transport deadline lands the error is not instrumented.
- NOAA Boston radiofax timetable still "Valid 19 September 2018" (operative,
  PNS 26-48 proposes ending HF radiofax end of 2026); EiBi A26 valid to
  2026-10-25. Sources in docs/hf-scheduler.md.
- Open: watchdog validation under a real suspend; sustainable hosting
  decision; stall-vs-receiver-fault attribution.

## 490/4209.5 kHz expansion (separate lane, held)

Recovery branch `feat/navtex-ext-490-4209-recovery` @ 409c0d0: expansion
code (bc0049a) + failure-classification cherry-pick + recovered evidence in
docs/recovery/navtex-ext-490-4209/. Original run's WAV/ledger files are
INACCESSIBLE (private sandbox of a failed agent; destruction not
established). Replacement listener built but NOT launched: the old
benched-but-possibly-alive service cannot be confirmed stopped. Expansion
results are by-content only - no live-gate-verified 490/4209.5 decode was
ever achieved.
