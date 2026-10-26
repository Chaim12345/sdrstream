# Third-party notices

This notice identifies third-party components incorporated into sdrstream or
adapted to implement its decoders. Each entry retains its upstream copyright
and license terms. The repository's AGPL-3.0-or-later terms apply to the
combined work only to the extent compatible with each component's original
license. See `LICENSE` for the GNU Affero General Public License.

## GNU GPL-3.0-or-later-derived components

- `go/internal/decode/wspr.go`, `go/internal/decode/wspr_tables.go`: Go port
  of the single-pass `wsprd` decoder path and soft-symbol metric table from
  WSJT-X, Copyright 2001-2018 Joe Taylor K1JT, 2014-2018 Steven Franke K9AN;
  Fano decoder Copyright 1994 Phil Karn KA9Q. Source: WSJT-X project,
  https://wsjt.sourceforge.io/ .
- `go/internal/hfdl`: portions ported from `dumphfdl`, Copyright Tomasz
  Lemiech. Source: https://github.com/szpajder/dumphfdl . The Viterbi decoder
  in `go/internal/hfdl/viterbi.go` is separately from libfec, LGPL-2.1-or-later.
- `go/internal/decode/flex.go`: FLEX demodulator/decoder follows
  `demod_flex.c`, Copyright Elias Oenal and contributors, under
  GPL-2.0-or-later. This is explicitly GPL-2.0-or-later, not GPL-2.0-only.
  Source: https://github.com/EliasOenal/multimon-ng .

## Protocol specifications and independently implemented ACARS

- `go/internal/decode/acars.go`: the MSK demodulator was rewritten as an
  independent coherent receiver. Its framing and field extraction follow the
  publicly documented ARINC 618 / ACARS protocol; protocol facts were checked
  against https://gophertrunk.org/acars.html and J.-M. Friedt, “ACARS: Aircraft
  Communications Addressing and Reporting System,”
  https://jmfriedt.free.fr/sdra_acars.pdf . The retained utility/wrapper pieces
  are `ACARSDecode` field extraction, `ReadWAVChannels`, `acarsLabel`,
  `acarsAck`, `acarsCRCUpdate`, and the protocol doc comment. These cover
  sample input, decoding orchestration, message field mapping, and the
  specified CRC calculation; no LGPL-only MSK demodulator or copied ACARS
  implementation is included. No ACARS recording is distributed in this tree.

## External reference tools (subprocess only, not linked or redistributed)

- WSJT-X `jt9` and `wsprd` (GPL-2.0-or-later, Joe Taylor K1JT and
  contributors), `multimon-ng` (GPL-2.0-or-later, Elias Oenal and
  contributors) and `acarsdec` (LGPL-2.0, Thierry Leconte F4DWV) may be
  invoked as unmodified child processes on WAV files (mere aggregation);
  no code from these tools is copied into sdrstream. `decode_ft8` from
  kgoba/ft8_lib (MIT, Karlis Goba) may be used the same way. See
  `go/docs/EXTERNAL-DECODERS.md`.

## MIT and Apache-2.0 components

- `go/internal/decode/ft8.go`, `go/internal/decode/ftx_tables.go`: Go port of
  kgoba/ft8_lib decode path, Copyright 2018 Karlis Goba, MIT. The upstream
  license is retained at `go/internal/decode/testdata/ft8_lib.LICENSE`.
  https://github.com/kgoba/ft8_lib .
- `go/internal/hfdl`: parsing adapted from libacars (MIT), Copyright
  2018-2023 Tomasz Lemiech; soft-demapper scaling informed by liquid-dsp (MIT),
  Copyright 2007-2023 Joseph Gaeddert.
- `go/internal/rivaspeech`: generated protobuf/gRPC bindings preserve NVIDIA
  and Google MIT/Apache-2.0 copyright and license headers in each source file.
- `go/internal/vocoder`, `sdrstream/_websdr_table.py`: the 256-entry sample
  table is taken from the WebSDR HTML5 client, copyright 2013-2025 PA3FWM.
  **Permission to redistribute or reuse it has NOT been obtained.** Upstream
  reserves all rights; see the open issue in `NOTICE`.
- `go/internal/web/static/vendor/leaflet`: Leaflet 1.9.4, BSD-2-Clause,
  Copyright (c) 2010-2023 Vladimir Agafonkin, (c) 2010-2011 CloudMade. License
  text in `go/internal/web/static/vendor/leaflet/LICENSE`.
- `go/internal/adpcm`: mirrors OpenWebRX's public IMA-ADPCM decoder behavior;
  no upstream source file is included. OpenWebRX source: https://github.com/jketterl/openwebrx .

## Independently written decoders and protocol material

The aircraft/marine message semantics (`go/internal/decode/modes.go`,
`aisnmea.go`, `acarsmsg.go`) are original implementations from public
protocol specifications and community-feed payloads. The satellite GMSK/BPSK
demodulators and blob pipeline are original implementations and use public
protocol specifications. Other decoders that cite multimon-ng follow its
published protocol/framing descriptions or use its output only as an external
reference tool; multimon-ng itself is not bundled or linked into sdrstream.

Third-party sample audio: two real off-air recordings in `tests/data` and the
recordings under `go/internal/decode/testdata` are third-party material. Their
sources and open licence questions are in `tests/data/NOTICE`; `ft8_lib.LICENSE`
covers the ft8_lib port only. The test harness fetches
recordings when explicitly requested.

## sgoudelis/ground-station (design reference, GPL-3.0)

`go/internal/passplan` re-implements, independently in Go, the pass-observation
scheduling rules of ground-station (pinned 351631d3066b,
`backend/observations/{constants,conflicts,generator}.py`): peak-elevation
filter, task window from elevation crossing, +/-5 min overlap tolerance,
shared-resource conflicts, priority/skip/force strategies, status lifecycle.
No upstream source text was copied. Upstream is GPL-3.0; sdrstream is AGPL-3.0.
