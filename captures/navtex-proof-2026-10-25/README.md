# Live-gate NAVTEX decodes: L Pinneberg 490 kHz, 2026-10-25

Evidence delivered by the radio capture lane via main on 2026-10-25 (~20:11
IST) and integrated here verbatim. The lane reconciled its own tally against
proof-ledger.jsonl before delivery; the earlier self-corrected totals
(7-vs-9 captures, then 9-then-10 missed slots) are superseded by the
script-derived reconciliation below (cutoff 18:10 UTC).

## Ledger reconciliation (lane-verified, 2026-10-25)

- A) Pre-turn-hold freeze regime (00:00-08:30 UTC): 15 ledger entries, ALL
  status=failed / "context canceled" (slot_end kills; attempts started 1-6
  min late, cut at slot end). Neutral class; no decodes; metrics untouched
  per the lane's classification rules.
- B) Turn-held windows (09:50-18:10 UTC): 10 ledger entries = 9 full
  captures (status=recorded, complete 4.05M-sample / 570s audio,
  listen-timer finishes, starts 0-7s after slot open) + 1 interrupted
  (Oostende 1610Z, context canceled 16:15:27Z by the lane's own approved
  redeploy TERM - own shutdown, neutral).
- Of the 9 full captures: 3 live-gate decodes (below) + 6 honest no-decodes
  (full capture, no gate pass, WAV only, no readable text): Istanbul 1000Z
  "too short", Serapeum 1150Z "too short", Malin Head 1200Z "too short",
  Istanbul 1400Z "too short", Malin Head 1600Z "too short", Istanbul 1800Z
  "expected service anchor missing" (gate heard framing-like content, no
  station/service anchor; receiver healthy - honest no-decode, not a
  service fault).
- Missed slots (no ledger entry) - 15 individual slots at/before the
  18:10 UTC cutoff, script-derived:
  - Attempt started, killed/frozen, state-record-only (6): Pinneberg
    0150Z; Heraklion 0300Z (backstop kill pre-guard); Portpatrick 0420Z
    (same); Malin Head 0800Z (true wedge; SIGQUIT 08:16Z, saved state
    2de603dd per the lane); Niton 0920Z (true wedge; SIGQUIT 09:36Z,
    0fceb85a per the lane); Oostende 1210Z (true wedge; SIGQUIT 12:38Z,
    ebbd0c5c per the lane).
  - No attempt at all, VM frozen/restarting (9): Oostende 0810Z;
    Heraklion 1100Z; Cullercoats 1120Z; Portpatrick 1220Z; Niton 1320Z;
    Heraklion 1500Z; Cullercoats 1520Z; Portpatrick 1620Z; Niton 1720Z
    (freeze/service restart; liveness guard correctly suppressed by stale
    heartbeat where applicable).
  The lane's earlier 9- and 10-slot hand counts were wrong (they missed
  0810Z and the 1100/1120/1500/1520 cluster); the script-derived 15
  supersedes both. The retracted 15:50 Serapeum "wedge" is not counted
  anywhere: the schedule has no 15:50 Serapeum slot (confirmed below).

### Reconciliation provenance (what is verified vs lane-reported)

Method (lane's reconcile-navtex-20261025 script, transferred with its
output and inspected at integration): schedule-file slots vs
proof-ledger.jsonl keys vs state.json attempt records, cutoff 18:10 UTC.
Arithmetic in the output is internally consistent: 15 failed
(context-canceled pre-hold) + 9 recorded + 1 interrupted = 25 ledgered;
40 due - 25 ledgered = 15 missing.

- VERIFIED HERE: 40 slots due at/before 18:10Z, recomputed independently
  from the committed schedule file
  (examples/hfschedule/eibi-extra-navtex-490-4209.txt) with equivalent
  parsing - matches the lane's 40 and its full 40-row per-slot table.
  Also verified: Serapeum appears only at 0750 and 1150 UTC (no 15:50 or
  19:50 slot), consistent with the lane's wedge retraction.
- VERIFIED HERE (against snapshots): the lane transferred its
  proof-ledger.jsonl and state.json, snapshotted 2026-10-25T20:12:29Z.
  Rerunning the same reconciliation script locally against those
  snapshots and the committed schedule reproduces the lane's output
  exactly, from the per-slot table through the 40/25/15 totals and the
  after-cutoff section. Spot checks: 26 ledger rows for today (25
  at/before cutoff + the 2000Z after-cutoff record); statuses and gate
  reasons match the table; the 6 state-record-only slots (0150, 0300,
  0420, 0800, 0920, 1210) all carry state Status=interrupted; the 9
  no-attempt slots are absent from state. Caveat: snapshots are post-facto
  copies of the lane's files - they verify the reconciliation as of
  20:12:29Z, not the original runtime provenance of each record.

