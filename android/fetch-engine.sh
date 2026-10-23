#!/usr/bin/env bash
# Cross-compile the sdrstream Go engine for Android and place it where the
# APK build expects it (jniLibs/<abi>/libsdrstreamd.so). Android extracts
# files from nativeLibraryDir as executable; app-private data dirs are
# noexec since Android 10, which is why the engine ships as a "library".
#
# CGO is REQUIRED: with CGO_ENABLED=0 the pure-Go resolver cannot read
# Android DNS config (no /etc/resolv.conf) and silently falls back to
# hardcoded public DNS, which carrier/NAT64 networks can break entirely
# (Go issue #10714: "DNS requests don't work on Android"). Linking bionic
# via the NDK clang makes the engine resolve through Android's own netd
# like every other app. The API-level wrapper matches minSdk so the binary
# loads on every supported Android version.
#
# Usage: ./fetch-engine.sh [arm64] [x86_64]   (default: arm64)
# x86_64 exists for the CI emulator; the shipping APK stays arm64-only.
set -euo pipefail
cd "$(dirname "$0")/../go"
arches=("$@")
if [ ${#arches[@]} -eq 0 ]; then arches=(arm64); fi

NDK=$(ls -d "$ANDROID_HOME/ndk/"* 2>/dev/null | sort -V | tail -1)
if [ -z "$NDK" ]; then
  echo "fetch-engine: no Android NDK found under \$ANDROID_HOME ($ANDROID_HOME)" >&2
  echo "fetch-engine: CGO is required for correct Android DNS; refusing to fall back to CGO_ENABLED=0" >&2
  exit 1
fi
CLANG_DIR="$NDK/toolchains/llvm/prebuilt/linux-x86_64/bin"
# Match the app's minSdk (26) so the binary loads on all supported devices;
# fall back to the 34 wrapper when the NDK no longer ships 26.
pick_cc() { # $1 = triple prefix (aarch64-linux-android / x86_64-linux-android)
  for api in 26 28 29 30 31 33 34; do
    if [ -x "$CLANG_DIR/$1$api-clang" ]; then echo "$CLANG_DIR/$1$api-clang"; return 0; fi
  done
  echo "fetch-engine: no $1<api>-clang wrapper in $CLANG_DIR" >&2; return 1
}

for arch in "${arches[@]}"; do
  case "$arch" in
    arm64)  goarch=arm64; abi=arm64-v8a; triple=aarch64-linux-android ;;
    x86_64|amd64) goarch=amd64; abi=x86_64; triple=x86_64-linux-android ;;
    *) echo "fetch-engine: unknown arch '$arch' (want arm64 or x86_64)" >&2; exit 1 ;;
  esac
  CC_BIN=$(pick_cc "$triple")
  mkdir -p "../android/app/src/main/jniLibs/$abi"
  CGO_ENABLED=1 GOOS=android GOARCH=$goarch CC="$CC_BIN" go build -trimpath -ldflags "-s -w" \
    -o "../android/app/src/main/jniLibs/$abi/libsdrstreamd.so" ./cmd/sdrstream
  echo "engine: android/app/src/main/jniLibs/$abi/libsdrstreamd.so (CGO via $(basename "$CC_BIN"))"
done
