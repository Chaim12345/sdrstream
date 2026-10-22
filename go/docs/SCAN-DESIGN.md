<!-- tm_board_write · 2026-10-21T15:36:55.576Z · role=architect · session=20261021-183655 -->
# SCAN-DESIGN — fleet-wide scanning architecture for sdrstream

Goal: "scan all stations, all frequencies; voice→transcribe, Morse→text, image→decode, etc."
Scale: ~1305 public receivers (directory.Station, stations.go:13) × 1..N profiles of ~1-2 MHz
(catalogue.Band, catalogue.go:8) = tens of thousands of ranges, each needing seconds of listening.
Today the engine watches ONE receiver: monitor.Monitor (monitor.go:103) drives one monitor.Source
(monitor.go:28) through one worker over a bounded jobs chan (monitor.go:105,196). This design
fans that one out to a polite fleet without monopolising any shared public receiver.

## 1. Architecture

**Scan item identity** — the unit of work is a struct `ScanItem{ReceiverID, Kind, ServerURL,
ProfileKey, CentreHz, SpanHz}`. ReceiverID = server URL; ProfileKey = the OpenWebRX profile key
(openwebrx.Client.ProfileList, openwebrx.go:577) or the catalogue.Band name for WebSDR/Kiwi.
CentreHz/SpanHz come from catalogue.Band (CenterKHz/SampleRateKHz, catalogue.go:8) or the probed
/status.json profiles (directory.probeOne, directory.go:131). A stable hash of the five fields is
the dedup/cooldown key (mirrors monitor.peakKey bucketing, monitor.go:606).

**Scheduler** (new `internal/scansched`) — a work queue over ScanItems with a persisted cursor.
It never holds more than one connection per receiver per pass; it rotates receivers round-robin so
no single server is polled back-to-back. It pulls the next due item (respecting per-receiver
cooldown/backoff), opens a monitor.Source via monitor.SourceFor (source.go:248), runs one survey
pass, and releases the connection. Reuses the existing Source interface unchanged.

**Worker pool** — three bounded pools, not one:
- *survey workers* (I/O-bound): each owns one open Source, runs decode.BandScanner.ScanRange
  (scanner.go:40) or the OpenWebRX spectrum path (openwebrx.FindSpectrumPeaks, openwebrx.go:395),
  and emits hits. Mostly network wait.
- *decode workers* (CPU-bound, pure Go): consume the hit queue, record + classify + decode.
- *whisper slot* (1, hard limit): voice.Transcribe (voice.go:161) behind a single-slot semaphore.

**Per-receiver politeness** (new `internal/politeness`):
- one connection per receiver per pass; connection closed after the pass (Source.Close).
- minimum interval between passes to the same receiver (default 60 s), enforced by the scheduler.
- client caps: read the server's own limit where exposed — kiwisdr Info.RXChannels (kiwisdr.go:82)
  and OpenWebRX config; never open more than one, and back off if a connect is refused.
- exponential backoff on connect/record failure (1s→2→4… capped 15 min, mirrors websdr ReconnectMax,
  websdr.go:75); after N consecutive failures the receiver is blacklisted until a reset time.
- retune in-stream rather than reconnecting: openWebRXSource.Listen already retunes on the live
  socket (source.go:132); the scheduler reuses that so a whole profile is surveyed on one connection.

**State persistence / resumability** (new `internal/scanstate`) — a JSON (later SQLite) file:
per-receiver {lastPassAt, consecutiveFails, blacklistUntil}; per-item {lastSurveyAt, lastHitAt}.
On restart the scheduler skips receivers in backoff and items surveyed within the cooldown, so a
crash resumes rather than restarts. The hit queue and results are append-only files (see §2), so
nothing already decoded is lost.

## 2. Decoding pipeline

