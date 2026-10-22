# Scheduled HF tuner

`go/cmd/hfschedule` is a separate opt-in process. It never calls the app API,
changes the running scanner, or opens a tunnel. No new Go dependencies.

## Run

```sh
. scripts/env.sh
cd go
go build -o /tmp/hfschedule ./cmd/hfschedule
# Plan only. Fetch each source once at startup, no directory scraping.
/tmp/hfschedule -source eibi -match 'DWD 1 Pinneberg' -horizon 2h
# One capture, with an explicit operator-selected receiver and range.
/tmp/hfschedule -source eibi -kind rtty -freq 10100.8 \
  -receivers ../examples/hfschedule/receivers.json -run -once \
  -listen 60s -out /tmp/hf-scheduled-captures
# To wait for future broadcasts, omit -once. Check the local schedule every 15s.
# Use -eibi /path/eibi.txt and -noaa /path/radiofax_update.txt for offline inputs.
/tmp/hfschedule -source noaa -kind wefax -receivers /path/receivers.json \
  -listen 10m -out /tmp/hf-noaa -run
```

Default is plan-only. `-once` attempts at most one *currently active* service,
not the next future service. Filters: station/product substring, kind, exact
carrier kHz. Broadcast entries use AM unless explicitly USB/LSB. Known DWD RTTY,
NAVTEX and WEFAX entries tune appropriate USB offsets and call the existing
radio decoders. Broadcast voice is recorded, not transcribed, and cannot pass
the decode gate. Other utility codes are not treated as AM broadcasts.

## RTTY cross-verification (verified anchor)

A recorded RTTY capture is only marked **verified** when an independent
reference decoder agrees with the Go decode on the same audio. The known
failure mode this guards is readable-looking false text from a strong
carrier: one engine hallucinating is plausible, two unrelated
demodulators producing the same station anchor and lines is not.

* Reference engine: minimodem (`go/internal/decode/rttyref.go`), run as an
  unmodified child process on the saved WAV. multimon-ng ships no RTTY
  demodulator and fldigi has no non-interactive WAV batch mode, so neither
  can fill this role. Binary resolution: `SDRSTREAM_REF_MINIMODEM`,
  `SDRSTREAM_REFDEC_DIR` (`<dir>/usr/bin/minimodem`), then `$PATH`;
  extracted-package library dirs are added to the child's
  `LD_LIBRARY_PATH` only. `bash ../scripts/refdec.sh` installs the
  extracted-package layout without root.
* Verdict (`RTTYAgreement`): verified requires non-trivial reference text
  AND the expected anchor (DDK2/DDH7/DDK9) in both engines, or at least
  one 40-character reference line whose 8-char shingles land at >=80%
  inside the Go text. Disagreement, an empty reference, or a missing
  binary leave the capture unverified with the reason recorded.
* Artifacts per capture: `<key>.wav` (audio), `<key>.txt` (Go decode),
  `<key>.ref.txt` (reference decode), `<key>.json` (audit with verified
  fields), and the proof-ledger line (`verified`, `verified_lines`,
  `verified_reason`, `ref_engine`, `ref_text`).
* `hfschedule -report -out <dir>` prints ledger statistics: window hours,
  capture tallies, verified captures, verified lines and verified
  lines/hour, plus reject reasons.
* `examples/hfschedule/dwd-rtty-hourly.txt` splits the three continuous
  DWD RTTY frequencies into hourly windows so each service is re-captured
  every hour:

```sh
SDRSTREAM_REF_MINIMODEM=$HOME/refdec/root/usr/bin/minimodem \
/tmp/hfschedule -source eibi -eibi ../examples/hfschedule/dwd-rtty-hourly.txt \
  -match 'DWD 1 Pinneberg' -kind rtty \
  -receivers ../examples/hfschedule/receivers-health.json \
  -listen 5m -out /tmp/rtty-verified -run
/tmp/hfschedule -report -out /tmp/rtty-verified
```

## Source and UTC semantics

* EiBi: http://eibispace.de/dx/eibi.txt, fixed-width frequency schedule, Latin-1.
  Source format: http://eibispace.de/dx/README.TXT. Its header permits copying and
  distribution. Schedule validity dates are enforced, including an inclusive
  final date. Numeric weekdays use Monday=1. Simple weekday names, one-off
  dates, midnight wrapping and 2400 are supported. Complex/irregular/alternate
  day rules fail closed: they do not trigger recordings.
