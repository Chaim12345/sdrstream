# Feeding Rdio Scanner from sdrstream

[Rdio Scanner](https://github.com/chuot/rdio-scanner) is a call archive with a
police-scanner-style web UI: live feed, playback, search, systems/talkgroups,
tags and user accounts. It does **not** tune receivers, detect signals or decode
anything — it ingests finished audio calls.

sdrstream is the other half: it tunes public SDR receivers, finds transmissions,
decodes data/CW/SSTV and transcribes voice. The natural composition is to keep
Rdio Scanner as the archive/UI and have sdrstream upload its verified captures
through Rdio's `POST /api/call-upload` API. That is what `internal/rdio` does.

## Run Rdio Scanner

```sh
docker run -d --name rdio-scanner -p 3000:3000 \
  -v rdio-data:/data chuot/rdio-scanner
```

Open `http://127.0.0.1:3000`, finish the admin setup, then create an API key
whose systems field is `*` (accept any system/talkgroup). Keys configured with
specific systems only accept calls for those numeric ids; sdrstream sends
labels, not ids, so `*` is the useful setting.

## Feed it from sdrstream

Single receiver:

```sh
export NVIDIA_API_KEY=...            # hosted ASR preferred; local whisper is fallback

go run ./cmd/sdrstream monitor \
  --server http://sdr.ladar.club:8073/ --kind openwebrx \
  --profile "D5 Man/Liv/Bpool Airports 118-120MHz" \
  --strategy spectrum --listen 8s \
  --rdio http://127.0.0.1:3000 --rdio-key "$RDIO_KEY" \
  --rdio-system "Manchester airband" \
  --out captures/rdio
```

Web UI + fleet scan:

```sh
go run ./cmd/sdrstream web \
  --monitor-server http://sdr.ladar.club:8073/ \
  --scan-open-directory \
  --rdio http://127.0.0.1:3000 --rdio-key "$RDIO_KEY" \
  --rdio-system sdrstream
```

Every verified live artifact that has an audio file is queued for upload by one
background worker. A slow or dead archive never blocks the detector; if the
queue fills, calls are dropped and counted rather than stalling the receiver.

## Field mapping

| sdrstream | Rdio Scanner |
|---|---|
| `activity.FrequencyHz` | `frequency` (Hz) and the `talkgroupLabel` (`119.8000 MHz voice`) |
| `activity.Kind` | `talkgroupGroup` (`voice`, `data`, `cw`, `sstv`) |
| `activity.Detail` (transcript or decoder output) | `talkgroupTag` (truncated to 160 chars) |
| `-rdio-system` / receiver URL | `systemLabel` |
| `activity.Started` | `timestamp` (Unix ms) |
| WAV written by `-out` / scanner captures | `audio` multipart file |

## What was reused, what was not copied

- **Reused by integration:** the `/api/call-upload` contract, the call metadata
  model (system/talkgroup/frequency/timestamp/audio), and the whole archive UI.
- **Not copied:** the 14k-line Go server (SQLite/MySQL/Postgres, JWT users,
  admin, dirwatch, ffmpeg transcoding) and the Angular client. sdrstream already
  has its own web UI and storage; duplicating that stack would add a second
  source of truth without adding SDR capability.
- **Possible later copy:** `server/dirwatch.go`'s metadata parsers
  (Trunk Recorder / DSDPlus / SDRTrunk) if sdrstream ever needs to ingest an
  existing recorder directory, and Rdio's search/filter query shapes for the
  sdrstream gallery.

License: sdrstream is AGPL-3.0 and Rdio Scanner is GPL-3.0; per the project's
standing directive (personal use, licensing is not a constraint) this is not a
gate. Attribution is kept in `NOTICE-third-party.md` as a courtesy.
