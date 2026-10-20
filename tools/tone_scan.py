#!/usr/bin/env python3
"""Report the strongest tones in a WAV capture, per time slice.

Usage: python3 tools/tone_scan.py CAPTURE.wav [LO_HZ] [--band LOW HIGH]

Prints, every minute of audio, the strongest bins inside a band so a
beacon can be told from receiver spurs: a real satellite tone drifts
with Doppler between retunes, a spur sits at a fixed frequency.
"""
import argparse
import math
import struct
import sys
import wave


def fft_re(x):
    n = len(x)
    if n == 1:
        return x[:]
    even = fft_re(x[0::2])
    odd = fft_re(x[1::2])
    out = [0j] * n
    for k in range(n // 2):
        t = math.e ** (-2j * math.pi * k / n) * odd[k]
        out[k] = even[k] + t
        out[k + n // 2] = even[k] - t
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("wav")
    ap.add_argument("--band", type=float, nargs=2, default=[300.0, 1500.0],
                    help="audio band to scan, default 300 1500 Hz")
    ap.add_argument("--interval", type=float, default=60.0,
                    help="seconds between slices")
    args = ap.parse_args()

    with wave.open(args.wav) as w:
        rate, n = w.getframerate(), w.getnframes()
        print(f"{args.wav}: {n} samples @ {rate} Hz = {n / rate:.1f} s")
        N = 8192
        step = int(rate * args.interval)
        for pos in range(0, max(1, n - N), step):
            w.setpos(pos)
            frames = w.readframes(min(rate * 3, n - pos))
            samples = struct.unpack("<%dh" % (len(frames) // 2), frames)
            acc = [0.0] * (N // 2)
            count = 0
            for f in range(len(samples) // N):
                x = [complex(s / 32768.0, 0) for s in samples[f * N:(f + 1) * N]]
                for k, v in enumerate(fft_re(x)[:N // 2]):
                    acc[k] += abs(v) ** 2
                count += 1
            if not count:
                continue
            avg = [a / count for a in acc]
            lo = max(1, int(args.band[0] / (rate / N)))
            hi = min(N // 2, int(args.band[1] / (rate / N)))
            ranked = sorted(range(lo, hi), key=lambda k: -avg[k])[:3]
            noise = sorted(avg[k] for k in range(lo, hi))[len(range(lo, hi)) // 2]
            tones = ", ".join(f"{k * rate / N:7.1f}Hz {10 * math.log10(avg[k]):4.1f}dB" for k in ranked)
            print(f"t={pos / rate:5.0f}s  noise {10 * math.log10(noise):5.1f}dB  {tones}")


if __name__ == "__main__":
    sys.exit(main())
