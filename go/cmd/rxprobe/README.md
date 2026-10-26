# rxprobe - bounded receiver probe harness

One-shot CLI that measures public SDR receiver endpoints under a strict
contact budget. Every attempt - success, refusal, dial failure, harness
crash - is one ledgered contact written as one JSONL row. No retries, no
retunes, no silent drops.

    go run ./cmd/rxprobe <candidates.json> <ledger.jsonl> [stage]

Candidates and ledgers are PRIVATE endpoint datasets: they live outside
the repository and are never committed or redistributed.

## Guards (all enforced in code, covered by tests)

- One bounded time window per attempt; a 429 or `too_busy` ends the
  attempt with no retry. `too_busy` is recorded plainly - it is not
  unambiguous hardware-full (API allowance can trigger it).
- No invented frequencies. KiwiSDR / Web-888 legs send `SET mod=iq` only
  at the passively observed `init.freq` on the probe's own channel; the
  UberSDR leg omits the frequency parameter entirely and the server
  applies its own default (14074000 Hz, ka9q_ubersdr websocket.go:540) -
  recorded as server-default provenance, never as "untuned".
- Operator access flags are hard gates checked pre-flight: KiwiSDR
  `/status` `ext_api=0` skips; on the Web-888 (RaspSDR fork) the field is
  expected to be ABSENT (not a denial), but a present `ext_api=0` still
  skips.
- Honest identity: `sdrstream-*` User-Agents and `ident_user=sdrstream-probe`.
  No browser fingerprint spoofing, no auth bypass, empty passwords only.
- Public dial targets only (private/loopback ranges refused), default TLS
  verification.

## Families

KiwiSDR, OpenWebRX, WebSDR, SpyServer (stages 1/2/c), UberSDR (`uber1`:
POST /connection + WS pcm-zstd v4), Web-888 (`a`: RaspSDR-fork SND
protocol).

## Protocol grounding

- Web-888 standalone parser (web888.go) verified against
  github.com/RaspSDR/server @ 6445a00742e35935a4b2e43453b5a1e3afc7699e:
  20-byte IQ header (rx/rx_sound.h:56-70), SND_FLAG_MODE_IQ=0x08 and
  SND_FLAG_LITTLE_ENDIAN=0x80 (rx/rx_sound.cpp:470-477), int16 BE payload
  by default (rx_sound.cpp:1112-1137).
- pcmv4/ is a VENDORED copy of the reviewed bounded PCM4 decoder from
  branch feat/ubersdr-iq-adapter @ 862cec8 (go/internal/ubersdr/pcmv4 +
  v4enc test encoder); see pcmv4/doc.go. The adapter branch remains the
  source of truth.

## Verdict honesty

`iq-samples-decoded` means real samples were decoded by a parser verified
offline against the owning source. `iq-frames-received` means frames
arrived but were not decoded - never upgraded to sample proof.
