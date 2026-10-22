# External reference decoders (subprocess)

`go/internal/decode/reference.go` wraps unmodified upstream decoders behind
one `RefDecoder` interface and runs them as child processes on WAV files:

| Tool | Modes | Licence | Source |
|---|---|---|---|
| WSJT-X `jt9` | FT8 (`-8`), FT4 (`-4`), JT65, JT9 | GPL-2.0+ | Ubuntu `wsjtx` package / wsjt.sourceforge.io |
| WSJT-X `wsprd` | WSPR | GPL-2.0+ | same package as jt9 |
| `decode_ft8` (kgoba/ft8_lib) | FT8, FT4 | MIT | github.com/kgoba/ft8_lib (`make decode_ft8`) |
| `multimon-ng` | POCSAG, FLEX, AFSK1200 (APRS), MORSE_CW | GPL-2.0+ | Ubuntu `multimon-ng` package |
| `acarsdec` | ACARS | LGPL-2.0 | github.com/TLeconte/acarsdec (built `WITH_SNDFILE`, WAV input at 12500 Hz) |

## Licensing: GPL as a separate process

Every tool above runs as an **unmodified child process** exchanging data
only through WAV files and stdout (mere aggregation). sdrstream's Go code
does not link against them, does not copy their code, and does not modify
them; the AGPL-3.0 of sdrstream does not extend to the tools, and their
GPL/LGPL terms do not extend to sdrstream. Tools are fetched/built at
setup time by the operator, not redistributed in this repo.

## Resolution order for each binary

1. explicit path passed to the constructor,
2. `SDRSTREAM_REF_<NAME>` (JT9, WSPRD, FT8LIB, MULTIMON, ACARSDEC),
3. `SDRSTREAM_REFDEC_DIR` (`<dir>/usr/bin/<tool>` or `<dir>/<tool>`),
4. `$PATH`.

When the binary lives in an extracted-package layout (`<prefix>/usr/bin`
plus `<prefix>/usr/lib/<triplet>`), the bundled library dir is prepended to
`LD_LIBRARY_PATH` for the child only.

## arm64 (and other non-x86) builds

- `wsjtx` and `multimon-ng`: Ubuntu ships arm64 builds on
  ports.ubuntu.com (jammy: wsjtx 2.5.4+repack-1 arm64,
  multimon-ng 1.1.9+dfsg-2 arm64); `apt-get download` + `dpkg-deb -x`
  works exactly like amd64.
- `ft8_lib`: portable C99, `make decode_ft8` builds natively on arm64.
- `acarsdec`: portable C; build the WAV-input subset with
  `gcc -O2 acars.c acarsdec.c cJSON.c label.c msk.c output.c netout.c
  fileout.c soundfile.c -DWITH_SNDFILE -lsndfile -lm -lpthread`
  (drop `-march=native` from CMakeLists when cross-compiling).
  WAV input must be mono/multi-channel PCM at exactly 12500 Hz
  (INTRATE); `decode.AcarsdecWAV` resamples.

## Bake-off (decoderegress -bakeoff), 2026-10-22, amd64 sandbox

Per-family recall against the golden reference lists (all corpus cases
with goldens; ft8 = 20 recordings, wspr = 3 live Twente captures):

| family | Go | best reference | call (2% rule) |
|---|---|---|---|
| ft8 | 78.9% | jt9 98.5% (ft8lib 76.8%) | **prefer jt9** |
| wspr | 87.5% | wsprd 100% | **prefer wsprd** |
| pocsag | 100% | multimon-ng 100% | keep Go |
| flex | 100% | multimon-ng 67.5% | keep Go |
| aprs | 100% | multimon-ng 100% | keep Go |
| acars | 100% | acarsdec 100% | keep Go |

The verdict is encoded in `decode.refPreferred` (engine.go):
`FTXDecodeAuto` / `WSPRDecodeAuto` run the reference tool when it resolves
and fall back to the Go decoder on any failure or missing binary.
`SDRSTREAM_DECODE_ENGINE=go|ref` forces an engine.
