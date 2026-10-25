# sdrstream

Finds live transmissions on public SDR receivers, decodes them, and
surfaces the results in a web UI and Android app.

## Language

**Candidate**:
A detected spectral peak under observation; it becomes a reported
transmission only after holding the start level through the dwell time.
_Avoid_: detection, hit (as nouns)

**Skirt peak**:
A narrow spectral artifact (<=5 kHz, including zero-width) at the edge
of a strong wide signal such as broadcast FM; suppressed from candidacy
so it cannot monopolize the decode worker.
_Avoid_: edge hit, sideband blip

**Benign close**:
A log-drain read interrupt caused by the engine process being destroyed
during a normal restart; recorded as nothing, never as a crash.
_Avoid_: crash (for this case)

**Spot**:
A parsed station report line from a dxcluster feed; non-spot lines
(banner, chatter) still prove the feed is alive.
_Avoid_: message, packet

**Archive**:
The record-all store: every detection the fleet makes, kept for later
review. _Avoid_: history, log

**Ledger**:
The proof ledger: per-decode evidence rows exposed at
`/api/ledger.json`, the answer to "show real decoded data".
_Avoid_: report, summary