### After the cutoff (kept separate, NOT in the totals above)

Malin Head 2000Z: full capture (20:00:03-20:09:36Z, listen-timer finish),
decode_live=false "too short" - honest no-decode, lane-reported via its
reconciliation output's after-cutoff section. Not part of the day's
reconciled 40/25/15.

Scope honesty: the 3-of-9 decode ratio covers the held windows only. It is
NOT an all-day reliability figure - the 15 pre-hold failures and the missed
slots above are part of the same day and are listed here deliberately.

## Live-gate decodes (decode_live=true, independently readable NAVTEX text)

All L Pinneberg 490 kHz via Twente WebSDR.

| File | Window (UTC) | Lane key | Gate verdict | Content |
| --- | --- | --- | --- | --- |
| proof-490-pinneberg-20261025T0950Z.txt | 09:50-09:59 | 8f06b9337aab034b-20261025T0950Z | "NAVTEX framing + 7 maritime words" | ZCZC LB90 gale/storm warning Baltic; ZCZC LA54 naut. warn. 598; ZCZC LA52 naut. warn. 592 (NAVTEX-HAMBURG/NCC, German) |
| proof-490-pinneberg-20261025T1350Z.txt | 13:50-13:59 | 28e0d32670f4bd3e-20261025T1350Z | "framing + 5 maritime words" | ZCZC LB93 gale/storm warning Deutsche Bucht (W 8-9 easing 7); ZCZC LA42 naut. warn. 582 (Norderney lighthouse characteristic change) |
| proof-490-pinneberg-20261025T1750Z.txt | 17:50-17:59 | 904ad8be5b631d67-20261025T1750Z | "framing + 7 maritime words" | ZCZC LA52 naut. warn. 592 (Darsser Ort lighthouse extinguished); ZCZC LE70 Sunday sea-weather forecast |

WEAK-SIGNAL CORRUPTION: the 1750Z copy contains FSK corruption ("UTCUTC",
"WARNRM", "F?R", "S?DWEST" and similar). Framing and the maritime-word gate
still passed. Any quotation of that file's strings must carry this caveat.
The 1350Z copy also shows minor corruption ("UTCUWKT", "NOERDLIL",
"ANKERN C FISCHEN").

## Audio

WAVs are NOT committed (8,116,012 bytes each; this repo just removed a
10 MB binary and keeps evidence text-only). The lane retains them. The two
transferred WAVs (0950Z, 1750Z) were metadata-verified at integration time
by reading the headers: 7119 Hz, 16-bit, mono, 4,057,984 frames = 570.0 s
- matches the lane's claim. They are raw demodulated audio; many players
need the 7119 Hz rate specified explicitly. The 1350Z WAV was not
transferred (attachment cap); available from the lane on request.

## Date headers

Broadcast headers in these files are preserved raw. The lane's reading,
from internal consistency of the decoded text itself, is the format
DDHHMM UTC MON YY:

- Transmission stamps "250950", "251350", "251750 NAVTEX-HAMBURG (NCC)"
  match the capture day (25th) and the exact slot times - an empirical
  anchor for DDHHMM.
- "150945 UTC SEP 26" in the 1350Z file is a still-repeated warning; read
  as 15 Sep 2026 09:45 UTC, the trailing "SEP 26" is month + 2-digit year,
  which makes "OKT 26" = October 2026 in the same position.
- On that reading: "241915 UTC OKT 26" = issued 24 Oct 2026 19:15 UTC,
  and "NACHRICHT WIRD AUFGEHOBEN 261100 UTC OKT 26" = cancellation
  26 Oct 2026 11:00 UTC - the next-day part comes from the leading DD=26
  token, not from "OKT 26". LE70 "VORHERSAGE FUER SONNTAG" is a forecast
  for Sunday, the capture day itself, not the next day.

This is an interpretation from internal consistency, NOT independent
verification against the NAVTEX standard or the NAVTEX-HAMBURG broadcast
schedule. Corrupt headers stay raw with uncertainty: "160500 UTCUWKT 26"
(garbled - do not assert), "211230 UTCUTC OKT 26" (duplicated UTC token),
and "WARNUNG NR. 90 250435UTC OCT" / "NR. 175 251030UTC OCT" carry no
year token. Reading the decoded text establishes text readability only,
not validation of broadcast content or dates.

## Content caveat

Decoded maritime warnings/forecasts are captured over-the-air radio
evidence (with FSK corruption where flagged), not advice verified for
navigation. A live-gate pass is a framing/vocabulary verdict, not
independent content validation.
