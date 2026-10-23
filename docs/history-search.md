# History search

`GET /api/history/search` reads existing metadata, without a new database or
external audio/network lookup. It uses the normal UI authentication wrapper.
The request does not change recordings or provenance. It does call the existing
capture listing/cache path (`scanCaptures`), which can synchronize captured
clips into the receiver ledger (`syncReceiverLedger`). That inherited ledger
append/cache side effect also exists on the captures/status listing; this is
not a side-effect-free filesystem read.

Parameters:

- `q`: case-insensitive literal substring in name, decoded text, mode, kind,
  receiver, source or frequency in Hz; at most 1024 bytes.
- `source-class` (alias `source_class`): `synthetic`, `foreign`, `unknown`, `live-verified`,
  `live-unverified` or `live-redecode`.
- `mode`: case-insensitive exact decoder/mode.
- `redecode`: `true` or `false`, or omit. This is independent of provenance:
  unknown-source re-decodes remain `unknown`, never `live-redecode`.
- `limit`: 1-200, default 50. `offset`: nonnegative integer, default 0.

Response: `items`, matching `total`, unfiltered `available`, `limit`, `offset`,
nullable `next_offset` and a scope note. Items are newest first; equal times use
store-scoped IDs. Empty results use `[]`. Bad parameters return 400; non-GET
requests return 405. Unknown or repeated query parameters and conflicting
source-class aliases return 400 rather than silently dropping a filter. Responses are not cached.

Stores: on-disk capture sidecars and text-only orphan sidecars, in-memory
captures, live decode manifest, imported gallery, and the monitor's retained
text activity. A named disk/memory capture is listed once, preferring disk
metadata. Captures and their decoded artefacts remain separate records, not
unique observations. The WAV listing uses the existing 5000-file cap; monitor
history and gallery are limited to their existing retained metadata. Search
does not read image/audio payloads or discover deleted metadata.

Each page is a fresh metadata snapshot. New recordings or cleanup can move
results between offset pages. The UI does not poll or claim stable archival
pagination; submit Search again to refresh.

Source classes reuse the existing gallery provenance classification. Explicit
fixture content is synthetic regardless of a decoder verdict. Unknown-source
re-decodes retain their measured verdict but never assert live provenance.
Legacy SatNOGS source backfill applies only to the imported-gallery store.
Own-monitor activity is receiver-pipeline live data, not guessed from its URL;
rejected rows remain live-unverified and keep their recorded rejection text.
Activity source labels are additive: the existing activity inclusion, twin
merge and gate count rules remain unchanged. Fixture and foreign gallery rows
are still excluded from the live activity feed.

Tests: `go test ./internal/web -run 'TestHistory|TestActivitySerializes' -race`
from `go/`; `node --test scripts/tests/history-ui.test.cjs` from the repo root.
