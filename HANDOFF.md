# Handoff - sdrstream

Current as of **2026-10-22**, docs pass on branch `feat/ledger-on-pr5`
(`4af3fee`). Self-contained: everything a fresh session needs is here.
This file is maintained documentation, not a record of user instructions;
decisions attributed to the owner cite where they were recorded.

## 1. Branch and PR map (the real state)

Everything current hangs off three lines:

| Line | Branch | Head | Open PR | Contains |
| --- | --- | --- | --- | --- |
| Base | `master` | `71d9273` | - | Pre-integration master |
| Integration | `sdrstream-team-integration` | `0f3a91b` | PR #4 (open) | Team merge: security, web UI, SSTV/HF decoders, voice guard, verify gate |
| Fleet | `feat/continuous-fleet-record-all` | `3af0750` | PR #5 (open) | All of PR #4 + continuous scanning, record-all archive, rotation persistence, review fixes R1/R3/R4, kiwi pre-capture drain (M4) |
| Current work | `feat/ledger-on-pr5` | `4af3fee` | no PR yet | All of PR #5 + metrics (`/api/metrics.json`), proof ledger (`/api/ledger.json`/`.csv`), per-source terms block, WebSDR retune race fix, WSPR/AIS decode hardening |

2026-10-25 addition (recorded at merge `34b271e`): a fourth line exists -
`integration/nonphone-decoders` @ `1d21cec`, 342+ commits ahead of
`master`, carrying the Android app (0.1.3 debug APK, CI emulator-install
verified), fleet campaign, direct-SMTPS diagnostics, and the 2026-10-25
S23 device-log fixes (crash-mislabel guard, logcat spam filter,
dxcluster idle-deadline, FM skirt-peak suppression). Correction, recorded
2026-10-25 minutes later: the containment claim above is SUPERSEDED and
was wrong. Verified by merge-base: `4af3fee` (fleet tip), `0f3a91bd`
(PR #4 head), `3af0750` (PR #5 head) and `master` are all ancestors of
`integration/nonphone-decoders` - the lines already converged; see
`docs/convergence-2026-10-25.md`.

Facts a fresh session should not re-derive:

- PR #4's head is an ancestor of PR #5's head, and PR #5's head is an
  ancestor of `feat/ledger-on-pr5`. PR #4 content is **not** in `master`.
  (An earlier revision of this file claimed master already contained PR #4;
  that was wrong.)
- PR #5 is 31 commits ahead of `master`.
- Work lands via branches and PRs; `master` is left for the owner to merge.
  No force pushes.

### Branches not merged into `feat/ledger-on-pr5`

| Branch | Unique commits | What it is |
| --- | --- | --- |
| `fix/satnogs-pagination-aprs-login` | 1 (`638ba5d`) | SatNOGS cursor pagination + APRS-IS login line. Open as draft PR #3. |
| `enhancements` | 1 (`2f01a28`) | Same patch as above (identical patch-id) - duplicate, close one. |
| `feature/asr-quality` | 2 (`7460b9b`, `e77e375`) | `asreval -ext` pluggable external ASR backends + hosted-vs-local WER doc (`docs/asr-quality.md`). |
| `feature/qa-tests` | 1 (`a3627bb`) | `scripts/qa-matrix.sh`: ci.sh plus `-race -count=3` per branch. |
| `wip/auto-capture-detector` | 1 (`844f10e`) | New `go/internal/capture` package: adaptive PCM activity detector + aligned HF windows (377 lines, WIP). |

Every other `feature/*`, `fix/*`, `recover/*`, `licence-agpl`,
`acars-cleanroom` and `openhands/*` branch is already merged into
`feat/ledger-on-pr5`.

## 1a. Open items (integration lane)

- 2026-10-25: android-ui-qa harness robustness - fail the run when the
  emulator is lost mid-run (run 40211520353 concluded SUCCESS with
  "device not found" x12 and 0-byte screenshots), scroll to the Receiver
  panel and screenshot the decode-target chip row (verified via DOM dump
  only). Owner: integration lane, after OpenHands findings land.

## 2. The product and its stated end goal

Vision (from `AGENTS.md` + `go/docs/SCAN-DESIGN.md`): sdrstream finds active
transmissions on *real, public* SDR receivers (WebSDR / OpenWebRX / KiwiSDR),
then, continuously and fleet-wide:

1. scans **all stations for activity** and records **everything possible**;
2. transcribes **voice**, decodes **Morse and data**
   (RTTY/POCSAG/NAVTEX/ACARS/HFDL/FT8/WSPR/...), decodes **images**
   (SSTV, WEFAX, APT);
3. surfaces it all in a live web UI (scan board, gallery, map, live feeds)
   and feeds an existing scanner product - **Rdio Scanner** - through
   `POST /api/call-upload`;
4. runs unattended for days within politeness limits (one connection per
   receiver, bounded concurrency, load guard, retention).

Recorded project decisions (provenance in parentheses):

- Hosted NVIDIA NIM ASR first (whisper-large-v3 / parakeet), local
  whisper.cpp as offline fallback only (project memory, owner direction
  Oct 21-22).
- The repo is AGPL-3.0-or-later with `NOTICE` + `NOTICE-third-party.md`;
  third-party decoder code (WSJT-X, multimon-ng, dumphfdl, ft8_lib, ...)
  is vendored with its upstream licence retained per file (licence/CI
  branch `644d2b7`). Three reuse questions are still open - see
  "Pending owner decisions" below.
- Real sources only for verification (no mock data); tests stay
  network-free and env-gated.
- Secrets in env only, never in flags or docs.

## 3. What the code does today

`sdrstream` records audio from public SDR receivers and works out what is on
the air. The Go build (`go/`) is the maintained one; the Python package is
the original port source.

- **Receivers** - WebSDR, OpenWebRX and KiwiSDR clients, live-verified.
- **Monitor** (`go/internal/monitor`) - watches one receiver for any
  transmission: data to the packet/paging decoders, voice transcribed,
  images reconstructed.
- **Fleet scanner** (`web --scan-open-directory`, `--scan-kiwi-directory`) -
  bounded parallel scan of receiverbook stations with a concurrency ramp,
  load guard, per-host blocklist/backoff, verified results into the
  gallery. OpenWebRX stations survey their own FFT (one rotating profile
  per visit); KiwiSDR walks the HF target list with a persisted cursor;
  WebSDR still tunes fixed VHF/UHF targets.
- **Continuous roster + rotation** (`go/cmd/sdrstream/scan_roster.go`,
  `scan_rotation_state.go`) - manual + directory stations deduped by
  host:port; `--scan-directory-refresh` (default 1 h) swaps only the
  dynamic half; rotation cursors persist in `scan-rotation.json` under
  `SDRSTREAM_STATE_DIR` (atomic temp+rename, 0600).
- **Record-all archive** (`go/cmd/sdrstream/capture.go`) -
  `--scan-record-all` writes WAV + `key=value` sidecar for every detection
  including unverified/noise/carrier; the monitor archives before the
  noise/silence filter. `--captures-max-gb` / `--captures-retain` prune;
  both default unlimited.
- **Decoders** (`go/internal/decode`, plus `hfdl`, `vocoder`) - AFSK/AX.25,
  Morse, DTMF, RTTY, PSK31, SSTV (VIS + Martin M1/M2, Robot36, Scottie S1),
  POCSAG, FLEX, EAS/SAME, NAVTEX, ACARS, HFDL, FT8/WSPR, APT, AIS/Mode-S,
  WebSDR vocoder (companding table + LPC).
- **Speech** (`go/internal/voice`, `rivaspeech`) - hosted NVIDIA NIM first,
  local whisper.cpp large-v3 fallback (globally serialized), transcript
  guard against hallucination on all publish paths.
- **Verify** (`go/internal/verify`) - live quality gates (callsign/service
  anchors) before anything reaches the gallery or Rdio.
- **Live internet feeds** (`go/internal/feeds`) - ADS-B, AIS, APRS-IS,
  PSK Reporter, RBN, DX cluster, POTA/SOTA, WSPR (wspr.live + wsprnet
  cross-check), NOAA SWPC, SondeHub, Digitraffic, airframes.io. Full
  per-source terms and polling table: **`SOURCES.md`**.
- **Rdio** (`go/internal/rdio`) - async non-blocking queue uploading
  verified captures with audio to `POST /api/call-upload`.
- **Gallery / web UI** - static frontend, map, live scan view, live feeds
  panel with per-item proof toggle, auth, sidecar capture metadata.

## 4. Health, metrics, proof ledger, terms

- `GET /healthz` - unauthenticated liveness probe, returns `{"ok":true}`.
- `GET /api/metrics.json` (login) - uptime, capture count, scan outcomes
  and per feed source: state, items now, new items in the last hour and
  since boot. Counts are first sightings seen by this process and reset on
  restart.
- `GET /api/ledger.json` and `/api/ledger.csv` (login, `?source=`,
  `?limit=`) - append-only record of every first sighting: provider time,
  our fetch time, who decoded it (`provider` for every current feed), and
  a link to the provider. It is sdrstream's normalised view, not the raw
  upstream bytes. File: `sdrstream-ledger.jsonl` in the state dir, rotated
  at 64 MiB per file (`SDRSTREAM_LEDGER_MAX_MB`, 1-1024).
- Every feed card in the UI shows its terms block (licence, notes, max
  age) from `go/internal/feeds/terms.go`; sources with no recorded licence
  say so instead of inventing one. Each item has a "proof" toggle
  (provider time, fetch time, decoded-by, source).

## 5. Verification status (honest labels)

**Verified live, on real receivers/services (2026-10-22 sessions, PR #5
line):**

- Continuous roster + rotation + record-all on a Kiwi receiver: 3 visits,
  12 distinct HF targets, VOLMET 5505 voice clip `verified=true` with a
  NIM transcript; rejects archived with gate reasons.
- Rotation persistence across restarts (cursor resumed at the next
  unprocessed target, not the list head).
- Shannon VOLMET transcripts on `kiwisdr.johnnorton.co.uk:8073`;
  OpenWebRX spectrum survey at `sdr.ladar.club`.
- FT8 decoder at ~77% of the WSJT-X reference on live Twente WebSDR
  captures; WSPR decoder cross-checked against reference `wsprd` on live
  windows.
- Feeds: adsb.lol, APRS-IS, PSK Reporter, RBN, SondeHub, POTA/SOTA,
  wspr.live, NOAA SWPC and Digitraffic have all delivered real items to
  the UI in live runs.

**Verified against reference implementations only (not live):**
PSK31Scan timing recovery; parts of the WSPR Fano path.

**Synthetic/tests only:** SSTV (live classification enabled 7d7ec06;
  full-frame proof is a synthesized Martin M1 round trip, 256/256 lines
  locked - no live RF capture yet), EAS/SAME fixtures,
most unit-level decoder fixtures.

**Known gaps:** wide-scan yield of verified decodes is still low; most
KiwiSDRs time out or reject from the current cloud network; WEFAX live
decode not yet readable (signal too weak in tested windows).

Build/test hygiene as of `4af3fee`: `gofmt` clean, `go build ./...`,
`go vet ./...` and `go test -race` were green on the race-capable packages
(`cmd/sdrstream`, `internal/monitor`, `internal/web`, `internal/rdio`,
`internal/openwebrx`, `internal/kiwisdr`) in the working session.
`go test -race` cannot run for `internal/voice` (cgo). This docs pass did
not re-run the suite (no Go toolchain in the docs sandbox); re-run before
the next slice.

## 6. Deep review snapshot (2026-10-22) - fix status

Five parallel review passes over the whole repo. Landed fixes are on the
PR #5 line and `feat/ledger-on-pr5`; the full finding list with file:line
references is in the PR #5 description.

**Fixed and merged:** C2 (torn WAV clamp), C1 (CPR odd-newer confirmed a
false positive, armored with a round-trip test), H1+M1 (EMFILE/ENFILE no
longer mass-blocklists receivers), H4 (NIM pooled conn, 2-slot cap,
cooldowns), H5 (GuardTranscript on the Kiwi HF voice path), H6 (HTTP
timeouts + body caps), H2 events half (openwebrx readLoop closes events;
monitor freshness-gates Spectrum), H3 write deadlines on openwebrx/websdr,
M2/M5/M8/M9/M10/M11/M12/M13/M14, M4 kiwi pre-capture drain (inside
`Record`, 250 ms post-tune discard), WSPR NaN guard (defensive; degenerate
path not reachable from `WSPRDecode`), AIS orphan eviction + lat/lon
clamp, WebSDR retune-during-reconnect race.

**Still open** (re-audited against the tree 2026-10-23; several former
entries were stale and are listed under "Recently closed" below):

- R2 transport remainder: closed. openwebrx auto-reconnect shipped in
  a9e606d, the websdr backoff reset in 3cb752d. The "websdr preflight
  short-read" phrase has no anchor in the tree and the original review
  note is unrecoverable (both sides searched) - treated as unverifiable,
  no fix invented.
- openwebrx readLoop Config/Profiles data race: fixed in a9e606d (the
  handleText mutations now run under the client mutex).

Section 6 is now fully closed. The R5 decode-coverage list was audited
(2026-10-23) and every anchored item already exists in the tree -
truncated-WAV fuzz (`fuzz_wav_test.go`: FuzzReadWAV, FuzzDecodeTornWAV),
FT8/WSPR audio-level SNR fixtures (`ft8_snr_test.go`, `wspr_snr_test.go`),
WEFAX mid-chart (`TestWEFAXMidChartFalseWedgeFixed`,
`TestWEFAXMidChartCorrelationSync`), AIS eviction tests
(`aisnmea_eviction_test.go`, `TestAISAssemblerEvictsOrphanedFragments`).
Two fragments have no anchor: "WEFAX color" (the decoder is monochrome,
as HF WEFAX is) and "block-scan budget reduction" (no block-scan site;
nearest is WSPR's already-bounded Fano cycle budget). Treated as
unverifiable, no work invented.

**Recently closed (previously listed as open):**

- websdr test suite: `internal/websdr/websdr_test.go` exists.
- kiwi in-flight reconnect: `Client.Record` now honours deadlines while
  waiting and caps reconnects against a flapping server.
- M6 verify gate: text checks count runes, not bytes (accented text no
  longer rejected).
- R4 smallies: `/audio/live` exact match (regression-tested), `app.js`
  placeholders use DOM nodes/textContent (zero non-vendor innerHTML
  uses remain), gallery `index.json` writes are atomic
  (`common.WriteFileAtomic`).
- Retention: capture quota is wired - `pruneCapturesLoop` runs from
  `cmd/sdrstream/main.go` (`--captures-max-gb`) and the web server prunes
  on its own tick.
- Jukebox temp-WAV cleanup: unique temp + rename in `main.go`.
- NIM semaphore: `TranscribeNIM` is bounded by `nimSlots` (2 concurrent)
  with quota/auth cooldown, pinned by `internal/voice/nim_slot_test.go`;
  every production path (monitor, web, cmd) goes through
  `TranscribeBest`, so all hosted calls are semaphored. Only the
  `asreval` dev tool calls local `TranscribeASR` directly, which is
  bounded by `transcribeSlot`.

## 7. Pending owner decisions

These need the owner's call; nothing here is blocked on code.

1. **WebSDR companding table (PA3FWM).** `sdrstream/_websdr_table.py` and
   the generated `go/internal/vocoder/table.go` reproduce a 256-entry
   table from the WebSDR HTML5 client (copyright 2013-2025 PA3FWM, all
   rights reserved per the upstream header and server docs). No permission
   has been obtained; `NOTICE` marks the table as not redistributable
   under AGPL until resolved. Options: ask PA3FWM for written permission,
   or replace the table with an independently derived one. See `NOTICE`
   section "OPEN ISSUE".
2. **Two test WAV licences.** `tests/data/real_aprs_wa8lmf_30s.wav`
   (WA8LMF "TNC Test CD", freely distributed but no explicit licence) and
   `tests/data/real_pocsag_1200_taunus.wav` (signals.taunus.de, no licence
   statement). Options per `tests/data/NOTICE`: confirm redistribution
   terms with the authors, or replace with self-recorded clips.
3. **Copyright line.** `NOTICE` currently reads "Copyright (C) 2026
   Chaim12345 and sdrstream contributors". Confirm whether the GitHub
   username is the desired attribution or a real name should appear.
4. **airframes.io left as is.** `go/internal/feeds/airframes.go` (REST
   poll, default 60 s, plus the socket.io firehose) is used only by the
   `avmarine` command; it is deliberately not wired into the web feeds
   manager. Left as is unless the owner wants it in the UI feed set.

## 7a. Lane backlog (2026-10-24)

**App diagnostics (QA spec a-e, feat/app-diagnostics):** engine.log now
rotates at ~2MB with one rollover (engine.log.1) via a bounded drain
thread instead of unbounded Redirect.appendTo; javalog.txt captures
logcat for the app's own PID at startup and on Share (redacted, 512KB
cap); diag.txt carries a 64KB engine.log tail, the javalog tail, an
ApplicationExitInfo section (Android 11+ silent-kill/OOM/native-crash
history) and the engine log ring fetched over the authenticated loopback
UI when reachable; the error screen has a one-tap "Send diagnostics"
button; engine stderr is redacted at the Go source (web.RedactingWriter)
and javalog on capture (DiagRedact port) so shared logs can never carry
credentials, cookies, URL queries or keyed coordinates. JVM unit tests:
DiagRedactTest, BoundedTailTest, RotatingLogWriterTest (7 tests).


Code-owned follow-ups for a future lane; none block the current tip.

1. ~~Flake: openwebrx TestReconnectResumesStream~~ FIXED on
   fix/openwebrx-reconnect-flake. Root cause: the drop-server harness
   dropped connection 1 on the FIRST dspcontrol, which is the handshake's
   own DSP start (openwebrx.go:315) - earlier than the test's intent
   ('drops after the profile selection's demod restart'). The RST then
   raced the test's SelectProfile write: 'write: broken pipe' ~4/300.
   Fix: drop threshold is a startDropServer parameter (2 = after the
   profile-restart dspcontrol for the resume test, 1 = immediate for the
   exhaustion test). 1000/1000 stress green, race green.
2. ~~jt9 real-binary leg UNVERIFIED~~ RUN 2026-10-24 with three CLI jt9
   builds (2.2.2, 2.5.4, 2.7.0, all at `jt9 -8 -d 3`). Finding, not a
   clean pass: 3 of the 175 WSJT-X GUI decode-list lines are not
   reproduced by the CLI (test_01 "CQ RX3ASQ KO95" -24 dB, test_04
   "OK2BJ JG1SRO -15" -24 dB, test_05 "R8JA CT3IQ RR73" -10 dB). Cause
   unconfirmed: GUI a-priori/KVASD-class decodes are a hypothesis, not
   proven - kvasd is not shipped in any deb and its binary download
   refused 403, so no KVASD-assisted run was possible. Per QA spec the
   misses are pinned as a per-line allowlist keyed by (fixture, message),
   each entry carrying the exact reference line + SNR, the versions tried
   with the exact command/depth, per-version observed misses, the cause
   label and the date (manifest `ref_known_misses` + `jt9_leg` block,
   feat/jt9-corpus-known-misses). Every other line stays strict, stale
   exemptions fail, and an allowlisted line that starts reproducing is
   logged. Version-sensitivity at the margins: 2.7.0 reproduces RX3ASQ
   but misses 6 other, mostly stronger-signal lines.
3. ~~test_01..06 upstream-revision claim unproven~~ RESOLVED 2026-10-25:
   all 12 pinned files (6 wav + 6 txt) are BYTE-IDENTICAL (git blob sha1)
   to current upstream master test/wav/20m_busy/ @ 9fec6ca. The "earlier
   revision" claim came from comparing against the wrong upstream
   filenames (websdr_test*.wav); the cited commit 8428a81 does not exist
   upstream. PROVENANCE.md and the manifest provenance strings corrected.

## 8. Running it

```bash
cd go && go build ./... && go test ./...   # live tests skip unless env-gated
export NVIDIA_API_KEY=...                  # hosted ASR (preferred)

# Fleet web run with feeds:
go run ./cmd/sdrstream web --host 127.0.0.1 --port 8000 \
  --captures-dir ../captures \
  --scan-open-directory --scan-kiwi-directory \
  --scan-target-limit 4 --scan-station-session 3m \
  --scan-record-all --scan-directory-refresh 1h \
  --feeds-lat 31.8 --feeds-lon 35.2
# Optional AIS ships layer:
export SDRSTREAM_AIS_KEY=...   # free key from aisstream.io (OAuth signup)

# Single receiver:
go run ./cmd/sdrstream monitor \
  --server http://sdr.ladar.club:8073/ --kind openwebrx \
  --strategy spectrum --listen 8s --voice
```

Flags added on this line: `--scan-directory-refresh`, `--scan-record-all`,
`--captures-max-gb`, `--captures-retain`, `--scan-state`, `--feeds-lat`,
`--feeds-lon`, `--ais-key`. Env: `SDRSTREAM_STATE_DIR`,
`SDRSTREAM_LEDGER_MAX_MB`, `SDRSTREAM_AIS_KEY`, `NVIDIA_API_KEY` /
`SDRSTREAM_NIM_API_KEY`. Run `go run ./cmd/sdrstream web -h` for the full
list.

## 9. Next session restart instructions

1. Clone fresh: `git clone https://github.com/Chaim12345/sdrstream.git`,
   `git checkout integration/nonphone-decoders` (the active integration
   branch, also PR #4's head lineage). Read this file top to bottom.
   The `/root/sdrstream` and `/tmp/opencode` paths quoted in older
   revisions belonged to a previous sandbox and are gone.
2. Branch topology as of 2026-10-24: PR #7 and PR #8 are CLOSED as
   superseded-by-integration (both fully contained here, verified by
   merge-base). The receiver health pool (`--scan-pool health`, the
   default) and beacon quiet window are live on this branch. Section 6
   is fully closed. Lane branches merge here only after independent QA
   signoff; master is never touched by the lanes.
3. Merged 2026-10-24 (tips: 0be18ce): feat/ui-history-search (history
   search API + UI + activity source-class labels), feat/asr-eval-harness
   (hash-pinned WER corpus, skip-gated), feat/decoder-fixtures (SSTV +
   WEFAX fixture corpus; WEFAX fixtures are same-project synthetic round
   trips - regression pins, not independent proof), plus the SSTV
   sync-cadence hardening (45aeb98, grid vote restricted to near-header
   anchors + 70% occupancy over the densest 30-line window).
4. Work queue: ask the integrator lane for the current roadmap; each fix
   gets a test, `gofmt`, `go vet ./...`, `go test -race` on the touched
   package, then a tight commit on a feat/* branch.
6. feat/probe-catalog (in review, NOT yet merged): station probe catalog.
   Package go/internal/probe (Catalog with RWMutex + lock-free Snapshot,
   per-kind HTTP probers, Sweep scheduler, directory-source adapters) plus
   GET /api/station-catalog.json (only when a catalog is installed) and the
   OPT-IN --probe-directory flag (off by default; enabling it makes the
   server sweep public receiver listings into a persistent catalog file).
   QA: -race -count=20 clean on internal/probe, handler-mid-sweep -race
   test in internal/web.
5. Live-verify against real receivers only when the current task's
   owner approves the poll; never claim verification without real
   receivers or real feeds.

## 2026-10-26 addition - remote-IQ line and ground-station assembly

Recorded at `feat/remote-iq-input` tip `1362f84`. Branch line: off
`integration/nonphone-decoders` @ `299cdb0`.

| Branch | Tip (prefix) | State |
| --- | --- | --- |
| `feat/remote-iq-input` | `1362f84` | rtl_tcp groundwork, SpyServer client (protocol pinned to SDR++ master `f10fb98`), iqdemod AM/NBFM/USB/LSB, monitor spySource, web kind `spyserver` with SSRF-guarded dial, receiver dropdown + public SpyServer directory (`/api/spyservers.json`) + datalist autocomplete, stream Stats, ControlTruth UI text, `SDRSTREAM_PREVIEW_NO_START` demo gate, deterministic AFSK in-flight cancellation test |
| `feat/gs-ui-pass-timeline` | `58d3d4d` (+ unwired `c08f6bc4` coverage.js) | Pass timeline, band plan with region caveats, Activity category chips with decoder/target/frequency-guess basis + "Hide frequency guesses" |
| `feat/pass-plan-rebased` | `a980eb7` | passplan state + approved web seam `/api/passplan.json` (enabled:false until cmd wires a provider) |
| `feat/gs-decoder-deframers` | `a59f20c` (freeze) | G3RUH/ASM/PN/RS-CCSDS/AX100/FM-MPX; Tal verified via libfec oracle, rest synthetic |
| `feat/multivfo-shared-sdr` | `31266df` | Shared-source join/takeover state, unwired |

Live validation (labeled): SpyServer handshake + IQ streaming + carrier-level
reception proven on one public receiver; audio decode, frequency-axis
accuracy on CanControl=0 servers, and zero-stall streaming NOT proven
(26% of real-time observed, cause unknown; instrumentation landed, no
re-measurement without owner go).

Integration facts:
- `/api/stations.json` serves only type/name/url/lat/lon (no RF metadata at
  the source). `/api/spyservers.json` serves host/port/device_type/
  min_frequency/max_frequency (int64 Hz)/clients/max_clients/full_control/
  lat/lon/online. Receiver coverage filtering uses the SpyServer range
  fields only; stations are "coverage unknown" - never inferred from protocol.
- passplan plans, satsched executes; no planner->execution wiring yet.
- Trial assembly `feat/remote-iq-input` + UI `58d3d4d` builds and tests
  green and ran the authenticated preview (Activity chips pixel-verified).
- Preview evidence (IQ snippet, probe notes) lives in the owner's private
  Drive, hash-verified; not in this repo.

## 2026-10-26 addition (evening) - non-phone receiver family runtime + catalog

Recorded at `feat/remote-iq-input` tip `2b07f03`. Preview/backup branch
`integration/preview-uia-kiwiiq` @ `52b8e6a` (pre-replay-evidence merge;
replay commits land on the next preview merge).

| Branch | Tip (prefix) | State |
| --- | --- | --- |
| `feat/remote-iq-input` | `2b07f03` | Everything below |
| `integration/preview-uia-kiwiiq` | `52b8e6a` | Live preview build source |

New runtime (offline + cross-compile proven; NOT live-verified - the 308
receiver-contact ceiling is frozen, so no new client has touched a real
receiver):

- UberSDR: worker adapter `862cec8` merged (`625acf4`); monitor source +
  `monitorKinds` + CLI (`4daad1c`); UI-start https-scheme downgrade bug
  found and fixed with regression tests (`6fce9eb`); embedded legal notice
  sync (`32b4a77`); catalog chips + monitor dropdown option with guard
  test (`7ef317c`).
- Web-888: `iqsrc.DialWeb888` (`5c50df7`) - reuses the kiwi-iq transport +
  SND parser (dual-grounded KiwiSDR mainline + RaspSDR fork @`6445a00`);
  owns the fork differences: passive default frequency (server-announced
  load_cfg init.freq minus freq_offset, kHz; never invented, honest error
  when unannounced, deadline raced against the blocking read) and float
  `sample_rate` parsing (rx_sound.cpp:174). Monitor source + kind + CLI +
  dropdown wired in the same commit.
- Catalog import no longer fabricates consecutive-failure history:
  `ImportLedger` upserts verbatim (1 unreachable = one recorded
  observation, idempotent re-import, no transition events); live probes
  still increment through `Update` (`43f2180`).
- Offline replay evidence: upstream's hash-pinned recorded PCM4 stream
  replays pcmv4 -> complex IQ -> iqdemod (structural assertions only;
  content unknown) - `15665b5`, unaffected. The NAVTEX replay fixture
  (`15665b5`/`587863b`/`2b07f03`) was REMOVED from the public branch:
  the Oostende 490 kHz capture was made from the public Twente WebSDR,
  not the user's own receiver as its provenance file and manifest entry
  mislabeled it, and the code push grant does not cover publishing
  capture files. The replay test now skips unless the private 100s trim
  is supplied via `NAVTEX_REPLAY_WAV` (or dropped into testdata). The
  blob remains reachable in branch history at `15665b5`; a history
  rewrite was NOT done (needs review).

Catalog scope honesty: the station catalog carries ONLY the 70 newly
tested results (Batch C 40 + UberSDR 15 + Web-888 15), not the 308
historical contacts. Live preview: fail-closed auth re-verified after
every restart (401 unauth local + tunnel), served via
`deploy/preview.sh`.

Open gaps: directory sync (gated on reuse/source-policy grounding, not
started); live end-to-end decode through the new clients (needs an
owner-approved contact batch); UberSDR monitor/CLI now wired but Android
APK does not include any of this (APK predates `64db19a`).
