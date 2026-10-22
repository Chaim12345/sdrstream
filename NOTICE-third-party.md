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

## Open licence decisions (pending owner, 2026-10-22)

QA-prepared summary of the three licence questions that need an owner
decision. Nothing in this section changes shipped text; it only records the
options so the decision can be applied in one commit each.

1. **WebSDR companding table** (`go/internal/vocoder/table.go`,
   `sdrstream/_websdr_table.py`). The 256-entry table is copied from the
   WebSDR HTML5 client, copyright 2013-2025 PA3FWM, all rights reserved;
   redistribution permission has NOT been obtained.
   Options: (a) email PA3FWM asking for permission and cite it here;
   (b) replace the table with a computed ITU G.711 A-law table and handle
   the 32 differing entries as a documented compatibility delta;
   (c) clean-room re-measure the curve from a live WebSDR stream.
   Until decided, the table stays with its warning notice.

2. **Two off-air WAV fixtures** (`tests/data/real_aprs_wa8lmf_30s.wav` from
   the WA8LMF TNC Test CD, and `tests/data/real_pocsag_1200_taunus.wav` from
   signals.taunus.de). Neither source states a redistribution licence (see
   `tests/data/NOTICE` TODOs).
   Options per file: (a) ask the author/site owner for explicit
   redistribution permission; (b) replace with a self-recorded or synthetic
   clip; (c) drop the fixture and fetch it in the test harness instead of
   committing it.

3. **Project copyright line.** The only project-level line today is
   `README.md`: "Copyright (C) 2026 Chaim12345 and sdrstream contributors."
   Decide whether that exact form is the canonical one; once confirmed it
   should also head this NOTICE file and any source headers, replacing the
   handle with a legal name if the owner prefers.
