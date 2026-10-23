#!/usr/bin/env bash
# Fetch and verify the ASR eval corpus pinned by
# go/internal/voice/testdata/eval/manifest.json.
#
# Source: LibriSpeech dev-clean, CC BY 4.0 (https://www.openslr.org/12).
# The 28-utterance selection is extracted locally; every wav/txt is then
# verified against the manifest's sha256 pins. Nothing is uploaded anywhere.
#
# Usage: scripts/fetch-asr-eval-corpus.sh [target-dir]
#   default target: ~/.cache/sdrstream/asr-eval
#   then:           export SDRSTREAM_ASR_EVAL_DIR=<target-dir>
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
manifest="$here/../go/internal/voice/testdata/eval/manifest.json"
target="${1:-$HOME/.cache/sdrstream/asr-eval}"
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
mkdir -p "$target"
echo "downloading LibriSpeech dev-clean (CC BY 4.0, openslr.org/12) ..."
curl -fsSL -o "$tmp/dev-clean.tar.gz" https://www.openslr.org/resources/12/dev-clean.tar.gz
tar -xzf "$tmp/dev-clean.tar.gz" -C "$tmp"
python3 - "$manifest" "$tmp/LibriSpeech/dev-clean" "$target" <<'PY'
import json, subprocess, sys, os, hashlib
manifest, src, target = sys.argv[1:4]
m = json.load(open(manifest))
def sha(p):
    h = hashlib.sha256()
    with open(p, 'rb') as f:
        for b in iter(lambda: f.read(1 << 20), b''):
            h.update(b)
    return h.hexdigest()
for entry in m["files"]:
    name = entry["name"]
    speaker, chapter, _ = name.split("-")
    flac = os.path.join(src, speaker, chapter, name + ".flac")
    trans = os.path.join(src, speaker, chapter, f"{speaker}-{chapter}.trans.txt")
    wav = os.path.join(target, name + ".wav")
    txt = os.path.join(target, name + ".txt")
    subprocess.run(["ffmpeg", "-loglevel", "error", "-y", "-i", flac, "-ar", "16000", "-ac", "1", wav], check=True)
    line = [l for l in open(trans) if l.startswith(name + " ")][0]
    open(txt, "w").write(line[len(name) + 1:])
    assert sha(wav) == entry["wav_sha256"], f"{name}.wav hash mismatch after conversion"
    assert sha(txt) == entry["txt_sha256"], f"{name}.txt hash mismatch"
    print("verified", name)
print(f"corpus ready at {target}; export SDRSTREAM_ASR_EVAL_DIR={target}")
PY
