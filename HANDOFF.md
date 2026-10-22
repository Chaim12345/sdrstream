# Handoff

## Current state (2026-10-22)

Branch `master`, fast-forwarded to PR #4 (`4673478` "Team integration"), which
brought the static web UI, auth, SSTV image decoding, WEFAX/RTTY/HF decoders,
the voice guard, the verify gate and the fleet scanner. On top of that this
session added:

* **Rdio Scanner output** (`go/internal/rdio`) — verified live captures with
  audio are uploaded to `POST /api/call-upload`, so the fleet can feed a real
  scanner archive/UI. Wired into `monitor` (`-rdio`, `-rdio-key`,
  `-rdio-system`) and into the web scanner through `web.Server.SetRdioHook`.
  See `go/docs/RDIO.md`.
* **Hosted ASR preferred** — `DefaultNIMOptions` now accepts `NVIDIA_API_KEY`
  (the key exported on this host) as well as `SDRSTREAM_NIM_API_KEY`.
  `TranscribeBest` already tries hosted whisper-large-v3 first and falls back
  to local whisper.cpp. Live-verified against `grpc.nvcf.nvidia.com`.
* **One local whisper at a time** — a package-level slot serialises local
  whisper.cpp runs, so a 49–100 station fleet scan cannot launch dozens of
  3 GB large-v3 processes. Tested with a fake binary.
* **Voice CLI flags** — `--voice-lang/threads/beam/prompt` (and `monitor-*`
  variants) with a built-in radio phraseology prompt.
* **OpenWebRX surveys its whole profile** — the web fleet scanner now uses
  each OpenWebRX receiver's own FFT (`scanOpenWebRXSpectrum`) instead of 20
  blind frequencies, with a 10 s FFT watchdog and a fallback to the fixed
  target list for old servers/profiles. Audio is archived only for voice and
  SSTV (`monitor.Options.SaveAudio`), so a wide survey does not fill the disk.
* **Live false-positive fixes** — the aviation PoC decoded a controller as
  RTTY, and Shannon VOLMET was called a 0 kHz carrier. POCSAG pages with no
  text are ignored; Morse/RTTY/PSK31 output must contain an identifier
  (CQ, a callsign, NAVTEX framing); speech detection now runs before the
  narrow-signal carrier rule; and `SpeechCoverage` uses the hysteresis VAD
  so continuous SSB voice passes the hallucination guard.
* **Fleet-scan source lifetime fixed** — `scanStation` created each receiver
  with a 10 s connect context and cancelled it after connecting. The
  OpenWebRX/WebSDR/Kiwi clients run their websocket read loop on that
  context, so the FFT and audio stopped immediately after a successful
  connection. The source now lives for the whole station session. The Kiwi
  heuristic also no longer claims every `:8073` host (OpenWebRX's default
  port), and `kiwisdr` preflight accepts full URLs instead of building
  `http://http//host/status`.
* `AGENTS.md` records the standing directives (personal use: licensing is not
  a gate; prefer NIM over local).

Build and tests pass for the touched packages (`voice`, `rdio`, `web`,
`monitor`, `decode`, `cmd`). Live-verified: Shannon VOLMET on 5505 kHz USB
(`websdr.ewi.utwente.nl`) transcribes through NIM to "G-point 16, QMH 1025…
Wind 200 degrees, 7 knots…"; the OpenWebRX spectrum survey at
`sdr.ladar.club` detects and classifies peaks with no invented RTTY.

## What the tool does

`sdrstream` records audio from public SDR receivers and works out what is on
the air. The Go build is the maintained one; the Python package is the
original port source.

* **Receivers** — WebSDR, OpenWebRX and KiwiSDR clients, live-verified.
* **Monitor** (`go/internal/monitor`) — watches one receiver for any
  transmission: data goes to the packet/paging decoders, voice is transcribed,
  images are reconstructed.
* **Fleet scanner** (`web --scan-open-directory`, `--scan-kiwi-directory`) —
  bounded parallel scan of receiverbook stations with a 49→100 concurrency
  ramp, load guard, per-host blocklist/backoff, and verified results into the
  gallery. It currently tunes a fixed target list (amateur/aviation/marine/FM)
  per station.
* **Decoders** (`go/internal/decode`) — AFSK/AX.25, Morse, DTMF, RTTY, PSK31,
  SSTV (VIS + Martin image decode), POCSAG, FLEX, EAS/SAME, NAVTEX, ACARS,
  HFDL, FT8/WSPR, APT, plus the transmission scanner.
* **Speech** (`go/internal/voice`) — hosted NVIDIA NIM first, local
  whisper.cpp large-v3 fallback, transcript guard against hallucination.
* **Gallery / web UI** — static frontend, map, live scan view, fixtures, auth;
  verified live artifacts only.
* **Stations** (`go/internal/directory`) — every public receiver in the
  receiverbook directory.

## Running it

```bash
cd go
go build ./...
go test ./...                       # live tests skip unless env-gated
export NVIDIA_API_KEY=...           # hosted ASR (preferred)
go run ./cmd/sdrstream web --host 0.0.0.0 --port 8000 \
    --captures-dir ../captures \
    --scan-open-directory --scan-start-connections 49 \
    --rdio http://127.0.0.1:3000 --rdio-key "$RDIO_KEY"
```

Single receiver, spectrum strategy:

```bash
go run ./cmd/sdrstream monitor \
  --server http://sdr.ladar.club:8073/ --kind openwebrx \
  --profile "D5 Man/Liv/Bpool Airports 118-120MHz" \
  --strategy spectrum --listen 8s --voice
```

## Known problems / next work

* **OpenWebRX no longer uses the fixed target list**; WebSDR/Kiwi still do
  (they send no spectrum in this client). The next improvement there is a
  band sweep (`decode.BandScanner`) over each station's advertised band, and
  a per-station profile choice for OpenWebRX (the fleet scanner connects
  without selecting a profile, so it surveys whatever the server defaults to).
* **SSTV image decode covers Martin M1/M2, Robot36 and Scottie S1**; the
  other VIS modes fall back to header-only detection.
* **The hosted ASR is fluent but can hallucinate on weak audio** (a real ATC
  clip came back as "I'm going to go ahead and turn it off"). The transcript
  guard is the defence; treat short/weak clips as unverified.
* **SatNOGS API is often unreachable from this sandbox**; the gallery retries
  on a timer.
* **The sweep never finishes a large band on a slow link** (receiver settle
  time dominates); prefer the OpenWebRX spectrum strategy.
* `go test -race` cannot run in this sandbox (TSan unsupported).

## Environment notes

* x86_64 Linux, 8 cores, ~24 GB RAM, root.
* whisper.cpp v1.8.3 + `ggml-large-v3.bin` in `~/.cache/sdrstream/whisper/`.
* faster-whisper 1.2.1 in `~/.cache/sdrstream/fw/.venv` (not wired in).
* Python has no working `pip`; wheels are unpacked by hand.
* `NVIDIA_API_KEY` is exported; NIM gRPC endpoint is reachable.
