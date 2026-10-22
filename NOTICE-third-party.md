# Third-party notices

This repository vendors or adapts third-party code. Copyright stays with
the original authors; each entry names the source and its license.

## go/internal/decode/ft8.go and ftx_tables.go

Go port of the decode path of kgoba/ft8_lib (MIT License, Copyright (c)
2018 Karlis Goba); the LDPC tables come from WSJT-X via ft8_lib. License
text: go/internal/decode/testdata/ft8_lib.LICENSE. Test recordings under
go/internal/decode/testdata/ft8 come from the ft8_lib test suite.

## go/internal/rivaspeech

Go bindings generated from NVIDIA's Riva speech proto files
(`riva_asr.proto`, `riva_audio.proto`, `riva_common.proto`), fetched from
github.com/nvidia-riva/common (riva/proto, main branch, 2026-10-22) and
compiled with protoc 29.3, protoc-gen-go and protoc-gen-go-grpc.

Copyright NVIDIA CORPORATION and Google LLC. License: MIT / Apache-2.0,
as stated in the SPDX and license headers preserved at the top of each
generated file.

## ACARS MSK demodulator and framer (go/internal/decode/acars.go)

Ported to Go from sdr-acars (https://github.com/jasonherald/sdr-acars),
`src/msk.rs` and `src/frame.rs`, licensed LGPL-2.0. sdr-acars is itself a
port of acarsdec by Thierry Leconte (https://github.com/TLeconte/acarsdec).
Copyright remains with those authors. The 7-block regression oracle comes
from the sdr-acars test fixture derived from acarsdec's test.wav; the WAV
is not vendored in this repository.

# Third-party code and attribution

## HFDL decoder (go/internal/hfdl)

Ported to Go from:

- **dumphfdl** - Copyright (c) Tomasz Lemiech. GPL-3.0-or-later.
  https://github.com/szpajder/dumphfdl
  Used for: frame structure and state machine, A/M1/T sequences, scrambler,
  deinterleaver, per-mode parameters, MPDU/LPDU/SPDU/HFNPDU parsing, CRC.
- **libacars** - Copyright (c) 2018-2023 Tomasz Lemiech. MIT.
  https://github.com/szpajder/libacars
  Used for: ACARS block parsing (acars.c) and its CRC (crc.c).
- **libfec** (K=7 Viterbi decoder as shipped in dumphfdl) - Copyright Phil
  Karn, KA9Q. LGPL-2.1-or-later. Used for: the soft-decision Viterbi decoder.
- **liquid-dsp** - Copyright (c) 2007-2023 Joseph Gaeddert. MIT.
  https://github.com/jgaeddert/liquid-dsp
  Used for: soft PSK demapper scaling, AGC update rule, the m-sequence LFSR.

The HFDL package as a whole is therefore distributed under terms compatible
with GPL-3.0-or-later.

Test audio is not part of this repository. The tests read public recordings
from signals.taunus.de when `HFDL_SAMPLES` points at them.


go/internal/decode/wspr.go and wspr_tables.go are a Go port of the single-pass
path of wsprd from WSJT-X (GNU GPL v3; Copyright 2001-2018 Joe Taylor K1JT,
2014-2018 Steven Franke K9AN; Fano decoder Copyright 1994 Phil Karn KA9Q),
read from github.com/pavel-demin/wsprd. These files are GPL v3, so a binary
that includes them is GPL v3. Reference oracle for tests: wsprd built from
the same source.

