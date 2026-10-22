# Source me: puts a user-writable Go toolchain on PATH, installing it if missing.
# Needed because the sandbox /usr/local/go shim fails on /opt/lazy/go.lock.
SDR_TOOLCHAIN="${SDR_TOOLCHAIN:-$HOME/toolchain}"
GO_VERSION="${GO_VERSION:-1.27.3}"
GO_SHA256="${GO_SHA256:-65efbc80fdc1fa2cd2631cad5b481777a68e8725f34e89f50fc6ac714e379b40}"  # linux-amd64
if [ ! -x "$SDR_TOOLCHAIN/go/bin/go" ]; then
  mkdir -p "$SDR_TOOLCHAIN"
  tarball="$SDR_TOOLCHAIN/go${GO_VERSION}.linux-amd64.tar.gz"
  curl -fsSL -o "$tarball" "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" || return 1
  echo "$GO_SHA256  $tarball" | sha256sum -c - >/dev/null || { echo "go checksum mismatch" >&2; rm -f "$tarball"; return 1; }
  tar -xzf "$tarball" -C "$SDR_TOOLCHAIN" && rm -f "$tarball"
fi
export PATH="$SDR_TOOLCHAIN/go/bin:$PATH"
export GOPATH="${GOPATH:-$SDR_TOOLCHAIN/gopath}"
