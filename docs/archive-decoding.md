# Decoding public archived recordings

Bench run 2026-10-22, branch `feature/archive-decoding`. Goal: take real
publicly archived radio recordings, run our decoders (and hosted Whisper
ASR) over them, compare with ground truth where it exists, and tabulate
what actually decodes. Every entry names its source; "verified" means
the output was checked against independent ground truth, not that text
merely appeared.

## Sources and permission

All audio was fetched from the Internet Archive (archive.org), whose
public-download terms allow automated access at polite rates. Files were
downloaded once each, sequentially, with a pause between requests. No
authentication, no paywalled content, no rate-limit pressure. Audio
files are not committed to this repo; only decoder outputs are.

| Source | Item | Notes |
| --- | --- | --- |
| The Conet Project (Irdial) | https://archive.org/details/ird059 | Numbers-station recordings + booklet PDF with transcripts (ground truth) |
| Various EAS Captures | https://archive.org/details/msx-eas | SAME header captures with the uploader's decoder logs as ground truth |
| NOAA Weather Radio aircheck KEC74 | https://archive.org/details/noaa-2-10-15-aircheck-indianapolis | 2015 NWR broadcast |
| WeFax IOC576 Demo | https://archive.org/details/wefax-ioc576-demo | Classic decoder test file |
| vision (SSTV) | https://archive.org/details/vision-st3-sstv | SSTV audio + reference image |
| VARC CW practice night | https://archive.org/details/14G0OIK | Club Morse practice, operators named in description |

Broadcastify was considered and dropped: its archives require a paid
premium account, so it fails the "publicly available, ToS-allowed" bar.
SatNOGS archived audio is covered by the satellite workstream and was
not duplicated here.

## Results

| Recording | Decoder | Result | Verification |
| --- | --- | --- | --- |
| Conet D1 Lincolnshire Poacher (E3 voice, en) | NIM whisper-large-v3 | Full 5-figure groups read out | VERIFIED: first groups 66475 19274 92028 78494 match the Conet booklet transcript exactly |
| Conet D4 Spanish Counting (V5A, es) | NIM whisper-large-v3 (es) | "1, 2, 3, 4, 5, 6, 7, 8, 9..." count | PARTIAL: consistent with a counting station, no per-digit ground truth |
| Conet D3 Hier ist DFC Sieben und Dreizig (G14, de) | NIM whisper-large-v3 (de) | "Hier ist BFC 37..." + figure groups | PARTIAL: station ID heard as BFC/WFC vs documented DFC37 (noisy SW), format correct |
| NOAA Weather Radio KEC74 aircheck | NIM whisper-large-v3 (en) | Full forecast transcript | VERIFIED against item metadata (station, city list, February 2015 cold-snap content) |
| VARC CW practice (01-M0HCL) | Morse | "CQ CQ CQ DE M 0HCL M0HCL K" | VERIFIED: operator is M0HCL per item description; one spurious space in the first callsign copy |
| vision-st3-sstv (SSTV audio) | SSTV detect + image | Scottie S1 VIS 0x3C at 2.1 s, 320x256 PNG | VERIFIED visually against the item's reference image (archivebench/sstv-vision-decoded.png vs -reference.png) |
| WeFax IOC576 Demo (661 s) | WEFAX image | Tones locked 1110/1890 Hz, phasing sync (60 lines), 901-line image | PARTIAL: real FSK with phasing locked, but ~+1000 ppm recorder clock drift leaves the chart slanted and unreadable. This file uses a non-standard 1200/1800 Hz tone pair; the lock window was widened for it (below) |
| msx-eas who-rmt + 3 more SAME captures | EAS/SAME | No SAME headers | NOT DECODED: bursts visible in the spectrogram (archivebench/eas-who-rmt-spectrogram.png) but the 16-32 kbps MP3 + recorder noise reduction corrupts the bit stream (best preamble score 0.73 vs the 0.90 gate; bit extraction yields garbage). Uploader's own logs are the ground truth that a better demod should reach |
| Conet D4 M1 / M1b (MCW numbers) | Morse | No verifiable text | NOT DECODED: heavy fading and noise; fragments only. Honest gap - off-air numbers-station Morse needs fading-tolerant envelope tracking |

## Decoder changes made for this bench

* `internal/decode/morse.go`
  * Character/word gap thresholds tightened from the textbook 1/3/7-dot
    ratios to 2/4-dot boundaries. Real captures lose gap length to
    envelope rise/fall bleed, so 3-dot character gaps were landing in a
    dead zone and characters merged into garbage. Recovers the M0HCL
    decode above; `TestMorseGapBleed` reproduces the capture timing.
  * Two-tone MCW fallback: numbers-station Morse (M1 family) alternates
    keying between two carriers, so a single-tone envelope sees every
    other element. When the single-tone read is implausible, the decoder
    now combines envelopes from the two strongest tones and retries with
    a 2.5 kHz tone ceiling. Covered by `TestMorseTwoTone`.
* `internal/decode/wefax.go`
  * Tone-pair lock windows widened asymmetrically (black 1500-450 Hz,
    white 2300-650 Hz) so amateur/demo captures with a shifted
    1200/1800 Hz pair lock. Standard 1500/2300 Hz signals lock exactly
    as before (strongest peak in window wins), and the prominence,
    anti-correlation and phasing checks still gate false locks. Covered
    by `TestWEFAXLockStandardTones` and `TestWEFAXLockShiftedTones`.

## Reproduce

```bash
# fetch (once, politely) from the URLs above, then:
ffmpeg -i <file>.mp3 -ac 1 -ar 8000  -sample_fmt s16 <file>.wav   # data modes
ffmpeg -i <file>.mp3 -ac 1 -ar 16000 -sample_fmt s16 <file>.wav   # ASR
go run ./cmd/sdrstream decode -mode morse <file>.wav
go run ./cmd/sdrstream decode -mode eas <file>.wav
go run ./cmd/sdrstream decode -mode sstv -sstv-image out.png <file>.wav
go run ./cmd/hfdecode -kind wefax <file>.wav
SDRSTREAM_NIM_API_KEY=... go run ./cmd/nimasr -model whisper-large-v3 -lang en <file>.wav
```

Status labels: VERIFIED = matches independent ground truth; PARTIAL =
decoder worked but only part of the output is checkable or readable;
NOT DECODED = honest failure, cause investigated and recorded above.
