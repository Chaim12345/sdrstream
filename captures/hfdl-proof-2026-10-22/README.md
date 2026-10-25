# HFDL verified-live proof, 2026-10-22 22:56-23:07 UTC

Receiver: Twente WebSDR (websdr.ewi.utwente.nl:8901), 5720.0 kHz USB
(Shannon HFDL night frequency; the 17:57Z hfdl.observer table still showed
Shannon on 8942 kHz, where nothing was heard - 5720 came from the repo's own
HF target list).

- twente_5720.wav (75 s, 22:56:22Z): 2 FCS-valid frames - ELY028 position,
  ACARS label SA for EC-MXV / IB0267.
- twente_5720_long.wav (360 s, 23:01:02Z): 3 FCS-valid frames - ETH741
  position, ACARS label Q0 for EC-ODH / UX0023, IBE030 position (embedded
  UTC 22:50:58 predates window; unverified).
- *.log: raw `sdrstream decode -mode hfdl` output.
- proof-ledger.jsonl: one row per frame with cross-check verdicts.

Cross-checks: adsb.lol live API at ~23:00-23:09Z. ELY028 30.7 NM downrange
in 3.4 min at 539 kt; ETH741 46.5 NM in 5.8 min at 486 kt; EC-MXV and
EC-ODH registration-exact live matches. IBE030 not visible (likely landed
Madrid) - left unverified.
