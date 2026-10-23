#!/usr/bin/env bash
# Cross-compile the sdrstream Go engine for Android and place it where the
# APK build expects it (jniLibs/<abi>/libsdrstreamd.so). Android extracts
# files from nativeLibraryDir as executable; app-private data dirs are
# noexec since Android 10, which is why the engine ships as a "library".
#
# Usage: ./fetch-engine.sh [arm64] [x86_64]   (default: arm64)
# x86_64 exists for the CI emulator; the shipping APK stays arm64-only.
set -euo pipefail
cd "$(dirname "$0")/../go"
arches=("$@")
if [ ${#arches[@]} -eq 0 ]; then arches=(arm64); fi
for arch in "${arches[@]}"; do
  case "$arch" in
    arm64)  goarch=arm64; abi=arm64-v8a ;;
    x86_64|amd64) goarch=amd64; abi=x86_64 ;;
    *) echo "fetch-engine: unknown arch '$arch' (want arm64 or x86_64)" >&2; exit 1 ;;
  esac
  mkdir -p "../android/app/src/main/jniLibs/$abi"
  CGO_ENABLED=0 GOOS=android GOARCH=$goarch go build -trimpath -ldflags "-s -w" \
    -o "../android/app/src/main/jniLibs/$abi/libsdrstreamd.so" ./cmd/sdrstream
  echo "engine: android/app/src/main/jniLibs/$abi/libsdrstreamd.so"
done
