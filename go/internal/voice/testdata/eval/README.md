# ASR eval corpus harness

`manifest.json` pins a 28-utterance speech corpus (LibriSpeech dev-clean,
CC BY 4.0 - attribution in the manifest) and two whisper.cpp GGML models by
sha256, plus the WER numbers measured with them on the harness branch tip.

- Fetch/verify the corpus: `scripts/fetch-asr-eval-corpus.sh [dir]`, then
  `export SDRSTREAM_ASR_EVAL_DIR=<dir>`.
- Models: `SDRSTREAM_WHISPER_MODEL` / `SDRSTREAM_WHISPER_FALLBACK_MODEL`
  must point at the pinned `ggml-tiny.en.bin` / `ggml-base.en.bin`
  (hashes checked; mismatch fails, missing pieces skip).
- Run: `go test ./internal/voice -run TestASREvalCorpus -v` (~6 min CPU).
  Without the env above the tests SKIP.
- "radio" conditions are a SIMULATED channel (300-2800 Hz band-pass + white
  noise + noise gaps, fixed seed), never a real radio recording.
- Gate margins are absolute percentage points declared at manifest creation.
  A failing gate is a finding to report; the gate is never tuned to pass.
