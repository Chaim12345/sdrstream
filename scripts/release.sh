#!/usr/bin/env bash
# Build static sdrstream binaries for every supported platform into dist/.
#
# Usage: scripts/release.sh [version]   (default version: "dev")
#
# CGO is disabled everywhere: the resulting binaries are fully static and
# need nothing but a kernel. Reference decoders (jt9/wsprd/multimon-ng)
# are separate processes installed by scripts/install.sh or the Docker
# image; they are never linked into these binaries.
set -euo pipefail

version="${1:-dev}"
root="$(cd "$(dirname "$0")/.." && pwd)"
out="$root/dist/$version"
mkdir -p "$out"

cd "$root/go"

build() { # goos goarch [suffix]
  local goos="$1" goarch="$2" suffix="${3:-}"
  local name="sdrstream_${version}_${goos}_${goarch}${suffix}"
  echo "building $name"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags "-s -w" -o "$out/$name" ./cmd/sdrstream
}

build linux   amd64
build linux   arm64
build darwin  amd64
build darwin  arm64
build windows amd64 .exe
build android arm64   # full-stack Android app payload (see android/)

( cd "$out" && sha256sum sdrstream_* > "SHA256SUMS" )
echo "dist: $out"
ls -la "$out"
