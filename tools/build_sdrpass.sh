#!/bin/sh
# Build the sdrpass Go helper used for fast satellite pass prediction.
#
# The binary is optional; sdrstream falls back to pure-Python prediction when
# it is absent. Point SDRPASS_BIN at a different binary to override the path.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
src="$here/../gotools/sdrpass"

if ! command -v go >/dev/null 2>&1; then
    echo "build_sdrpass: the Go toolchain is not on PATH" >&2
    echo "install Go from https://go.dev/dl/ and try again" >&2
    exit 1
fi

cd "$src"
go build -o sdrpass .
echo "built $src/sdrpass"
