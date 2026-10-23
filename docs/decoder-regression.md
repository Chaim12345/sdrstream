# Decoder regression harness

`go/cmd/decoderegress` runs the Go decoders (FT8, WSPR, POCSAG, FLEX, ACARS,
HFDL, APRS/AFSK1200, SSTV) over public real recordings and prints a table of decode
counts against reference decodes.

    cd go
    go run ./cmd/decoderegress -fetch -v        # fetch recordings (1 request/s), run, list misses
    go run ./cmd/decoderegress -only pocsag     # one decoder or case-name substring
    go run ./cmd/decoderegress -json            # machine-readable
    go run ./cmd/decoderegress -write-baseline > internal/regress/golden/baseline.txt   # review the diff

Recordings are cached in `$TMPDIR/sdrstream-regress` (`-cache`). Each remote file
is pinned by SHA-256; a changed upstream file fails the fetch. FLAC sources
need `ffmpeg` to convert to WAV. The harness sends a descriptive User-Agent and
pauses `-delay` (default 1 s) after each download.

## Status meaning

| Status | Meaning |
| --- | --- |
| PASS | every reference message decoded, no unconfirmed extras |
| GAP  | at or above the recorded baseline but below the reference (a known decoder gap) |
| FAIL | below the baseline floor, or more unconfirmed extras than the baseline allows. Exit status 1 |
| INFO | no reference decoder available, decodes listed only |
| SKIP | recording not in the cache |

Cases marked `Negative` are ground truth that must NOT decode: any decoded
item fails the case, so a detector that fires on everything cannot pass.
Image decoders gate on a full frame decode, not only a header detect -
header-only clips stay a failure (see the SSTV unit tests).

Columns: EXPECTED reference messages, GOT distinct decodes, MATCHED, MISSED,
EXTRA (not in the reference nor the second decoder), BONUS (not in WSJT-X but
decoded by ft8_lib's C decoder), UNSUP (CRC-valid FT8 frames of a message type
the Go decoder cannot unpack).

`golden/baseline.txt` holds `<case> <floor> <maxextra>`. Improving a decoder
raises the floor; commit the new baseline with the change.

## Sources and references

| Decoder | Recordings | Reference |
| --- | --- | --- |
| FT8 | kgoba/ft8_lib `test/wav` (MIT) | WSJT-X decode lists shipped next to each WAV; ft8_lib C decoder as second opinion |
| FT8 | WSJT sample archive (SourceForge `wsjt/files/samples/FT8`) | ft8_lib C decoder (built from source, MIT) |
| WSPR | committed Twente WebSDR captures; WSJT `samples/WSPR/150426_0918.wav` | wsprd 1.x (from the existing tests); none for the WSJT file |
| POCSAG | multimon-ng `test/samples` flac (GPL-2.0), signals.taunus.de POCSAG-512/1200 | multimon-ng run as an external tool |
| FLEX | multimon-ng `test/samples` P2000 flac | multimon-ng |
| ACARS | TLeconte/acarsdec `test.wav` (LGPL-2.0) | acarsdec output (sdr-acars fixture) |
| HFDL | signals.taunus.de HFDL 300/600/1200 bps | at least one FCS-verified frame (no reference decoder run) |
| APRS | committed WA8LMF TNC test CD excerpt | multimon-ng AFSK1200 |
| SSTV (synthetic) | committed pysstv-generated Robot36 fixture (project-generated pattern; synthetic, not on-air proof) | VIS detect + full frame decode |
| SSTV (negative) | committed generated RTTY clip | must produce zero detections |

A real off-air SSTV positive sample is wanted but not yet rights-clean for
redistribution; add one only with established redistribution terms.

Reference decoders are never vendored; only their output lists are, as
`internal/regress/golden/*.txt`. `scripts/regress_golden.sh` regenerates them
(needs a built ft8_lib, a built multimon-ng, sox, ffmpeg, python3).

Offline CI: `go test ./internal/regress` runs the committed recordings only.
With a populated cache, `SDRSTREAM_REGRESS_CACHE=<dir> go test ./internal/regress`
runs the whole manifest against the baseline.
