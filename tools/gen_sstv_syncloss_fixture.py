#!/usr/bin/env python3
"""Generate a synthetic SSTV geometry-failure fixture.

Post-processes the clean pysstv fixture internal/decode/testdata/
sstv_martin_m1.wav with two impairments over a 100-line band
(lines 80-179):

1. Sync erasure: each line's 1200 Hz sync pulse is replaced with
   white noise, so the decoder cannot lock those lines and coasts.
2. Clock stretch: the band audio is resampled 1.2% longer, so the
   true line clock drifts away from the constant-clock assumption
   while the syncs are gone. Post-band syncs reappear ~536 ms late,
   far outside the +/-12 ms search window, so the decoder keeps
   coasting to end of frame.

A clean VIS header, full-length audio and in-bounds channel sampling
remain, so the frame passes every pre-gate decode check (VIS + parity,
80% line-presence floor) while its geometry is torn: the top 80 lines
are correct, everything below is smeared. Negative fixture for the
SSTV coast-fraction geometry gate.

Synthetic; derived from the pysstv-generated sstv_martin_m1.wav.
No off-air material.
"""

import wave

import numpy as np

SRC = "go/internal/decode/testdata/sstv_martin_m1.wav"
DST = "go/internal/decode/testdata/sstv_martin_m1_syncloss_drift.wav"
BAND_FIRST, BAND_LAST = 80, 180  # erase/drift lines [80, 180)
STRETCH = 1.012


def read_wav(path):
    with wave.open(path) as w:
        rate = w.getframerate()
        n = w.getnframes()
        samples = np.frombuffer(w.readframes(n), dtype="<i2").astype(np.float64) / 32768.0
    return samples, rate


def write_wav(path, samples, rate):
    pcm = np.clip(samples * 32768.0, -32768, 32767).astype("<i2")
    with wave.open(path, "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(rate)
        w.writeframes(pcm.tobytes())


def find_sync_grid(samples, rate):
    """Locate the regular ~446 ms Martin M1 sync grid by 1200 Hz purity."""
    win = int(0.004 * rate)
    hop = int(0.002 * rate)
    n = np.arange(win)
    cw = np.cos(2 * np.pi * 1200 * n / rate)
    sw = np.sin(2 * np.pi * 1200 * n / rate)
    times = []
    for i in range((len(samples) - win) // hop):
        seg = samples[i * hop:i * hop + win]
        power = float(seg @ seg)
        if power <= 1e-9:
            continue
        re = float(seg @ cw)
        im = float(seg @ sw)
        if 2 * (re * re + im * im) / win / power > 0.4:
            t = i * hop / rate
            if t > 1.0 and (not times or t - times[-1] > 0.010):
                times.append(t)
    kept = []
    for t in times:
        if not kept or 0.43 < t - kept[-1] < 0.47:
            kept.append(t)
    if len(kept) < 200:
        raise SystemExit(f"sync grid too short: {len(kept)} clusters")
    return kept


def main():
    samples, rate = read_wav(SRC)
    grid = find_sync_grid(samples, rate)
    if len(grid) < BAND_LAST:
        raise SystemExit(f"grid has {len(grid)} syncs, need {BAND_LAST}")
    rng = np.random.default_rng(20261023)
    rms = float(np.sqrt(np.mean(samples * samples)))
    out = samples.copy()
    for y in range(BAND_FIRST, BAND_LAST):
        # The grid marks the first hot 4 ms hop; the 4.862 ms pulse
        # spans roughly [grid, grid+8 ms]. Erase with margin or the
        # pulse tail still locks the sample-precise refiner.
        s0 = int(grid[y] * rate) - int(0.002 * rate)
        s1 = s0 + int(0.013 * rate)
        out[s0:s1] = rng.normal(0.0, 0.8 * rms, s1 - s0)
    band_start = int(grid[BAND_FIRST] * rate)
    band_end = int(grid[BAND_LAST] * rate)
    band = out[band_start:band_end]
    x_old = np.arange(len(band))
    x_new = np.linspace(0, len(band) - 1, int(len(band) * STRETCH))
    band_stretched = np.interp(x_new, x_old, band)
    result = np.concatenate([out[:band_start], band_stretched, out[band_end:]])
    write_wav(DST, result, rate)
    print(f"wrote {DST}: {len(result)/rate:.2f}s (band stretched x{STRETCH}, "
          f"syncs erased on lines {BAND_FIRST}-{BAND_LAST-1})")


if __name__ == "__main__":
    main()
