# Installing sdrstream

sdrstream is one static Go binary with a web UI. Pick the install that
matches where it will run:

| Where | How |
|---|---|
| Linux server / VPS (keeps running) | `scripts/install.sh` (systemd) |
| macOS | `scripts/install.sh` (launchd) |
| Windows | `scripts/install.ps1` (Scheduled Task) |
| Docker / any host with Compose | `docker compose up -d` |
| Android phone (full server on the phone) | `android/` - builds an APK that runs the same binary in a foreground service |
| Any browser / phone home screen | the web UI is an installable PWA (see below) |

## One-command install (Linux / macOS)

```bash
curl -fsSL https://raw.githubusercontent.com/Chaim12345/sdrstream/master/scripts/install.sh | bash
```

The script installs the binary (GitHub release when one is published,
otherwise builds from source with a private Go toolchain under
`~/toolchain`), asks for a web-UI password once without echoing it (or
generates a random one), stores it mode-600 in the config file - it is
never printed - and registers a systemd service (Linux) or launchd agent
(macOS). Then open http://127.0.0.1:8000 and log in.

Useful knobs (env): `SDRSTREAM_PORT`, `SDRSTREAM_PREFIX`,
`SDRSTREAM_NO_SERVICE=1`, `SDRSTREAM_WITH_REFDEC=1`.

The password lives in `/etc/sdrstream/sdrstream.env` (Linux) or
`~/.config/sdrstream/sdrstream.env` (macOS). Read it once with
`sudo grep UI_PASSWORD /etc/sdrstream/sdrstream.env`.

## Windows

```powershell
irm https://raw.githubusercontent.com/Chaim12345/sdrstream/master/scripts/install.ps1 | iex
```

Installs the static `.exe`, asks for the password without echoing, stores
it in a user-only-ACL file under `%LOCALAPPDATA%\sdrstream`, and registers
a Scheduled Task that starts the monitor at logon.

## Docker

```bash
echo 'SDRSTREAM_UI_PASSWORD=choose-something-long' > .env   # mode 600
docker compose up -d
```

State persists in the `sdrstream-data` volume. The image ships the pure-Go
decoders plus multimon-ng and minimodem.

## Reference decoders (jt9 / wsprd)

sdrstream's Go decoders work out of the box. For reference-grade FT8/WSPR,
`scripts/refdec.sh` installs unmodified WSJT-X `jt9`/`wsprd` rootlessly
(apt download + dpkg-deb extract into `~/toolchain/refdec`) and exports
`SDRSTREAM_REFDEC_DIR`; `SDRSTREAM_WITH_REFDEC=1 scripts/install.sh` wires
it into the service environment. They always run as **separate processes**
exchanging WAV files - mere aggregation - so their GPL terms do not extend
to sdrstream and sdrstream's AGPL does not extend to them. The engine that
uses them lands with the external-decoder branch; on builds without it the
variable is simply inert and the Go decoders are used.

## The web UI as an app (PWA)

The UI is an installable Progressive Web App: open it in Chrome/Edge/
Safari and choose "Install app" / "Add to Home Screen". It then runs in its
own window with an icon. Live data (`/api/*`, `/audio/*`) always comes from
the network; only the static shell is cached for offline launch.

## Android (full stack on the phone)

See `android/README.md`. The APK embeds the same `sdrstream` binary
(cross-compiled `GOOS=android`) and runs it in a foreground service; the
UI is the same web UI in a WebView. Source: this repo (AGPL-3.0), linked
in the app.

## After install

- Health check: `curl http://127.0.0.1:8000/healthz`
- Logs: `journalctl -u sdrstream -f` (Linux), `~/Library/Logs/sdrstream/`
  (macOS), Task Scheduler + `%LOCALAPPDATA%\sdrstream` (Windows),
  `docker compose logs -f` (Docker).
- Optional keys (never in flags, env only): `SDRSTREAM_NIM_API_KEY`
  (hosted speech-to-text), `SDRSTREAM_AIS_KEY` (ship positions).
