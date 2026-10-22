# Third-party notices

## ACARS MSK demodulator and framer (go/internal/decode/acars.go)
Ported to Go from sdr-acars (https://github.com/jasonherald/sdr-acars),
`src/msk.rs` and `src/frame.rs`, licensed LGPL-2.0. sdr-acars is itself a
port of acarsdec by Thierry Leconte (https://github.com/TLeconte/acarsdec).
Copyright remains with those authors. The 7-block regression oracle comes
from the sdr-acars test fixture derived from acarsdec's test.wav; the WAV
is not vendored in this repository.
