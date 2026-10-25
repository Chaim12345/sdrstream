# Convergence map - 2026-10-25

Recorded at `integration/nonphone-decoders` @ `8489f22` by the integration
lane. Question: how do the development lines merge? Answer, verified:
they already did. There is one forward line.

## Verified ancestry (merge-base checks, 2026-10-25)

`git merge-base --is-ancestor <ref> integration/nonphone-decoders`:

- `master` @ `71d9273` - ancestor. Integration is 342+ commits ahead.
- `sdrstream-team-integration` @ `0f3a91bd` (PR #4 head) - ancestor.
- `feat/continuous-fleet-record-all` @ `3af0750d` (PR #5 head) - ancestor.
- `feat/ledger-on-pr5` @ `4af3fee` (fleet tip: metrics, proof ledger,
  terms block, WebSDR retune fix, WSPR/AIS hardening) - ancestor;
  integration is 310 commits past it.
- Also contained: `feat/installable-app`, `fix/android-startup-crash`
  (PR #9 head), `feat/decoder-fixtures`, `feat/asr-eval(-harness)`,
  `acars-cleanroom`, `docs/handoff-sources`, `feat/kiwi-waterfall-spectrum`,
  `feat/live-engine-selection`, `feat/jt9-corpus-known-misses`,
  `feat/app-diagnostics`, `feat/diag-autosend`.

## Consequences

1. PRs #4 and #5 propose content already contained in
   `integration/nonphone-decoders`. Merging them to `master` would move
   master onto a 310-commits-stale point. Disposition is the owner's:
   close as superseded, or leave open until master fast-forwards.
2. `master` itself is an ancestor - a fast-forward of master to the
   integration tip is technically clean. Also the owner's call; the
   integration lane never pushes master.
3. There is no fleet-line vs integration-line merge to plan. The real
   remaining work is the unique-branch queue below.

## Unique branches (not contained), ordered merge plan

Each needs individual QA (clean-clone, -race where Go, revert-red on new
tests) before merging into `integration/nonphone-decoders`:

| Order | Branch | Ahead | Notes |
| --- | --- | --- | --- |
| 1 | `feat/dwd-rtty-verified` | 1 | single verified-decoder commit |
| 2 | `fix/satnogs-pagination-aprs-login` | 1 | PR #3; dedupe with `enhancements` (identical patch-id per old HANDOFF - close one) |
| 3 | `feat/hfdl-live-proof` | 3 | live-proof evidence; check claims vs strict decode policy |
| 4 | `feature/asr-quality` | 2 | pluggable external ASR backends + doc |
| 5 | `feat/navtex-ext-490-4209` | 5 | NAVTEX 490/4209 extensions |
| 6 | `feat/navtex-ext-490-4209-recovery` | 11 | recovery variant of #5 - merge after, or instead; decide by diff |
| 7 | `feature/qa-tests` | 11 | qa-matrix.sh; review before adopting as gate |
| 8 | `candidate/android-current-ui` | 1 | UI candidate; needs visual check |
| 9 | `enhancements` | 1 | duplicate of #2 - recommend close (owner's word) |
| hold | `wip/auto-capture-detector` | 1 | WIP capture package - hold until owner declares it ready |

## Landed status (recorded 2026-10-25, integration tip fa4fa4e)

Items 1-8 merged into `integration/nonphone-decoders` in plan order with
per-branch QA (merge SHAs: c63e0db, 1dd084a, f59725c, 8eac23c, e1e0a5d,
c037b7d, 18cbf3e, fa4fa4e). Item 9 `enhancements`: identical patch-id
to #2 - recommend close, owner's word. `wip/auto-capture-detector`:
still WIP, hold.

## What this unblocks

"Add features and show real decoded data" now has one landing zone:
`integration/nonphone-decoders`. New features branch off it, QA, merge
back. The archive + proof-ledger surface the user ask points at is
already on this line (via `4af3fee`).