**Fast pre-classifier / triage** (new `internal/triage`, wraps existing code): on a recorded
transmission run decode.Classify (scanner.go:74, spectral flatness) + voice.Modulated
(voice.go:373, envelope) and, for long recordings, decode.ScanTransmissions (scan.go:25) to cut
the strongest segment. Output is one of silence/noise/carrier/cw/data/voice — the same vocabulary
monitor.classify already uses (monitor.go:736). This gate exists so we never hand noise to an
expensive decoder (the lesson behind decode.go's data-first ordering, monitor.go:731).

**Routing per kind** (reuses monitor.decodeData, monitor.go:786, refactored into triage):
- voice → voice.SpeechActivity (voice.go:387); if speech present → whisper slot → voice.Transcribe.
- cw → decode.MorseDecode (morse.go:91), or multimon MORSE_CW.
- data/packet → decode.MultimonDecode(ctx,path,"AFSK1200") FEC-first (external.go:26), fallback
  decode.AFSKFrames (afsk.go:291) → AX.25.
- paging → decode.POCSAGDecode (pocsag.go:33) at 512/1200/2400.
- eas → decode.EASDecode; navtex → decode.NAVTEXDecode; dtmf → decode.DTMFDecode.
- rtty → decode.RTTYDecode; psk31 → decode.PSK31Decode.
- sstv → decode.SSTVDetect (sstv.go:46, VIS only) then NEW image decoder (§4) → PNG.

**Timeouts**: every pure-Go decoder under a 30 s context; whisper keeps its 10 min cap
(voice.go:195). A decoder that exceeds its timeout is abandoned, not retried.

**Partial / streaming**: recordings stay fixed-duration (seconds), matching Source.Listen today;
there is no true streaming decode — a long transmission is segmented by ScanTransmissions and the
highest-concentration segment is decoded. Stated as a bound, not built.

**Result storage**: file layout `<out>/<receiver>/<freq>/<ts>-<kind>.wav` plus a sidecar
`<ts>-<kind>.json` {kind, mode, detail, started, duration}. WAV via voice.WriteWAV (voice.go:293).
Start with JSON sidecars (fits web.scanCaptures, web.go:251); add a SQLite `results` table
(id, receiver, profile, centre, span, freq, kind, mode, detail, started, audio_path) only when
cross-fleet queries are needed — the web UI reads it for §5(g).

## 3. Honest bounds + numeric limits (8 cores, 24 GB RAM, 1 whisper stream)

**Must NOT attempt**: monopolising a shared receiver (holding a socket open indefinitely or
opening several connections to one server); thousands of concurrent connections (1305×N is not a
connection count, it is a rotation); running whisper on every hit; trying every decoder on every
hit (triage first). These are politeness + cost guards, not features to add later.

**Numeric limits** (defaults, all overridable by flag/env):
- *Receiver connections, global: 12.* Connections are I/O-bound (network wait dominates), so this
  does not saturate 8 cores; 12 keeps per-server load low (1 conn/receiver/pass) and stays far
  under any server's client cap. ~1.5 survey items/s at 8 s dwell.
- *Per receiver: 1 connection, ≥60 s between passes.* Politeness; matches public-receiver norms.
- *Pure-Go decode workers: 4.* Leaves cores for the whisper slot, scheduler and web; decoders are
  short (<1 s typical) so 4 drains the hit queue without starving transcription.
- *Whisper: 1 stream, threads capped at 3.* Hard constraint (one stream); 3 threads keeps ~4 cores
  free for decoders/IO. tiny.en ≈15 s per 25 s clip (HANDOFF), base.en ≈45 s — prefer tiny.en.
- *Memory*: one whisper model resident ≈3 GB (models 1.5-2.9 GB); in-flight audio is seconds at
  12 kHz (~0.8 MB each), so even 100 buffers ≈80 MB. Comfortably inside 24 GB.

## 4. Reuse analysis

**Reuse as-is**: monitor.Source + monitor.SourceFor (source.go:28,248) — the connection
abstraction, unchanged; openwebrx/kiwisdr/websdr clients (Tune/Record/RecordSamples/ProfileList);
decode.Classify, decode.BandScanner.ScanRange, decode.ScanTransmissions; every decoder in
internal/decode (AFSK/Morse/DTMF/POCSAG/EAS/NAVTEX/PSK31/RTTY/SSTVDetect) and decode.MultimonDecode;
voice.Transcribe/SpeechActivity/Modulated/Envelope/WriteWAV/Resample; catalogue.Band/Receiver/Table;
directory.Discover/ParseStations/FetchStations/ForFrequency; web.Server/SetMonitor/AddCapture/
writeJSON; monitor.Monitor.classify + decodeData (extract to triage, do not rewrite).

**New packages** (one line each):
- `internal/scanitem` — ScanItem type + stable hash + JSON (de)serialisation.
- `internal/scansched` — multi-receiver scheduler: work queue, round-robin, worker-pool wiring.
- `internal/politeness` — per-receiver cooldown, client-cap check, exponential backoff, blacklist.
- `internal/scanstate` — persisted cursors (per-receiver + per-item) for resumability.
- `internal/triage` — fast pre-classifier + per-kind routing (wraps decode + voice).
- `internal/sstv` — real SSTV image decoder: VIS (reuse sstvModes, sstv.go:25) + line/PD decode
  to PNG. New because decode.SSTVDetect is header-detection only.
- `internal/results` — result store: JSON sidecars now, SQLite table behind one interface later.

## 5. Vertical slices (build order — each independently demoable on THIS machine)

(a) **RSSI-only survey, many receivers, no recording.** Reuse directory.Discover + BandScanner.
    Demo: `sdrstream survey --receivers 5` prints a level table per receiver, no WAV written.
(b) **Survey→hit queue, one receiver.** Wrap one monitor.Source; Classify each step; enqueue hits.
    Demo: one receiver prints its hits with kind/level (no decode yet).
(c) **Per-hit recording + classification.** Extract monitor.classify into triage; record the hit,
    classify, save WAV. Demo: a hit becomes a saved WAV + a kind on stdout.
(d) **Per-kind decoding.** Route classified hits through decodeData. Demo: `decode -mode all` on a
    saved hit WAV shows the correct decoder firing (aprs/morse/pocsag/…).
(e) **SSTV image decode.** New internal/sstv. Demo: `sdrstream sstv tests/data/ref_sstv.wav out.png`
    writes a PNG; VIS mode printed from sstvModes.
(f) **Multi-receiver scheduler + politeness.** New scansched/politeness/scanstate. Demo: N receivers,
    1 conn each, a dead host backs off and is skipped, Ctrl-C then restart resumes from scanstate.
(g) **Web UI surfacing.** Extend web.Server with /api/scan.json fed by results. Demo: browser shows
    live per-receiver hits + decoded detail, reusing the existing activity/spectrum panels.

## 6. Test strategy per slice

Offline, deterministic, using committed fixtures in tests/data/ (ref_aprs/dtmf/eas/morse/pocsag/
sstv/psk31/navtex/rtty.wav + websdr_*.bin): each slice gets a table-driven test over the fixture
that matches its kind (e.g. triage routes ref_morse.wav→cw, ref_aprs.wav→data; sstv decodes
ref_sstv.wav to a non-empty PNG). Scheduler/politeness tests use a fake Source (a func-backed
Source, as monitor_test.go's wavSource does, monitor_test.go:59) to assert one-conn-per-receiver,
backoff, blacklist and resume-from-state with no network.

Live, env-gated, following kiwisdr/live_test.go:14 (KIWI_LIVE → t.Skip otherwise): add
OPENWEBR_LIVE / WEBSDR_LIVE gates so a real survey pass, a real retune-in-stream and a real
backoff-on-refusal can be verified against a public receiver when the operator opts in. Note
HANDOFF: `go test -race` cannot run here (sandbox TSan unsupported) — concurrency is reviewed by
hand and covered by the fake-Source tests, not -race.

## 7. Current status (2026-10-22)

Slices (a)–(g) landed in PR #4. The integration changes that followed:

* **OpenWebRX stations now survey their own profile span.** `scanStation`
  dispatches OpenWebRX to `scanOpenWebRXSpectrum`, which runs one monitor in
  `spectrum` strategy, reports each peak that survives a 2 s dwell, and saves
  audio only for voice/SSTV. A 10 s watchdog falls back to the fixed target
  list when the server sends no FFT.
* **Verified captures feed Rdio Scanner.** `internal/rdio` uploads the audio
  plus frequency/talkgroup metadata to `POST /api/call-upload`, so the archive
  and its UI come from an existing product instead of a second frontend.
  See `go/docs/RDIO.md`.
* **Receiver sources now live for the whole station session.** A connect-only
  context used to be cancelled right after `monitor.SourceFor` returned; the
  protocol clients run their websocket read loop on it, so no FFT or audio
  arrived after connect. Preflight keeps its own 10 s budget and the source
  uses the station context. The Kiwi type check also trusts the directory
  type instead of guessing from port 8073, which OpenWebRX shares.
* **Kiwi HF voice is transcribed, not just captured.** `decodeHF` leaves voice
  targets unverified by design; `scanKiwiStation` now reads the captured WAV,
  transcribes it with the NIM-first backend, applies the live gate, writes the
  WAV to the captures directory and publishes the transcript (which also
  reaches the Rdio hook). Shannon VOLMET 5505 USB is the first HF target with a
  25 s listen and was live-verified with a real METAR report. A repeat visit
  registers only the targets it will tune, so a decoded row survives the next
  "queued" transition; the activity pill counts these gallery decodes too.
* **Continuous roster instead of a frozen station list.** `scanRoster` keeps
  operator-supplied stations and directory discoveries in separate halves.
  `--scan-directory-refresh` (default 1 h, 0 disables) re-fetches
  receiverbook/the Kiwi directory and swaps only the dynamic half, so a
  hand-picked receiver survives a refresh. A failed or empty refresh keeps the
  previous list — losing the directory must not stop a fleet that is already
  scanning. Entries are deduped by `host:port`, and `+N/-N` is logged so
  roster churn is visible.
* **Rotation across visits, so later targets are not starved.** A Kiwi session
  stops at `--scan-target-limit`/`--scan-station-session`; the scanner now
  records the last HF target it actually processed and the next visit starts
  after it (`nextKiwiTargets`/`noteKiwiProgress`). OpenWebRX visits do the same
  with profiles: `ProfileNames()`/`ActiveProfile()` are learned on connect and
  `nextOpenWebRXProfile` advances one profile per visit, falling back to the
  default profile (and relearning) when a server drops or renames one.
* **Record-all archive.** `--scan-record-all` writes a WAV plus a `key=value`
  sidecar for every detected activity, including unverified decodes, carriers
  and detector noise, so rejected evidence is inspectable too. The monitor
  writes OpenWebRX clips before the noise/silence/unknown filter
  (`monitor.Options.OnCapture`); Kiwi/WebSDR/HF clips are written by the
  scanner. Sidecars carry frequency, mode, receiver, kind, target, gate,
  started, duration and verified, which `scanCaptures` reads back into the
  Gallery. Filenames include the station and a Unix-nanosecond stamp because
  monitor activity IDs reset per station. `--captures-max-gb` and
  `--captures-retain` run a retention loop; both default to unlimited.

Remaining:

* WebSDR and KiwiSDR have no spectrum in this client, so they still use fixed
  targets; a `decode.BandScanner` sweep over the advertised band is next.
* Rotation cursors live in memory, so a restart re-scans from the top of the
  HF target list / the default OpenWebRX profile. Persisting them next to the
  scan state would make coverage monotonic across restarts, and a per-station
  profile hint would let an operator pin the span deliberately.
* Record-all keeps everything by default. A long fleet run needs an operator
  to set `--captures-max-gb`/`--captures-retain`; there is no automatic
  budget yet, and the Gallery index is rebuilt from disk at startup.
