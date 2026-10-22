# Transcription quality

`voice.TranscribeASR` = energy VAD with hysteresis and hangover -> drop silence -> chunk at pauses (<=25 s) -> whisper.cpp `-ojf` per chunk -> confidence (mean token probability) and language from whisper's JSON -> fallback to the next larger local GGML model when confidence < 0.8 (`SDRSTREAM_WHISPER_MIN_CONF`, `SDRSTREAM_WHISPER_FALLBACK_MODEL`) -> hallucination filter (filler on noise, repetition loops). Activities carry `confidence`, `language`, `asr_model`, `asr_fallback`; the web feed shows a colored chip.
Language is only identified with a multilingual model (`ggml-base.bin`, not `.en`).

## Evaluation (`go run ./cmd/asreval -dir <wav+txt folder>`)
Data: LibriSpeech dev-clean, real human read speech with true transcripts (14 utterances, 298 words). Models: tiny.en primary, base.en fallback, CPU.
"radio" = the same real speech through a SIMULATED channel (300-2800 Hz band-pass, white noise, noise-only lead-in/tail). Not a real radio recording.

| condition | baseline WER | new WER |
|---|---|---|
| clean | 9.1% | 8.7% |
| simulated radio 8 dB SNR, 3 s gaps | 26.5% | 19.8% |
| simulated radio 12 dB SNR, 15 s gaps (8 files, 120 words) | 40.0% | 25.8% |

At the old 0.6 fallback threshold the fallback never fired and WER matched the baseline (26.5% vs 25.5%), so the gain comes from the fallback and silence removal, and the threshold was raised to 0.8. Mean confidence of files with WER<=10%: 0.89, files above: 0.81 (a weak but real signal). Small sample; not yet validated on real recorded radio audio.
