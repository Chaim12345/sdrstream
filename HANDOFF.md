# Handoff

This file describes the code on the `sdrstream-team-integration` line.
Older text below was written on `master` (commit `a8a092e`) and parts of it
are historical: check `git log` before trusting a status claim. Whether the
app is running is a deployment fact, not something this file can state.

## Health, metrics and the proof ledger

* `GET /healthz` - unauthenticated liveness probe, returns only `{"ok":true}`.
* `GET /api/metrics.json` (login) - uptime, capture count, scan outcomes and
  per feed source: state, items now, new items in the last hour and since
  boot. Counts are first sightings seen by this process and reset on restart.
* `GET /api/ledger.json` and `/api/ledger.csv` (login, `?source=`, `?limit=`)
  - append-only record of every first sighting: provider time, our fetch
  time, who decoded it (`provider` for every current feed) and a link to the
  provider. It is sdrstream's normalised view, not the raw upstream bytes.
  File: `sdrstream-ledger.jsonl` in the state dir, rotated at 16 MiB.
* Each feed card in the UI has a "proof" toggle with the same fields.

## What the tool does

`sdrstream` records audio from public SDR receivers and works out what is on
the air. The Go build is the maintained one; the Python package is the
original port source.

* **Receivers** — WebSDR, OpenWebRX and KiwiSDR clients, all live-verified.
  `go/internal/kiwisdr` is the newest of them.
* **Monitor** (`go/internal/monitor`) — watches a receiver for *any*
  transmission, not only satellites, and decides what each one is: data is
  handed to the packet and paging decoders, voice is transcribed.
* **Decoders** (`go/internal/decode`) — AFSK/AX.25, Morse, DTMF, RTTY, PSK31,
  SSTV (VIS), POCSAG, EAS/SAME, NAVTEX, plus a scanner that classifies a
  recording as voice, data, CW or a carrier.
* **Speech** (`go/internal/voice`) — whisper.cpp as an external transcriber,
  plus the envelope and speech-activity measurements the monitor uses.
* **Gallery** (`go/internal/gallery`) — images and text that ground stations
  decoded, fetched from SatNOGS observations and classified by content.
* **Relays** (`go/internal/relays`) — which satellites are FM voice
  repeaters, linear transponders or digipeaters.
* **Stations** (`go/internal/directory`) — every public receiver in the
  receiverbook directory, with its type and position.
* **Web UI** (`go/internal/web`) — a single page with a live spectrum, an
  activity feed and six tabs.

## Running it

```bash
cd go
go build ./...
go test ./...                       # all green; the live tests are skipped
go run ./cmd/sdrstream web --host 0.0.0.0 --port 8000 \
    --captures-dir ../captures \
    --monitor-server http://sdr.ladar.club:8073/ --monitor-kind openwebrx \
    --monitor-profile "D2 434-436MHz Satellites" --monitor-strategy spectrum \
    --monitor-threshold 16 --monitor-listen 8s --monitor-out ../captures/monitor
```

The page is then on `http://<phone-ip>:8000`. `--host 0.0.0.0` exposes an
unauthenticated server that hands out recordings and makes the server
connect outwards; only do that on a network you control.

Other useful commands:

```bash
go run ./cmd/sdrstream monitor --server <url> --kind openwebrx \
    --strategy sweep --start 145000000 --end 146000000 --step 25000
go run ./cmd/sdrstream gallery -days 7            # real payloads from SatNOGS
go run ./cmd/sdrstream relays                     # repeater satellites
go run ./cmd/sdrstream stations --type KiwiSDR    # every public receiver
go run ./cmd/sdrstream decode -mode voice cap.wav # transcribe a recording
```

## Speech recognition

whisper.cpp is installed in `~/.cache/sdrstream/whisper` (the arm64 release
plus `ggml-tiny.en.bin`). `SDRSTREAM_WHISPER`, `SDRSTREAM_WHISPER_MODEL` and
`SDRSTREAM_WHISPER_DIR` override the paths. Without a model the monitor still
reports that a transmission was voice, it just cannot say what was said. The
base.en model is more accurate but much slower on this CPU: a 25 second
clip takes about 45 s with base.en and about 15 s with tiny.en.

