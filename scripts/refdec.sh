#!/usr/bin/env bash
# Fetch the external reference decoders as unmodified Ubuntu packages and
# extract them into a private prefix (no root needed). Run once per sandbox:
#
#   bash scripts/refdec.sh          # install + verify
#   . scripts/refdec.sh             # also export SDRSTREAM_REFDEC_DIR
#
# The Go code resolves binaries via SDRSTREAM_REF_<NAME>, SDRSTREAM_REFDEC_DIR
# or $PATH and sets LD_LIBRARY_PATH for the child itself; see
# go/docs/EXTERNAL-DECODERS.md for the licensing model (mere aggregation).
set -uo pipefail
SDR_REFDEC="${SDR_REFDEC:-$HOME/refdec}"
mkdir -p "$SDR_REFDEC/debs"
cd "$SDR_REFDEC/debs"
# minimodem (RTTY reference) and its library deps, Ubuntu jammy amd64.
pkgs="minimodem libsndfile1 libfftw3-double3 libasound2 libpulse0 libopus0 libflac8 libvorbisenc2 libogg0 libmpg123-0 libmp3lame0 libasyncns0"
for p in $pkgs; do
  ls "$p"_*.deb >/dev/null 2>&1 || apt-get download "$p" >/dev/null 2>&1 || echo "warn: $p not downloaded" >&2
done
for f in *.deb; do dpkg-deb -x "$f" "$SDR_REFDEC/root"; done
if [ -x "$SDR_REFDEC/root/usr/bin/minimodem" ]; then
  LD_LIBRARY_PATH="$SDR_REFDEC/root/usr/lib/x86_64-linux-gnu" "$SDR_REFDEC/root/usr/bin/minimodem" -V | head -1
  echo "refdec ready: export SDRSTREAM_REFDEC_DIR=$SDR_REFDEC/root"
else
  echo "minimodem install failed" >&2
  return 1 2>/dev/null || exit 1
fi
