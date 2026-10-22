# Weak-signal decoder increment

## Changes

FT8 keeps its existing 2x time/frequency search, score 10, 140 candidates and
25 LDPC iterations. It now performs up to three passes. After a pass, only
LDPC- and CRC-verified codewords are remapped to their 79 transmitted tones.
The decoded track's immediate time/frequency neighborhood is lowered to an
estimate of the nearby noise floor before searching again. An immutable
waterfall snapshot is used for extracting all codewords in that pass.
This is conservative waterfall-domain suppression, not coherent waveform
subtraction. The first-pass results are retained. FT4 remains single-pass.

Ablations tested but not adopted: 600/1000 candidates and 60 LDPC iterations
added no reference messages; 4x time search and frequency zero-padding found
more reference messages but also extra messages not independently verified.
No recording-specific exclusions or callsign filters were introduced.

## Real recordings

The ft8_lib corpus improves from 227/305 (74.43%) to 236/305 (77.38%): nine
additional reference messages, no lost reference messages, zero unconfirmed
extras. Six existing independently confirmed bonus messages remain. All
per-recording counts are in [the before/after table](proof/weak-signal/table.md).
The full harness output is in the same directory. This does not establish a
universal false-positive rate; four seeded noise slots and a silence slot are
also checked and produce no messages. The largest remaining deficits include
websdr_test5/6/7/11/12. These are archived real-audio tests, not live reception.

## Type 3 is real RTTY-RU, not a CRC false positive

On WSJT sample 181201_180245 the C ft8_lib decoder independently recovers
exactly the same six 77-bit type-3 payloads as Go. It returns ERROR_TYPE only
at the text-unpack stage. The transmitted layout is 1 TU bit, two 28-bit
callsigns, 1 R bit, 3 report bits, 13 exchange bits, and 3 type bits.
The new original Go unpacker follows that protocol and validates the exchange
range. Examples: `KV7J WD9HSY 529 IL`, `WB3LGC AA5AU R 529 LA`.

The six raw C payloads and all eight original standard payloads are preserved
in proof/weak-signal/ft8-lib-payloads.txt. The golden set explicitly adds the
six new supported messages, making this sample 14/14, rather than relabeling
unknown extras as accepted. This is independent payload verification plus
protocol-based unpacking, not an independent WSJT-X executable text decode.
No WSJT-X implementation code was copied. Sources inspected:

- https://github.com/kgoba/ft8_lib/blob/master/ft8/message.h
- https://github.com/kgoba/ft8_lib/blob/master/ft8/decode.c
- https://github.com/WSJTX/wsjtx/blob/master/lib/77bit/packjt77.f90

## FLEX

`FlexDecode` retains raw page/fragment output for compatibility with the
multimon-ng reference. When several sample phases recover a page, it now
chooses the phase with the fewest BCH corrections. P2000 improves from 40
reference fragments plus two corrupt alternatives to exactly 40/40, no extras.

`FlexReassemble(FlexDecode(samples, rate))` joins ordered fragment chains by
capcode and fragment sequence, with a maximum eight-frame gap. It suppresses
orphan tails and incomplete chains, so it is deliberately separate from the
raw-fragment API. Its state is limited to this call: it does not join across
separate audio captures. Both capcodes 1400141 and 1400285 produce:

`Test: Proefalarm Ochtend Brandweer Veiligheidsregio Rotterdam Rijnmond.`

Tests cover real P2000 (when cached), synthetic chain boundaries, missing
fragments, wrong capcodes and excessive frame gaps. Sequence wrap across a
full 16-cycle recording is not supported. The existing raw decoder still
supports only phase-A 1600 baud 2-level alphanumeric traffic.

Protocol/reference inspection:
https://github.com/EliasOenal/multimon-ng/blob/master/demod_flex.c

All new implementation code is original. Existing ft8_lib-derived code keeps
its MIT attribution. Existing FLEX provenance is unchanged; no multimon-ng
source was copied in this increment. Recordings remain in the harness cache,
not newly redistributed in this change.

## Verification

`STRICT_FMT=1 scripts/ci.sh` passed (format, vet, build, complete tests).
`cd go && go test -race ./...` passed. Final full regression: 18 PASS,
19 GAP, 0 FAIL, 1 INFO, 0 SKIP. GAPs are tracked decoder shortfalls, not
complete decode success. WSPR and other decoder counts are unchanged.
Full logs: docs/proof/weak-signal/{ci,race,after}.txt.
