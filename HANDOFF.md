# Handoff — sdrstream

Current as of **2026-10-22, post-deep-review**. Self-contained: everything a
fresh session needs is here.

## 1. The product and its stated end goal

**Vision (from `AGENTS.md` + `go/docs/SCAN-DESIGN.md`):** sdrstream finds
active transmissions on *real, public* SDR receivers (WebSDR / OpenWebRX /
KiwiSDR), then, continuously and fleet-wide:

1. scans **all stations for activity** and records **everything possible**;
2. transcribes **voice** transcription, decodes **Morse and data**
   (RTTY/POCSAG/NAVTEX/ACARS/HFDL/FT8/WSPR/…), decodes **images**
   (SSTV, WEFAX, APT);
3. surfaces it all in a live web UI (scan board, gallery, map) and feeds an
   existing scanner product — **Rdio Scanner** — through
   `POST /api/call-upload`, so the fleet plugs into a real operator UI
   instead of building a second frontend;
4. runs unattended for days within politeness limits (one connection per
   receiver, bounded concurrency load guard, retention) on this 8-core box.

**Standing user directives (do not regress):**

* Prefer the **hosted NVIDIA NIM ASR** (`NVIDIA_API_KEY` /
  `SDRSTREAM_NIM_API_KEY`; whisper-large-v3 first, then parakeet). Local
  whisper.cpp is an **offline fallback only**. Never switch the default back
  to local "to save calls"; serialization of local runs is a *safety cap*,
  not a preference.
* **Licensing is not a constraint** (personal use). Reuse/adapt/vendor public
  code (e.g. rdio-scanner) freely; attribution is courtesy, not a gate.
* Real sources only for PoC verification (no mock data); tests stay
  network-free and env-gated.
* Never put secrets in flags or docs; env only.

## 2. Where the code is right now

* Repo `/root/sdrstream`, git, branch
  **`feat/continuous-fleet-record-all`** — **working tree clean**, 2 commits
  ahead of master:
  * `2cdd7fb` Keep scanning continuously and archive every detection
  * `8232da2` Remember rotation cursors across restarts
* **PR #5 open** (`feat/continuous-fleet-record-all` → master) with full
  description + live evidence. Note: **PR #4 (team integration) is also
  still open**; master already contains its content, so #4 should be closed
  once #5 is reviewed (it merges into master as separate commits anyway).
* Every build slice passes: `gofmt` clean, `go build ./...`, `go vet ./...`,
  `go test ./cmd/sdrstream ./internal/monitor ./internal/web ./internal/rdio`.
* **`go test -race` WORKS on this sandbox** for `cmd/sdrstream`,
  `internal/monitor`, `internal/web`, `internal/rdio`, `internal/openwebrx`,
  `internal/kiwisdr` (all green today). The old "TSan unsupported" note
  applies only to the cgo `voice` package. **Use `-race` in every slice
  loop from now on.**

### Landed this session (all verified live on real receivers)

* **Continuous roster** (`go/cmd/sdrstream/scan_roster.go`) — manual +
  directory stations kept separately, deduped by host:port,
  `--scan-directory-refresh` (default 1 h) swaps only the dynamic half,
  failed/empty refresh keeps the previous list.
* **Rotation across visits** — Kiwi HF target cursor resumes after the last
  target actually processed; OpenWebRX rotates one profile per visit
  (`ProfileNames()`/`ActiveProfile()`), falls back + relearns on failure.
* **Persistence** (`go/cmd/sdrstream/scan_rotation_state.go`) —
  `scan-rotation.json` under `SDRSTREAM_STATE_DIR` (default
  `~/sdrstream-state`; `--scan-state` overrides, empty disables). Loaded at
  web start, written atomically (temp+rename, 0600) per visit; corrupt/
  missing/newer ignored. Two bugs were caught by new tests before shipping:
  a deferred-mutex deadlock in `noteOpenWebRXProfiles` and a stale state
  file resurrecting a deleted cursor after the last receiver's reset.
