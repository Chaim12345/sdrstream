# Capture metadata sidecars

Every archived capture WAV may carry a `<name>.txt` sidecar next to it with
`key=value` lines describing provenance. The web UI renders these fields on
the Captures page and in the proof ledger; wrong or contradictory values
mislead, so sidecars are validated:

    go run ./cmd/sdrstream capturemeta -check <sidecar.txt|dir> [...]

Exit status is 1 when any file has an error. Warnings print but do not fail.

## Keys

| Key | Type | Meaning |
| --- | --- | --- |
| frequency_hz | number >= 0 | tuned frequency |
| mode | text | modulation/decoder, e.g. `fm`, `sstv`, `aprs` |
| receiver | text | receiver identifier; `user-archive` for imported clips |
| decode | text | human-readable decode result and provenance |
| kind | text | `archive` = imported clip (not captured by this server); empty = live/scheduled capture |
| target | text | scheduled target name, e.g. `ISS`; empty = blind scan |
| gate | text | free-text gate description |
| gate_verdict | `passed` or `failed` | the Go live quality gate outcome; omit when no gate ran |
| crosscheck | see below | independent cross-check facet |
| crosscheck_ref | text | what the cross-check compared against |
| crosscheck_detail | text | how it compared, method and limits |
| source | text | provenance class: `live` = this receiver's own reception, `test-fixture` = synthetic, `satnogs` = imported; omitted by pre-field sidecars (UI shows "source not recorded", never guessed) |
| started | RFC3339 | capture start, UTC |
| duration_sec | number >= 0 | clip length |
| verified | `true` or `false` | legacy summary flag: the live gate passed |

## Consistency rules

These are errors, not conventions:

- `crosscheck=verified` requires `gate_verdict=passed`. "Verified live"
  means gate passed AND an independent reference confirmed the decode;
  the word `verified` in crosscheck is reserved for that independent
  confirmation.
- `verified=true` contradicts `gate_verdict=failed`.
- `gate_verdict` only takes `passed` or `failed`. A capture that ran no
  gate omits the key; the UI renders it as "gate: not recorded".
- `crosscheck=mismatch` means an independent reference contradicted the
  decode; it never pairs with `verified=true`.

Warnings flag honesty risks rather than hard contradictions: an
`kind=archive` clip claiming `gate_verdict=passed` (archive imports never
ran this server's live gate), or a descriptive crosscheck value containing
"verif" that could be misread as the reserved verdict.

Non-goal: the validator checks that a sidecar's claims are well-formed and
internally consistent. It cannot check that the claims are true - a
`crosscheck_ref` must name a reference that actually exists, and that stays
a human review responsibility.