* NOAA Boston NMF: https://ocean.weather.gov/radiofax_update.txt. Re-verified
  2026-10-22: the hosted file header still reads "Valid 19 September 2018 /
  Updated 7 August 2018", and it remains the current published Boston schedule
  (the live schedule page https://ocean.weather.gov/shtml/atlsch.php carries
  the same 19 September 2018 date with current chart timestamps), so the
  apparent staleness is the upstream's own. Startup logs the actual header
  dates. NWS Public Information Statement 26-48 (2026-06-09) proposes
  discontinuing all HF radiofax broadcasts, Boston NMF included, towards the
  end of 2026: treat the timetable as a planning source, never proof that any
  chart will be broadcast today. The parser respects per-frequency UTC windows,
  paired chart start times, 120 RPM/576 IOC and the next-chart boundary.
  It does not claim support for other NOAA stations or NAVTEX timetables.
  NOAA NAVTEX entries come from EiBi when present.
* All scheduling uses UTC, independent of the machine timezone. Overnight day
  rules belong to the transmission's start day. Captures end no later than the
  listed transmission end. NOAA chart durations are inferred from the next
  listed chart and capped at 20 minutes; there is no claim of exact chart length.

Sources are downloaded once, max 4 MiB, with a 20s timeout. No periodic host
polling. Restart with a newly downloaded schedule for a new season. An expired
EiBi feed will produce no jobs. The long-running process does not auto-refresh
sources. The parser refuses HTML/CAPTCHA pages and empty schedules.

## Receiver use and safety

Receiver JSON is an explicit array of URL, Type (`kiwi` or `websdr`), MinKHz,
MaxKHz and Priority. Lower priority wins; ties sort by URL. These are the
operator's chosen ranges, not an RF propagation forecast. Select a receiver
near the transmitter/target region and verify its current range. No external
receiver directory is polled. Only one connection/capture at a time, max 20m.
A receiver connection error backs that host off for one hour; failed jobs are
eligible after one minute so another configured receiver can be used. Reconnect
is disabled. The process closes each receiver after capture. Short/silent audio
is not marked as a valid recording/decode. Do not run this against a host that
forbids automated reception.

`-extra /path` merges a local supplemental EiBi-format file (with its own
validity header) into the schedule; `examples/hfschedule/eibi-extra.txt` adds
NAVTEX 518 kHz NAVAREA I slots (times from the ITU NAVTEX station list,
cross-checked against the Wikipedia station list), hourly DWD 4583 windows and
time-signal carrier windows. `-match` accepts comma-separated substrings.

State and sidecars are private files under `-out`: `state.json`, per-occurrence
JSON, PCM WAV, text and optional PNG. Every completed attempt also appends one
JSON line to `proof-ledger.jsonl` (capture key, target, receiver, UTC window,
status, decode-gate verdict and reason, artifact paths); a ledger write error
stops the command rather than silently losing proof. A UTC occurrence key prevents duplicate
captures across restarts. State writes are atomic; corrupt state fails closed.
An in-flight attempt is saved before connecting and is not replayed after a
crash. `scheduler.lock` prevents concurrent access to the same store. After an
unclean exit, first confirm no scheduler uses the store, then remove the stale
lock directory manually. Restart with a different output directory to recover
an intentionally interrupted occurrence. State is pruned after seven days.
A disk error stops the command rather than silently losing capture proof.

## Evidence (2026-10-22)

Live EiBi A26 fetched, header updated Oct 19, valid Mar 29 through Oct 25.
Scheduled DWD 10100.8 kHz RTTY was captured via Twente at 06:49:10 UTC, dial
10098.9 USB, 60s: 427392 samples at 7119 Hz, peak 17874, RMS 2670.15. Decoder
text includes `CQ CQ CQ ... DH7 DDK9` and the existing gate accepts `DDK9`.
The text remains noisy/partly corrupt: this proves scheduled tuning, real audio,
a decoder invocation and callsign recovery, not a clean weather bulletin.
A prior 7646 kHz capture was real audio but failed its decode gate. Neither
NOAA WEFAX nor NAVTEX live reception was verified in this run; their scheduling
and dispatch are covered by offline tests, not passed off as real decodes.
