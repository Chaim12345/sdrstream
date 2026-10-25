# Live-gate NAVTEX decodes: L Pinneberg 490 kHz, 2026-10-25

Evidence delivered by the radio capture lane via main on 2026-10-25 (~20:11
IST) and integrated here verbatim. The lane reconciled its own tally against
proof-ledger.jsonl (25 entries for 2026-10-25) before delivery; the earlier
self-corrected totals (7-vs-9 captures) are superseded by the counts below.

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
- Missed slots (no ledger entry, neutral - VM frozen or service restarting):
  Pinneberg 0150Z; Heraklion 0300Z (backstop kill pre-guard); Portpatrick
  0420Z (same); Niton 0920Z (wedge kill); Oostende/Portpatrick 1210Z/1220Z
  (freeze; 1210Z wedged, backstop-killed); Niton 1320Z (service restart +
  freeze); Portpatrick 1620Z and Niton 1720Z (frozen through start;
  liveness guard correctly suppressed by stale heartbeat).

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

## Content caveat

Decoded maritime warnings/forecasts are captured over-the-air radio
evidence (with FSK corruption where flagged), not advice verified for
navigation. A live-gate pass is a framing/vocabulary verdict, not
independent content validation.
