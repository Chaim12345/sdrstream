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
