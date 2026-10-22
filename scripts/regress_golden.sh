#!/usr/bin/env bash
# Regenerate go/internal/regress/golden/*.txt from the reference decoders.
# Needs a populated recording cache (decoderegress -fetch), and:
#   FT8LIB_DIR  built kgoba/ft8_lib checkout (decode_ft8)         MIT
#   MMNG        path to a built multimon-ng binary                GPL-2.0, run as an external tool only
#   sox, ffmpeg, python3
# The reference tools are not vendored; only their output (decode lists) is.
set -euo pipefail
CACHE="${CACHE:-/tmp/sdrstream-regress}"
OUT="$(cd "$(dirname "$0")/../go/internal/regress/golden" && pwd)"
: "${FT8LIB_DIR:?}" "${MMNG:?}"
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT

# FT8: ft8_lib test files carry their own WSJT-X decode list (.txt); the
# WSJT sample recordings have none, so ft8_lib's C decoder is the reference.
for w in "$CACHE"/*.wav; do
  b="$(basename "$w" .wav)"
  case "$b" in
    191111_*|websdr_test*)
      "$FT8LIB_DIR/decode_ft8" "$w" | sed -n 's/.*~ *//p' | sed 's/  .*//; s/ *$//' | { grep -v 'while unpacking' || true; } | sort -u > "$OUT/ok_ft8_$b.txt"  # second decoder: accepted extras
      if [ -f "$CACHE/$b.txt" ]; then
        sed -n 's/.*~ *//p' "$CACHE/$b.txt" | sed 's/  .*//; s/ *$//' | sort -u > "$OUT/ft8_$b.txt"
      fi;;
    1[0-9][0-9][0-9][0-9][0-9]_*|2[0-9][0-9][0-9][0-9][0-9]_*)
      case "$b" in 150426_0918) continue;; esac
      "$FT8LIB_DIR/decode_ft8" "$w" | sed -n 's/.*~ *//p' | sed 's/  .*//; s/ *$//' | { grep -v 'while unpacking' || true; } | sort -u > "$OUT/ft8_$b.txt";;
  esac
done

# POCSAG / FLEX via multimon-ng at 22050 Hz raw.
poc() { # name wavfile mode
  sox "$2" -t raw -r 22050 -e signed -b 16 -c 1 "$tmp/in.raw"
  "$MMNG" -t raw -a "$3" "$tmp/in.raw" 2>/dev/null | python3 -c '
import re,sys
for l in sys.stdin:
    m=re.match(r"POCSAG\d+: Address:\s*(\d+)\s+Function:\s*(\d+)\s+(Alpha|Numeric):\s*(.*)$",l.rstrip("\n"))
    if m: print("%s|%s|%s"%(m.group(1),m.group(2),m.group(4)))
' | sort -u > "$OUT/pocsag_$1.txt"
}
poc sample_512  "$CACHE/POCSAG_sample_-_512_bps.wav"  POCSAG512
poc sample_1200 "$CACHE/POCSAG_sample_-_1200_bps.wav" POCSAG1200
poc sample_2400 "$CACHE/POCSAG_sample_-_2400_bps.wav" POCSAG2400
poc taunus_512  "$CACHE/POCSAG-512.WAV"  POCSAG512
poc taunus_1200 "$CACHE/POCSAG-1200.WAV" POCSAG1200

sox "$CACHE/FLEX_1600_2fsk_P2000_proef_alarm.wav" -t raw -r 22050 -e signed -b 16 -c 1 "$tmp/in.raw"
"$MMNG" -t raw -a FLEX "$tmp/in.raw" 2>/dev/null | python3 -c '
import sys
for l in sys.stdin:
    p=l.rstrip("\n").split("|")
    if len(p)>=7 and p[0]=="FLEX" and p[5]=="ALN" and p[4].strip().isdigit(): print("%d|%s"%(int(p[4]),"|".join(p[6:])))
' | sort -u > "$OUT/flex_p2000.txt"

[ -f "$CACHE/acarsdec_expected.txt" ] || curl -sSL -A "sdrstream-decoder-regress/1.0" -o "$CACHE/acarsdec_expected.txt" \
  https://raw.githubusercontent.com/jasonherald/sdr-acars/main/tests/fixtures/acarsdec_test_wav_expected.txt
# ACARS: acarsdec reference output (sdr-acars fixture) -> reg|label|mode|blockid
python3 - "$CACHE/acarsdec_expected.txt" > "$OUT/acars_acarsdec_test.txt" <<'PY'
import re,sys
t=open(sys.argv[1]).read()
for blk in t.split("[#"):
    m=re.search(r"Mode : (\S) Label : (\S+) Id : (\S)",blk); r=re.search(r"Aircraft reg: (\S+)",blk)
    if m and r: print("%s|%s|%s|%s"%(r.group(1),m.group(2),m.group(1),m.group(3)))
PY
wc -l "$OUT"/*.txt | tail -1

# APRS: multimon-ng AFSK1200 on the committed 30 s WA8LMF excerpt -> SRC>DST
sox "$(dirname "$0")/../tests/data/real_aprs_wa8lmf_30s.wav" -t raw -r 22050 -e signed -b 16 -c 1 "$tmp/in.raw"
"$MMNG" -t raw -a AFSK1200 "$tmp/in.raw" 2>/dev/null | sed -n 's/^AFSK1200: fm \([^ ]*\) to \([^ ]*\) .*/\1>\2/p' | sort -u > "$OUT/aprs_wa8lmf_30s.txt"
# WSPR goldens (wsprd 1.x output, taken from the committed live-capture
# tests) are maintained by hand: wspr_utwente_*.txt
