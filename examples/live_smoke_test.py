"""Live smoke test against public servers.

Run this to confirm the library still works against real infrastructure::

    python examples/live_smoke_test.py

It records a few seconds from OpenWebRX servers (PCM and ADPCM) and one WebSDR
server, writes WAV files to ``--outdir`` and prints basic signal statistics.
Requires network access and ``websockets``.
"""

from __future__ import annotations

import argparse
import asyncio
import os
import sys
from typing import List, Optional

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from sdrstream.common import write_wav  # noqa: E402
from sdrstream.openwebrx import OpenWebRXClient  # noqa: E402
from sdrstream.websdr import WebSDRClient  # noqa: E402

# (url, frequency in Hz, modulation)
OPENWEBRX_SERVERS = [
    ("wss://hasenberg01.openwebrx.ch/ws/", 26965000, "nfm"),
    ("wss://sdr2.justjakob.de/ws/", 7100000, "lsb"),
]

# (url, frequency in kHz, modulation)
WEBSDR_SERVERS = [
    ("http://websdr.ewi.utwente.nl:8901", 10000.0, "AM"),
]


def stats(samples: List[int]) -> str:
    if not samples:
        return "no samples"
    peak = max(abs(s) for s in samples)
    rms = (sum(s * s for s in samples) / len(samples)) ** 0.5
    crossings = sum(1 for a, b in zip(samples, samples[1:]) if (a < 0) != (b < 0))
    return "peak=%6d rms=%8.1f zero_crossings=%d" % (peak, rms, crossings)


async def probe_openwebrx(url: str, freq: int, mode: str, seconds: float,
                          outdir: str) -> Optional[int]:
    print("\nOpenWebRX %s" % url)
    try:
        async with OpenWebRXClient(url) as client:
            config = client.config
            print("  center=%s samp_rate=%s audio_compression=%s"
                  % (config.center_freq, config.samp_rate, config.audio_compression))
            await client.tune(frequency=freq, modulation=mode)
            chunks = await client.record(seconds)
            samples = OpenWebRXClient.concat(chunks)
            if not samples:
                print("  FAILED: no audio received")
                return None
            rate = chunks[0].samplerate
            name = "openwebrx_%s.wav" % (
                "adpcm" if config.audio_compression == "adpcm" else "pcm")
            path = os.path.join(outdir, name)
            write_wav(path, samples, rate)
            print("  %d samples @ %d Hz  %s" % (len(samples), rate, stats(samples)))
            print("  wrote %s" % path)
            return len(samples)
    except Exception as exc:
        print("  FAILED: %s: %s" % (type(exc).__name__, exc))
        return None


async def probe_websdr(url: str, freq: float, mode: str, seconds: float,
                       outdir: str) -> Optional[int]:
    print("\nWebSDR %s" % url)
    try:
        async with WebSDRClient(url) as client:
            await client.tune(frequency_khz=freq, mode=mode)
            chunks = await client.record(seconds)
            samples = WebSDRClient.concat(chunks)
            if not samples:
                print("  FAILED: no audio received")
                return None
            rate = chunks[0].samplerate
            path = os.path.join(outdir, "websdr.wav")
            write_wav(path, samples, rate)
            print("  %d samples @ %d Hz  %s" % (len(samples), rate, stats(samples)))
            print("  wrote %s" % path)
            return len(samples)
    except Exception as exc:
        print("  FAILED: %s: %s" % (type(exc).__name__, exc))
        return None


async def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--seconds", type=float, default=5.0)
    parser.add_argument("--outdir", default=".")
    args = parser.parse_args()
    os.makedirs(args.outdir, exist_ok=True)

    results = []
    for url, freq, mode in OPENWEBRX_SERVERS:
        results.append(await probe_openwebrx(url, freq, mode, args.seconds, args.outdir))
    for url, freq, mode in WEBSDR_SERVERS:
        results.append(await probe_websdr(url, freq, mode, args.seconds, args.outdir))

    ok = sum(1 for r in results if r)
    print("\n%d/%d servers produced audio" % (ok, len(results)))
    return 0 if ok else 1


if __name__ == "__main__":
    raise SystemExit(asyncio.run(main()))
