#!/usr/bin/env python3
"""Generate SSTV test fixtures with the independent pysstv encoder.

The Go decoder under test must not share constants with its fixtures, so
these WAVs come from pysstv (https://github.com/dnet/pysstv, GPLv3), a
separately maintained encoder, plus numpy-based clock-offset and AWGN
channel impairments. See tools/vendor/decoder-notices/pysstv.copyright.

Fixtures written to go/internal/decode/testdata/ (8000 Hz, 16-bit mono):
  sstv_martin_m1.wav            Martin M1, clean
  sstv_martin_m2.wav            Martin M2, clean
  sstv_martin_m2_impaired.wav   Martin M2, +300 ppm clock offset, ~15 dB SNR
  sstv_scottie_s1.wav           Scottie S1, ~20 dB SNR
  sstv_robot36.wav              Robot 36, ~20 dB SNR
  sstv_robot36_impaired.wav     Robot 36, -250 ppm clock offset, ~15 dB SNR
  pattern_320x256.png / pattern_320x240.png  the encoded source images
and tests/data/ref_sstv.wav (22050 Hz: Martin M2 header + a few lines, for
the VIS reference test).
"""
import math
import os
import struct
import sys

import numpy as np
from PIL import Image, ImageDraw

sys.path.insert(0, os.path.dirname(__file__))

from pysstv.color import MartinM1, MartinM2, ScottieS1, Robot36

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
TESTDATA = os.path.join(ROOT, "go", "internal", "decode", "testdata")
RATE_IMAGE = 8000
RATE_REF = 22050


class MartinM2Wide(MartinM2):
    """Martin M2 is 320x256 per the G3OQD spec (same as M1, half scan time).

    pysstv ships MartinM2 with WIDTH=160; the pixel raster width does not
    change the RF signal (73.216 ms per channel either way), but the 320 px
    raster matches QSSTV/MMSSTV and the mode spec.
    """

    WIDTH = 320


def test_pattern(width, height):
    """Deterministic pattern: colour bars on top half, luminance ramp with a
    white square on the bottom half."""
    img = Image.new("RGB", (width, height))
    d = ImageDraw.Draw(img)
    bars = [(255, 0, 0), (0, 255, 0), (0, 0, 255), (255, 255, 255), (0, 0, 0)]
    bw = width / len(bars)
    for i, rgb in enumerate(bars):
        d.rectangle([round(i * bw), 0, round((i + 1) * bw) - 1, height // 2 - 1], fill=rgb)
    for x in range(width):
        v = round(255 * x / (width - 1))
        d.line([(x, height // 2), (x, height - 1)], fill=(v, v, v))
    x0, x1 = round(width * 0.44), round(width * 0.56)
    y0, y1 = round(height * 0.70), round(height * 0.86)
    d.rectangle([x0, y0, x1, y1], fill=(255, 255, 255))
    return img


def encode(cls, image, rate):
    enc = cls(image, rate, 16)
    samples = np.fromiter((v / 32768.0 for v in enc.gen_samples()), dtype=np.float64)
    return samples


def add_clock_offset(samples, ppm):
    """Resample to mimic a receiver clock off by ppm parts per million."""
    r = 1.0 + ppm * 1e-6
    n_out = int(len(samples) / r)
    idx = np.arange(n_out) * r
    return np.interp(idx, np.arange(len(samples)), samples)


def add_noise(samples, snr_db, seed):
    rng = np.random.default_rng(seed)
    sig_rms = float(np.sqrt(np.mean(samples**2)))
    noise_rms = sig_rms / (10.0 ** (snr_db / 20.0))
    return samples + rng.normal(0.0, noise_rms, len(samples))


def write_wav(path, samples, rate):
    samples = np.clip(samples, -1.0, 1.0)
    pcm = (samples * 32767.0).astype("<i2")
    with open(path, "wb") as f:
        f.write(b"RIFF")
        f.write(struct.pack("<I", 36 + len(pcm) * 2))
        f.write(b"WAVE")
        f.write(b"fmt ")
        f.write(struct.pack("<IHHIIHH", 16, 1, 1, rate, rate * 2, 2, 16))
        f.write(b"data")
        f.write(struct.pack("<I", len(pcm) * 2))
        f.write(pcm.tobytes())
    print(f"wrote {path} ({len(samples)/rate:.1f}s @ {rate} Hz)")


def vis_only(vis, rate, trailing_lines_cls=None, trailing_lines=0, image=None):
    """Martin M2 VIS header followed by a few image lines (for ref_sstv.wav)."""
    enc = MartinM2Wide(image, rate, 16)
    freq_bits = list(enc.gen_freq_bits())
    # leader1, break, leader2, start, 7 data, parity, stop = 12 tuples;
    # each Martin line = sync + (gap + 320 px + gap) + (320 px + gap) +
    # (320 px + gap) = 966 tuples.
    keep = 12 + trailing_lines * 966
    spms = rate / 1000
    out = []
    phase = 0.0
    for freq, msec in freq_bits[:keep]:
        n = int(spms * msec)
        t = np.arange(n)
        out.append(np.sin(2 * np.pi * freq * t / rate + phase))
        phase = (phase + 2 * np.pi * freq * n / rate) % (2 * np.pi)
    return np.concatenate(out)


def main():
    os.makedirs(TESTDATA, exist_ok=True)
    img256 = test_pattern(320, 256)
    img240 = test_pattern(320, 240)
    img256.save(os.path.join(TESTDATA, "pattern_320x256.png"))
    img240.save(os.path.join(TESTDATA, "pattern_320x240.png"))

    write_wav(os.path.join(TESTDATA, "sstv_martin_m1.wav"),
              encode(MartinM1, img256, RATE_IMAGE), RATE_IMAGE)

    m2 = encode(MartinM2Wide, img256, RATE_IMAGE)
    write_wav(os.path.join(TESTDATA, "sstv_martin_m2.wav"), m2, RATE_IMAGE)
    impaired = add_noise(add_clock_offset(m2, 300), 15.0, seed=36)
    write_wav(os.path.join(TESTDATA, "sstv_martin_m2_impaired.wav"), impaired, RATE_IMAGE)

    s1 = encode(ScottieS1, img256, RATE_IMAGE)
    write_wav(os.path.join(TESTDATA, "sstv_scottie_s1.wav"),
              add_noise(s1, 20.0, seed=41), RATE_IMAGE)

    r36 = encode(Robot36, img240, RATE_IMAGE)
    write_wav(os.path.join(TESTDATA, "sstv_robot36.wav"),
              add_noise(r36, 20.0, seed=72), RATE_IMAGE)
    impaired36 = add_noise(add_clock_offset(r36, -250), 15.0, seed=73)
    write_wav(os.path.join(TESTDATA, "sstv_robot36_impaired.wav"), impaired36, RATE_IMAGE)

    ref = add_noise(vis_only(0x28, RATE_REF, trailing_lines=6, image=img256), 30.0, seed=8)
    write_wav(os.path.join(ROOT, "tests", "data", "ref_sstv.wav"), ref, RATE_REF)


if __name__ == "__main__":
    main()
