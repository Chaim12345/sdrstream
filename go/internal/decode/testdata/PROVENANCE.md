# Decoder fixture provenance

## wefax_offair_klankbeeld_733401.wav (real off-air WEFAX)

Independent off-air recording of a real marine radiofax broadcast, licensed
for reuse with attribution.

- **Source page**: https://freesound.org/people/klankbeeld/sounds/733401/
- **Title**: "Fax over radio map 8038 MHz recorded April 24 2024 1424 UTC"
- **Recorder / attribution**: klankbeeld (freesound user)
- **License**: Creative Commons Attribution 4.0 (CC BY 4.0).
  The source page shows the CC-BY badge and links
  "Go to the full license text" -> https://creativecommons.org/licenses/by/4.0/
  (verified in page HTML on 2026-10-24).
- **Station** (from the recorder's description on the source page):
  "Radio Station: Northwood, GYA UK - Marine Radiofax Broadcast UK 8038 MHz
  recorded April 24 2024 14:24 UTC" (the recorder's "MHz" is a typo for kHz;
  GYA/Northwood broadcasts marine radiofax on 8038 kHz USB).
- **What it is**: ~10 min 47 s off-air HF recording of the GYA Northwood
  weather-fax broadcast, including the chart region. The recorder's
  description links the decoded chart image:
  https://upload.wikimedia.org/wikipedia/commons/thumb/7/70/Weerkaart_24_april_2024.png/800px-Weerkaart_24_april_2024.png
- **Derivation (changes, per CC BY 4.0 §3(a)(1)(B))**: the original wav
  download on freesound requires a freesound account, so this fixture is
  derived from the sound's public high-quality preview MP3
  (https://cdn.freesound.org/previews/733/733401_1648170-hq.mp3, 61 kbps,
  12 kHz mono). It was trimmed to the 280 s-430 s window (150 s of solid
  chart content) and resampled to 8 kHz mono 16-bit PCM with ffmpeg.
  The MP3 transcode step is a fidelity caveat: this is genuine atmospheric
  off-air audio, but one lossy generation removed from the recorder's
  original.
- **Decode check** (repo decoder, 120 lpm / IOC 576): 1810x298 image, sync
  via line-correlation (the recording starts mid-chart, no phasing block),
  drift 69 ppm. The decoded image shows a synoptic chart region with a
  marked "994" low, isobars, frontal bands and coordinate text.

License obligations satisfied by this file + the manifest entry: attribution
(klankbeeld), source URL, license URL, and indication of changes.

## ft8/ corpus (FT8/JT9 reference fixtures)

License check result for the FT8/JT9 corpus (2026-10-24):

- **wsjt-x sample files (candidate, not used)**: WSJT-X is GPLv3
  (https://wsjt.sourceforge.io/); its sample wavs inherit that license.
  Not needed: the repo already carries the ft8_lib corpus, which is MIT.
- **ft8_lib test corpus (used)**: https://github.com/kgoba/ft8_lib,
  repo-wide MIT license (see ft8_lib.LICENSE). The 191111_* wavs and their
  .txt WSJT-X decode lists are byte-identical to upstream test/wav/
  (sha256-verified against raw.githubusercontent.com/kgoba/ft8_lib/master
  on 2026-10-24). The test_01..06 wav+txt pairs are the ft8_lib websdr
  test recordings from upstream test/wav/20m_busy/: BYTE-IDENTICAL to
  current upstream master (git blob sha1 match for all 12 files against
  github.com/kgoba/ft8_lib @ 9fec6ca, verified 2026-10-25). An earlier
  note claimed they matched only "an earlier upstream revision" and cited
  commit 8428a81 - that commit does not exist upstream; the mismatch was
  a comparison against the wrong upstream filenames (websdr_test*.wav
  instead of 20m_busy/test_0*), and the claim is corrected here.
- **live_websdr_utwente_7074kHz_20261022T052841Z.wav**: recorded by the
  sdrstream team from the public Twente WebSDR; own recording, no
  reference list (LDPC+CRC-14 validity gates it in ft8_test.go).

All ten wavs are sha256-pinned in manifest.json; the nine reference decode
lists are pinned by hash and message count. TestJT9DecodeFT8Corpus
(reference_test.go, skip-gated on the jt9 binary) requires jt9 to
reproduce every pinned reference message except three documented known
misses (manifest ref_known_misses).

Real-binary leg, RUN 2026-10-24 (previously UNVERIFIED): the corpus was
exercised with three installable CLI jt9 builds - wsjtx 2.2.2, 2.5.4 and
2.7.0, all at `jt9 -8 -d 3`. The 191111_* upstream pairs reproduce
exactly on all three. Three test_0x reference lines are not reproduced
by CLI jt9 2.2.2 or 2.5.4: test_01 'CQ RX3ASQ KO95' (-24 dB), test_04
'OK2BJ JG1SRO -15' (-24 dB), test_05 'R8JA CT3IQ RR73' (-10 dB). The
cause is unconfirmed: these are WSJT-X GUI decode lists, and the GUI's
a-priori (AP) passes and KVASD-assisted deep decode are not exposed by
the jt9 CLI (no AP flag; no kvasd binary in any deb; the kvasd download
from physics.princeton.edu refuses 403; KI7MT/kvasd-installer is an
interactive GUI installer), so a GUI-only decode class is the working
hypothesis - not proven, and weakened by two facts: test_02 carries the
identical text 'OK2BJ JG1SRO -15' at the same -24 dB and every CLI jt9
reproduces it there, and jt9 2.7.0 misses four further lines including
stronger-signal ones (test_01 'JA1FWS HA7CH JN97', test_05
'<9A9A> F6DEO/QRP' and 'CQ HA1BF JN86', test_06 'R7NO EA2DIC R-11')
while reproducing RX3ASQ - decoder output is version-sensitive at the
margins. The gate is therefore calibrated and valid for CLI jt9
2.2.2-2.5.4 behaviour; running it under 2.7.0 fails on those four
non-allowlisted lines (a known incompatibility, evidence in
manifest.json jt9_leg), and any jt9 upgrade must re-validate the
allowlist before adoption.
