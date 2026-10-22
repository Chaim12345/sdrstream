#!/usr/bin/env bash
# Cross-compile the sdrstream Go engine for Android and place it where the
# APK build expects it (jniLibs/<abi>/libsdrstreamd.so). Android extracts
# files from nativeLibraryDir as executable; app-private data dirs are
# noexec since Android 10, which is why the engine ships as a "library".
set -euo pipefail
cd "$(dirname "$0")/../go"
CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build -trimpath -ldflags "-s -w" \
  -o ../android/app/src/main/jniLibs/arm64-v8a/libsdrstreamd.so ./cmd/sdrstream
echo "engine: android/app/src/main/jniLibs/arm64-v8a/libsdrstreamd.so"
