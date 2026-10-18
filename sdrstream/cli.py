"""Command-line interface for recording public SDR streams.

Examples
--------
Record 15 seconds of 40 m LSB from an OpenWebRX server::

    python -m sdrstream record --server wss://sdr.example.org/ws/ \
        --freq 7150000 --mode lsb --seconds 15 --out 40m.wav

Record from a WebSDR server (frequency in kHz)::

    python -m sdrstream record --kind websdr --server websdr.example.org:8901 \
        --freq 10000 --mode AM --seconds 10 --out wwv.wav

Inspect what a server offers without recording::

    python -m sdrstream info --server wss://sdr.example.org/ws/
"""

from __future__ import annotations

import argparse
import asyncio
import struct
import sys
from typing import List

from .common import write_wav
from .openwebrx import OpenWebRXClient
from .websdr import WebSDRClient


async def _record_openwebrx(args: argparse.Namespace) -> int:
    async with OpenWebRXClient(args.server, reconnect=not args.no_reconnect) as client:
        config = client.config
        print("server config: center=%s samp_rate=%s mod=%s audio_compression=%s"
              % (config.center_freq, config.samp_rate, config.start_mod,
                 config.audio_compression))
        await client.await_metadata(timeout=1.0)
        if client.modes:
            print("modes: " + ",".join(m.get("modulation", "?") for m in client.modes))
        await client.tune(
            frequency=int(args.freq) if args.freq else None,
            modulation=args.mode,
        )
        print("recording %.1fs ..." % args.seconds)
        chunks = await client.record(args.seconds)
        samples = OpenWebRXClient.concat(chunks)
        rate = chunks[0].samplerate if chunks else 12000
    return _finish(samples, rate, args)


async def _record_websdr(args: argparse.Namespace) -> int:
    async with WebSDRClient(args.server, reconnect=not args.no_reconnect) as client:
        await client.tune(
            frequency_khz=float(args.freq) if args.freq else None,
            mode=args.mode.upper(),
            bandwidth_hz=args.bandwidth,
        )
        print("recording %.1fs ..." % args.seconds)
        chunks = await client.record(args.seconds)
        samples = WebSDRClient.concat(chunks)
        rate = chunks[0].samplerate if chunks else 8000
    return _finish(samples, rate, args)


def _finish(samples: List[int], rate: int, args: argparse.Namespace) -> int:
    if not samples:
        print("no audio received", file=sys.stderr)
        return 1
    peak = max(abs(s) for s in samples)
    rms = (sum(s * s for s in samples) / len(samples)) ** 0.5
    if args.raw:
        with open(args.out, "wb") as handle:
            handle.write(struct.pack("<%dh" % len(samples), *samples))
    else:
        write_wav(args.out, samples, rate)
    print("wrote %s: %d samples @ %d Hz, peak=%d rms=%.0f"
          % (args.out, len(samples), rate, peak, rms))
    return 0


async def _info_openwebrx(args: argparse.Namespace) -> int:
    client = OpenWebRXClient(args.server, reconnect=False)
    await client.connect()
    try:
        await client.await_metadata()
        config = client.config
        print("server   :", client.url)
        print("center   :", config.center_freq)
        print("samp_rate:", config.samp_rate)
        print("start_mod:", config.start_mod)
        print("audio    :", config.audio_compression)
        print("fft      :", config.fft_compression)
        print("profiles :", len(client.profiles))
        print("modes    :", len(client.modes))
        if client.modes:
            print("  " + ",".join(m.get("modulation", "?") for m in client.modes))
    finally:
        await client.close()
    return 0


async def _info_websdr(args: argparse.Namespace) -> int:
    client = WebSDRClient(args.server)
    await client.connect()
    try:
        await client.tune(frequency_khz=float(args.freq) if args.freq else 10000.0,
                          mode=args.mode.upper())
        chunks = await client.record(2.0)
        samples = WebSDRClient.concat(chunks)
        print("server    :", client.url)
        print("samplerate:", client.samplerate)
        print("samples   :", len(samples))
        if samples:
            print("peak      :", max(abs(s) for s in samples))
    finally:
        await client.close()
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="sdrstream",
        description="Stream and record audio from public WebSDR / OpenWebRX servers.",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    def add_common(p: argparse.ArgumentParser) -> None:
        p.add_argument("--server", required=True,
                       help="server URL or host[:port]")
        p.add_argument("--kind", choices=("openwebrx", "websdr"), default="openwebrx",
                       help="protocol to use (default: openwebrx)")
        p.add_argument("--freq", default=None,
                       help="frequency: Hz for openwebrx, kHz for websdr")
        p.add_argument("--mode", default="lsb", help="modulation (default: lsb)")
        p.add_argument("--no-reconnect", action="store_true",
                       help="do not reconnect when the connection drops")

    rec = sub.add_parser("record", help="record audio to a file")
    add_common(rec)
    rec.add_argument("--seconds", type=float, default=10.0,
                     help="recording duration in seconds (default: 10)")
    rec.add_argument("--out", default="recording.wav", help="output file")
    rec.add_argument("--raw", action="store_true",
                     help="write headerless 16-bit PCM instead of WAV")
    rec.add_argument("--bandwidth", type=float, default=None,
                     help="passband width in Hz (websdr only)")

    inf = sub.add_parser("info", help="show server capabilities")
    add_common(inf)

    return parser


def main(argv: List[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    if args.command == "record":
        coro = _record_websdr(args) if args.kind == "websdr" else _record_openwebrx(args)
    else:
        coro = _info_websdr(args) if args.kind == "websdr" else _info_openwebrx(args)
    try:
        return asyncio.run(coro)
    except KeyboardInterrupt:
        return 130


if __name__ == "__main__":
    raise SystemExit(main())
