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

## Source and UTC semantics

* EiBi: http://eibispace.de/dx/eibi.txt, fixed-width frequency schedule, Latin-1.
  Source format: http://eibispace.de/dx/README.TXT. Its header permits copying and
  distribution. Schedule validity dates are enforced, including an inclusive
  final date. Numeric weekdays use Monday=1. Simple weekday names, one-off
  dates, midnight wrapping and 2400 are supported. Complex/irregular/alternate
  day rules fail closed: they do not trigger recordings.
* NOAA Boston NMF: https://ocean.weather.gov/radiofax_update.txt. The official
  feed still says valid September 2018 when fetched on 2026-10-22. A warning is
  printed every startup. This is a planning source, not proof that any chart
  will be broadcast today. The parser respects per-frequency UTC windows,
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

State and sidecars are private files under `-out`: `state.json`, per-occurrence
JSON, PCM WAV, text and optional PNG. A UTC occurrence key prevents duplicate
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