## Known problems

* **The SatNOGS API is unreachable from this sandbox.** The host drops the
  TLS handshake, and the public fetch proxies fail with 520/522 because the
  origin fails for them too. The gallery therefore stays empty here; the
  web server retries every 30 minutes and will fill it when the API is back.
  `SDRSTREAM_SATNOGS_PROXY` takes a read-only proxy with a `{url}`
  placeholder. The SatNOGS S3 store itself works, and the token the user
  supplied was rejected (`Invalid token`), so it was not used or stored.
* **The pass table in the web UI is a snapshot.** It is computed once at
  startup from the cached TLEs and is never refreshed.
* **The spectrum strategy is OpenWebRX only.** WebSDR and KiwiSDR send no
  spectrum in this client, so the panel stays empty for them and the sweep
  strategy is the way to watch those receivers.
* **RTTY and PSK31 can still invent text from a strong narrowband signal**
  that is not really that mode. Both now require a clean signal: RTTY needs
  six readable characters with half of them letters, PSK31 needs a phase
  correlation above 0.9 and a stream of valid varicode codes only.
* **The sweep never finishes a band in time on a slow link**: every step
  waits for the receiver to settle, so a 2 MHz band takes minutes.

## What the reviewers found, and what was done about it

Two reviewer subagents looked at the monitor, the decoders and the web page.
Fixed since:

* the sweep asked receivers for the modulation `auto`, which no receiver
  understands, so the default path heard nothing;
* the OpenWebRX client delivered audio and spectrum frames through one
  channel, so a monitor watching the spectrum stole half of every
  recording — audio now has its own channel;
* the strict varicode reader dropped any message whose idle was longer than
  16 bits, and lost the character before it;
* `Options.DwellTime` was never read: the peak persistence time was
  silently the sweep step time;
* the sweep's threshold had no effect and the reported level was the
  threshold itself;
* recording and decoding ran inside the 200 ms detection loop, so the
  monitor went blind while whisper ran — both strategies now use one
  worker;
* the web page overflowed sideways on a 360 px phone, rebuilt the audio
  player every five seconds, drew the canvas at the wrong aspect ratio and
  had no ARIA roles;
* the server sent no security headers, served any file in the captures
  directory, and put a network-supplied URL straight into an `href`.

Still open from the review, in rough priority order:

1. the spectrum downsampler keeps the strongest bin of each group, but the
   UI still cannot show a sub-bin detail of a transmission;
2. the passes table is not refreshed (see above);
3. the stations table is truncated to 400 rows without a way to see the
   rest, and a failing directory fetch is indistinguishable from an empty
   one;
4. `Activity.Ended` is never set, and `omitempty` does not apply to
   `time.Time`, so the JSON carries a zero timestamp;
5. the monitor's `Mode` field mixes a modulation and a verdict
   (`carrier`, `noise`, `speech`);
6. `decode -mode all` runs the whole decoder chain, including whisper, on
   every file;
7. the OpenWebRX `Tune` now re-sends `dspcontrol action=start` after a
   retune, which fixes servers that stop the demodulator but could
   interrupt a satellite chase on a server that does not expect it — it
   needs a mock-server test that pins the message sequence.

## Environment notes

* PRoot Debian 13 (trixie) on aarch64, inside Termux, root user. `pkg`
  refuses to run as root; `apt` works.
* `go test -race` cannot run here: the sandbox's ThreadSanitizer is not
  supported. The concurrency in the monitor was reviewed by hand instead.
* The root filesystem was at 97% full during this session (other projects
  in `/root/test` share it). Watch `df -h /` before large builds.
* Python has no working `pip`. Wheels were unpacked by hand into
  `/tmp/opencode/wheelroot` and used with `PYTHONPATH`.
* Celestrak and receiverbook are reachable; network.satnogs.org mostly is
  not.
