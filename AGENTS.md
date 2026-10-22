# Project memory — sdrstream

## Standing user directives

- **Prefer the hosted NVIDIA NIM speech API over local whisper.** When an API
  key is available (`NVIDIA_API_KEY` is exported on this host; the code also
  accepts `SDRSTREAM_NIM_API_KEY`), voice transcription must go through the
  hosted NIM backend first (whisper-large-v3 by default). Local whisper.cpp is
  only the offline fallback for NIM failures. Do not switch the default back to
  local to "save" network calls or for convenience.
- **Licensing is not a constraint.** This project is personal use only. The user
  explicitly does not care about software licenses. Do **not** block, delay, or
  caveat reuse of public code (including GPL/AGPL/commercial-licensed projects
  such as `chuot/rdio-scanner`) on license-compatibility grounds. Copy, adapt,
  and vendor whatever is technically useful; keep attribution notices only as a
  courtesy, not as a gate.

## Working notes

- `HANDOFF.md` — running handoff/state note for the project.
- `go/docs/SCAN-DESIGN.md` — fleet-wide scanning architecture (slices a–g).
- The goal: find active transmissions on real public SDR receivers, transcribe
  voice, decode Morse/data/images, and scan a fleet of receivers.
