# sdrstream for Android (full stack)

The whole sdrstream server runs **on the phone**: the same Go binary that
runs on servers, cross-compiled `GOOS=android GOARCH=arm64 CGO_ENABLED=0`
(no NDK needed - the codebase has no cgo), shipped inside the APK as a
native "library" and started by a foreground service. The UI is the normal
sdrstream web UI in a WebView pointed at `http://127.0.0.1:8080`.

## Build

Prereqs: JDK 17, Android SDK (platform 34, build-tools), Go 1.25+.

```bash
./fetch-engine.sh            # cross-compiles the Go engine into jniLibs/
./gradlew assembleDebug      # or assembleRelease with your signing config
```

The APK lands in `app/build/outputs/apk/`.

## How it works / limits

- **Exec model:** Android 10+ blocks `exec()` from app-private data dirs,
  so the engine binary is packaged as `jniLibs/arm64-v8a/libsdrstreamd.so`
  with `extractNativeLibs="true"` and started with `ProcessBuilder` from
  `nativeLibraryDir`. No root, no gomobile.
- **Lifecycle:** a foreground service (`dataSync`) with a persistent
  notification keeps the process alive; the app asks to be exempted from
  battery optimizations because Doze can still suspend networking. Scanning
  a large receiver fleet is bandwidth- and CPU-heavy - expect real battery
  drain while it runs.
- **State:** captures, the proof ledger and rotation state live in the
  app's private files dir (`SDRSTREAM_STATE_DIR`); engine stdout/stderr go
  to `files/engine.log`.
- **DNS:** the engine is built without cgo, so Go's pure resolver is used;
  with no `/etc/resolv.conf` on Android it falls back to Google Public DNS
  (8.8.8.8). Networks that block direct UDP 53 break lookups.
- **Decoders:** the pure-Go decoders (FT8, WSPR, APRS, POCSAG, NAVTEX,
  SSTV, APT, DTMF, EAS/SAME...) all work. The external reference decoders
  (WSJT-X jt9/wsprd, multimon-ng, minimodem) are x86/Linux tools and are
  **not** included on Android; FT8/WSPR recall is lower without them.
- **Login:** the existing web login protects the UI. On first run the app
  generates a random password (menu -> "Show UI password"); set your own
  with "Set UI password".
- **Licence:** sdrstream is AGPL-3.0. The app menu links the corresponding
  source (https://github.com/Chaim12345/sdrstream) - that is the in-app
  source offer. If you distribute a modified APK you must publish your
  corresponding source under AGPL-3.0.

## CI

`.github/workflows/android.yml` builds the engine + APK on every push to
android-affecting paths and uploads the APK as an artifact.
