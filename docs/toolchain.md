# Toolchain, CI and run scripts

## Why
The sandbox `go` shim unpacks a root-only archive and fails on `/opt/lazy/go.lock`.
`scripts/env.sh` installs an official Go release (checksum verified) into
`$HOME/toolchain` and puts it on `PATH`. No root needed. Override with
`SDR_TOOLCHAIN`, `GO_VERSION`, `GO_SHA256`. `go.mod` needs 1.23.4 or newer;
tested with 1.27.3.

## CI check
```bash
scripts/ci.sh
```
Runs gofmt, `go vet`, `go build`, `go test` on `go/`. Exit code is non-zero on
vet, build or test failure. gofmt only warns unless `STRICT_FMT=1`, because
some base files are not gofmt-clean yet (hfdecode, wefax_test, directory,
dvbs2, military, satellite, tracking, websdr). Run `gofmt -w` on them in one
dedicated commit, then use `STRICT_FMT=1`.

## Run
```bash
export SDRSTREAM_UI_PASSWORD='...'      # required, read from env only
scripts/run.sh start                    # build, nohup app, cloudflared quick tunnel, health check
scripts/run.sh status
scripts/run.sh stop                     # stops only processes it started (pid files)
```
Env: `PORT` (8000), `RUN_DIR` (`~/sdrstream-run`, holds logs, pid files, binary,
captures), `EXTRA_ARGS` (extra `sdrstream web` flags, for example
`--scan-stations ...`), `CLOUDFLARED` (default `$SDR_TOOLCHAIN/cloudflared`),
`SDRSTREAM_STATE_DIR` (default `~/sdrstream-state`, holds auth sessions and scan
state across restarts; keep it on a persistent home/volume).
Optional: `SDRSTREAM_AIS_KEY`, `SDRSTREAM_WHISPER`, `SDRSTREAM_WHISPER_MODEL`.

The app binds 127.0.0.1 only; the tunnel is the public path, and it prints the
`trycloudflare.com` URL. Health check: `/login` returns 200 and
unauthenticated `/api/status.json` returns 401. The tunnel check retries up to
about 60 s because new hostnames take a few seconds to resolve.

Install cloudflared (linux-amd64):
```bash
curl -fsSL -o ~/toolchain/cloudflared https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64 && chmod +x ~/toolchain/cloudflared
```
Quick tunnel URLs change on every start.