* **Record-all archive** (`go/cmd/sdrstream/capture.go`) —
  `--scan-record-all` writes WAV + `key=value` sidecar for every detection
  incl. unverified/noise/carrier; monitor archives *before* the
  noise/silence filter via `monitor.Options.OnCapture`; sidecars carry
  kind/target/gate/started/verified and `scanCaptures` reads them into the
  UI. `--captures-max-gb` / `--captures-retain` prune; both default
  unlimited.

### Live evidence (today)

* **Rotation + record-all (4 min, one Kiwi):** 3 visits, 12 distinct HF
  targets (VOLMET 5505 voice + DWD RTTY 4583/7646/10100.8 → OK0EU CW,
  NAVTEX 518, WEFAX 3855/7880 → WEFAX 13882.5, RWM 4996/9996/14996). Voice
  clip `verified=true` with a NIM transcript ("Wind 350 degrees, 10 knots.
  Cloud full at 2500 feet. Madrid-Barajas forecast…"); rejects archived
  with their gate reasons. Captures at `/tmp/opencode/live-captures`.
* **Persistence across restarts:** phase 1 (limit 2, 100 s) ended with
  cursor `"DWD WEFAX 7880"` on disk; phase 2 (fresh process, limit 1)
  started at **WEFAX 13882.5 (target 9)** — not VOLMET 5505 — and walked
  RWM + PSK31. State file at `/tmp/opencode/rotation-state.json` summarises
  what a reload restores.
* Earlier baseline live checks: repeated verified Shannon VOLMET transcripts
  on `kiwisdr.johnnorton.co.uk:8073` (5505 USB, 25 s listens), OpenWebRX
  spectrum survey at `sdr.ladar.club`.

## 3. Deep review snapshot (2026-10-22) — FIX STATUS

Five parallel review passes over the whole repo. Full finding list lives in
this session's conversation; every item has file:line + quote. **Fixed so
far** (commits `9856a52`, `41efb0d`, `7259d71`, `c1051e4`):

* **C2 fixed** — ReadWAV clamps a torn data chunk to the bytes that exist
  + truncation regression test.
* **C1 resolved as FALSE POSITIVE** — cross-checked Junzi Sun's pyModeS:
  our odd-newer branch is the DO-260B formula byte-for-byte (`ni = NL-1`,
  `m = floor(lonE*(NL-1) - lonO*NL + .5)`); the reviewer compared decodes
  at *different instants* (movement ≈ 0.02°). Armored with the synthetic
  encoder round-trip test (`cpr_armor_test.go`), both parities + stationary.
* **H1+M1 fixed** — EMFILE/ENFILE classify `localResource` (never refused,
  never blocklisted); blocklisting is gated on refused-or-timeout at all
  four sites. `TestClassifyScanFailureLocalResourceIsNotReceiverRefusal`.
* **M2 fixed** (profile-failure client close), **M5 fixed** (Enqueue/Close
  under q.mu, close before drain-outside-lock + hammer test), **M9 fixed**
  (CW temp WAV removed), **M8 fixed** (WriteWAV removes partial file +
  monitor logs record failures), **M10 fixed** (prune keeps accounting on
  failed remove; injectable remover + test), **M11 fixed** (boot logs and
  continues on directory-fetch failure; scanner starts on any roster
  source).
* **H4 fixed** — NIM: pooled TLS conn per endpoint, 2-slot concurrency
  cap, status-code cooldowns (quota/auth 10 min, Unavailable 1 min);
  `TestNIMPoolsTLSConnection`, `TestNIMCooldownAfterQuotaFailure`.
* **H5 fixed** — `voice.GuardTranscript` runs on the Kiwi HF voice path.
* **H6 fixed** — http.Server timeouts + MaxHeaderBytes; POST/PUT/PATCH
  bodies capped 1 MiB (`limitRequestBody`).
* **M12 fixed** (handleScanLog/handleScans snapshot-then-write), **M13
  fixed** (Secure follows r.TLS / X-Forwarded-Proto; both directions
  tested), **M14 fixed** (attempts map sweep+shed cap, tested).
* **H2 fixed (events half)** — openwebrx readLoop closes the events
  channel on exit (tested); monitor source freshness-gates Spectrum()
  (dead or >1 min stale → ok=false) and handles the ok flag.
* **H3 fixed (openwebrx+websdr)** — 10 s write deadline on every text
  write; kiwisdr already had one.

**Still open:**

* **H2/H3 remainder:** openwebrx auto-reconnect (`Reconnect` option still
  unused - consumers must reconnect instead), kiwi in-flight reconnect
  (fixed interval, masked error), websdr backoff reset + preflight
  short-read, and a websdr test suite (still zero tests).
* **M4 kiwi tune-drain still OPEN** — the socket-drain approach was
  reverted in `0441873`: it swallowed live audio and broke the three
  `TestRunHFSession*` reference tests (they record a fixed sample count
  from a fake receiver). The openwebrx (audio-buffer drain) and websdr
  (event-buffer drain) versions are safe and landed. Kiwi needs a design
  that discards pre-tune audio *without consuming the capture it is
  meant to prepare*.
* **M15 remainder:** ~345 MB/h retention (capture quota knob);
  WSPR Fano "off-by-one" re-verified against the Phil Karn reference
  (Guenael/rtlsdr-wsprd `wsprd/fano.c`) and is a **FALSE positive** -
  `lastnode=nbits-1`, `tail=nbits-31`, the metric/sort loops, the
  forward/backward walk and the `nodes[7]` step-8 octet extraction all
  match byte-for-byte, so no change was made.
* WSPR NaN/fac==0: guarded (`symbols[i]=128` on NaN/Inf), but the
  degenerate path is **not reachable** from `WSPRDecode` today (silence is
  filtered before symbol normalization), so the guard is defensive
  hardening, not a bug fix; the new test is a smoke test only.
* openwebrx readLoop writes Config/Profiles without mu (minor race).

**Critical**
* **C1** `internal/decode/modes.go:417` — CPRPosition **odd-newer** branch
  wrong zone count (`ni := cprNL(latOdd) - 1` while `m` uses `cprNL(latOdd)`)
  → ~1 km position error; empirically confirmed; odd branch **untested**.
* **C2** `internal/decode/decode.go:32-48` — **ReadWAV panics** on a
  truncated WAV (declared data-chunk size trusted) → recovered panic, decode
  wave lost, triggered by any disk-torn clip.

**High**
* **H1** `cmd/sdrstream/main.go:2439` — EMFILE/ENFILE ("too many open
  files") counts as *refused* → mass 30-min blocklist of healthy receivers
  under FD exhaustion + ramp backoff.
* **H2** `internal/openwebrx/openwebrx.go:117-125,478-501` — `Reconnect`
  declared but never implemented; readLoop exits **without closing
  `events`** → frozen spectrum keeps "scanning dead air".
* **H3** `internal/websdr/*.go:306,319,246` + `openwebrx.go:543-552` — **no
  write deadlines** on websocket writes → dead peer wedges the worker
  forever, leaks the fleet `active` slot (kiwisdr does this right).
* **H4** `internal/voice/nim.go` — hosted NIM is the one unbounded fleet
  resource: no concurrency cap, fresh gRPC conn per clip, no error
  classification (401/429 treated as transient) → dead key = 2 doomed RPCs
  per clip, fleet-wide.
* **H5** `cmd/sdrstream/main.go:3099` — Kiwi HF voice path publishes to
  gallery + Rdio **without** `voice.GuardTranscript` (monitor path has it)
  → whisper hallucinations can publish as "verified" text.
* **H6** `internal/web/web.go:1368` — `http.Server` zero timeouts and no
  `http.MaxBytesReader` anywhere → slowloris/body-bomb.

**Medium (abridged):** unconditional station blocklist on any error
(main.go:2845/2635/2651); leaked openwebrx client on failed profile select
(monitor/source.go:42-49); openwebrx readLoop writes Config/Profiles without
mu; Tune doesn't drain pre-tune audio in all three clients (clips start with
the previous frequency); rdio `Enqueue`-after-`Close` send-on-closed panic;
verify gate mixes byte/rune counts (rejects accented text) + substring
anchors; kiwi in-flight reconnect (fixed interval, masked error) + unlocked
`rate`/`info`; silent WAV-write failures; CW temp-WAV leak
(decode/hfrun.go:160); prune decrement-on-failure + orphaned sidecars
(capture.go:243-247); boot aborts on directory-fetch failure even with
manual stations; web writes responses under the global mutex; `Secure`
cookie on plain-HTTP bind (infinite login loop → pushes users to
`--no-auth`); unbounded login-attempts map; WSPR NaN/fac==0 + Fano
off-by-one + ~345 MB/h retention; AIS assembler never evicts orphans;
unclamped AIS lat/lon.

**Verified good:** traversal defense probed solid (`..%2F`, `%252e%252e` →
404, allowlist holds), crypto sessions, `textContent` discipline, argv-only
exec (no shell injection), non-blocking queues, source ctx lifetime correct
after last session's fix.

**Test gaps:** `internal/websdr` has **zero tests**; CPR odd branch,
truncated-WAV fuzz, FT8/WSPR audio-level SNR fixtures, WEFAX color, AIS
eviction, dtmf/blob/flex all missing.

## 4. Roadmap forward (priority order)

**Progress markers:** R0 done every slice (`go test -race` green on the six
race-capable packages). R1 batch 1+2 done (`9856a52`, `41efb0d`); NIM/web
parts of R3+R4 done (`7259d71`); openwebrx/websdr write-deadlines + events
close done (`c1051e4`).

* **R0 process:** run `go test -race` on the six race-capable packages in
  every slice loop; replace the stale `-race cannot run` note everywhere.
* **R1 fleet stability — DONE** (C1 armor, C2, H1, M1, M2, M5, M8, M9,
  M10, M11). Remaining R1-scope item: jukebox main.go:2865 temp-WAV.
* **R2 transport remainder:** openwebrx auto-reconnect (or feed
  `Reconnect` through), tune-drain M4 (all three clients), kiwi fixes
  (M7: in-flight reconnect, unlocked rate/info), websdr backoff reset +
  preflight short-read, and a websdr test suite (still zero tests).
* **R3 NIM governance — DONE** (pooled conn, worker pool, cooldowns,
  GuardTranscript on the Kiwi path). Remaining: rune-count/word-boundary
  verify fix (M6), `TranscribeASR` under the semaphore.
* **R4 web hardening — DONE** (H6, M12, M13, M14). Remaining smallies:
  `/audio/live` exact match, `app.js:1368` innerHTML → textContent, atomic
  `index.json`.
* **R5 decode coverage:** truncated-WAV fuzz, FT8/WSPR audio-level SNR
  fixtures, WEFAX color + mid-chart, AIS eviction, WSPR NaN/fac==0 +
  Fano off-by-one, block-scan budget reduction. (CPR odd test + C2 clamp
  test already landed in `9856a52`.)
* **R6 structure:** split main.go (3.4k lines) into `internal/fleetscan` +
  `internal/capture`; make `web.Server` the single owner of the scan board
  (kill the 500-cap mirror that strands rows); one station-identity
  normalizer (`stationHostPortKey`) for roster ≡ blocklist ≡ board; move
  per-host special cases into target config.
* **R7 mission capabilities:** WebSDR/Kiwi **band sweep** from advertised
  bands, persisted gallery index, retention/ops dashboard for multi-day
  unattended runs, real Rdio Docker end-to-end, per-station profile hints.

## 5. What the tool does

`sdrstream` records audio from public SDR receivers and works out what is on
the air. The Go build is the maintained one; the Python package is the
original port source.

* **Receivers** — WebSDR, OpenWebRX and KiwiSDR clients, live-verified.
* **Monitor** (`go/internal/monitor`) — watches one receiver for any
  transmission: data goes to the packet/paging decoders, voice is
  transcribed, images are reconstructed.
* **Fleet scanner** (`web --scan-open-directory`, `--scan-kiwi-directory`) —
  bounded parallel scan of receiverbook stations with a 49→100 concurrency
  ramp, load guard, per-host blocklist/backoff, and verified results into
  the gallery. OpenWebRX stations survey their own FFT (one rotating profile
  per visit); KiwiSDR walks the HF target list with a persisted cursor
  (VOLMET 5505 USB first, 25 s listen); WebSDR still tunes fixed VHF/UHF
  targets.
* **Decoders** (`go/internal/decode`) — AFSK/AX.25, Morse, DTMF, RTTY,
  PSK31, SSTV (VIS + Martin M1/M2, Robot36, Scottie S1), POCSAG, FLEX,
  EAS/SAME, NAVTEX, ACARS, HFDL, FT8/WSPR, APT, AIS/Mode-S, plus the
  transmission scanner.
* **Speech** (`go/internal/voice`) — hosted NVIDIA NIM first, local
  whisper.cpp large-v3 fallback (globally serialized), transcript guard
  against hallucination.
* **Verify** (`go/internal/verify`) — live quality gates (callsign/service
  anchors) before anything reaches the gallery or Rdio.
* **Rdio** (`go/internal/rdio`) — async non-blocking queue uploading
  verified captures with audio to `POST /api/call-upload`.
* **Gallery / web UI** — static frontend, map, live scan view, auth, sidecar
  capture metadata; verified live artifacts only.

## 6. Running it

```bash
cd go && go build ./... && go test ./...   # live tests skip unless env-gated
export NVIDIA_API_KEY=...                  # hosted ASR (preferred)

# Fleet web run:
go run ./cmd/sdrstream web --host 127.0.0.1 --port 8000 \
  --captures-dir ../captures \
  --scan-open-directory --scan-kiwi-directory \
  --scan-target-limit 4 --scan-station-session 3m \
  --scan-record-all --scan-directory-refresh 1h

# Single receiver:
go run ./cmd/sdrstream monitor \
  --server http://sdr.ladar.club:8073/ --kind openwebrx \
  --strategy spectrum --listen 8s --voice
```

New flags this session: `--scan-directory-refresh`, `--scan-record-all`,
`--captures-max-gb`, `--captures-retain`, `--scan-state`. Run
`go run ./cmd/sdrstream web -h` for the full list.

## 7. Known problems / next work

* The review items in §3 (R1–R5) are the concrete work queue; verifies be
  re-run after each.
* WebSDR/Kiwi still use fixed targets (see `go/docs/SCAN-DESIGN.md` §7).
* SSTV decode covers Martin M1/M2, Robot36, Scottie S1; other VIS modes fall
  back to header-only detection.
* Hosted ASR can hallucinate on weak audio; the guard is the defence — H5
  closes the one path that currently skips it.
* SatNOGS is often unreachable from this sandbox; gallery retries on a timer.
* `go test -race` cannot run for `internal/voice` (cgo); fine for the rest —
  see §2.

## 8. Environment

* x86_64 Linux, 8 cores, ~24 GB RAM, root. Date 2026-10-22.
* whisper.cpp v1.8.3 + `ggml-large-v3.bin` in `~/.cache/sdrstream/whisper/`;
  faster-whisper venv at `~/.cache/sdrstream/fw` (not wired in).
* `NVIDIA_API_KEY` exported; NIM gRPC (`grpc.nvcf.nvidia.com`) reachable.
* Python has no working `pip`; wheels unpacked by hand.
* `SDRSTREAM_STATE_DIR` (default `~/sdrstream-state`) holds
  `station-blocklist.gob` and `scan-rotation.json`.
* Live-run artifacts from this session: `/tmp/opencode/live-captures`,
  `/tmp/opencode/live-scan.log`, `/tmp/opencode/rotation-state.json`,
  `/tmp/opencode/persist{1,2}`.

## 9. Next session restart instructions

1. `cd /root/sdrstream && git status` — expect clean tree on
   `feat/continuous-fleet-record-all`; read this file top to bottom.
2. Check `gh pr list` — merge/reconcile PRs #4 (stale) and #5 (current).
3. Start the work queue at §4 **R1** in order (C1, C2, H1 first); each fix
   gets a test, `gofmt`, `go vet ./...`, `go test -race` on the touched
   package, then a tight commit on the branch.
4. Live-verify against `kiwisdr.johnnorton.co.uk:8073` (VOLMET 5505 USB,
   25 s listen reproduces reliably); never without real receivers.
