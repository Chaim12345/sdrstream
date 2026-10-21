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
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import List

from . import (catalogue, discovery, military,
                 openwebrx_directory as directory, satellite, satnogs)
from .common import write_wav
from .openwebrx import OpenWebRXClient
from .satellites import SATELLITES, by_name
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


def _parse_when(value: str) -> datetime:
    """Accept an ISO timestamp or a relative delay such as ``+15m``."""
    value = value.strip()
    if value.startswith("+"):
        text = value[1:]
        scale = {"s": 1, "m": 60, "h": 3600}.get(text[-1].lower())
        if scale is None:
            raise argparse.ArgumentTypeError("delay must end in s, m or h")
        return datetime.now(timezone.utc) + timedelta(seconds=float(text[:-1]) * scale)
    try:
        parsed = datetime.fromisoformat(value)
    except ValueError as exc:
        raise argparse.ArgumentTypeError(
            "expected ISO time or +delay (e.g. +15m): %s" % exc) from exc
    return parsed if parsed.tzinfo else parsed.replace(tzinfo=timezone.utc)


def _catalogue_receiver(url: str, kind: str = "websdr"):
    """Look a URL up in the catalogue, falling back to an ad-hoc receiver."""
    for receiver in catalogue.RECEIVERS.values():
        if url.rstrip("/") == receiver.url.rstrip("/"):
            return receiver
    return None


async def _satellites(args: argparse.Namespace) -> int:
    if args.satellite:
        sat = by_name(args.satellite)
        print("%s (NORAD %d)" % (sat.name, sat.norad_id))
        print("  downlink : %.4f MHz  %s  band %s"
              % (sat.downlink_hz / 1e6, sat.mode, sat.band))
        if sat.uplink_hz:
            tone = " tone %.1f Hz" % sat.tone_hz if sat.tone_hz else ""
            print("  uplink   : %.4f MHz%s" % (sat.uplink_hz / 1e6, tone))
        print("  status   : %s" % sat.status)
        if sat.notes:
            print("  notes    : %s" % sat.notes)
        receivers = catalogue.for_frequency(sat.downlink_hz)
        print("  receivers: %d in the catalogue can hear this downlink" % len(receivers))
        for receiver in receivers[:8]:
            print("    %-40s %.2f,%.2f"
                  % (receiver.url, receiver.latitude, receiver.longitude))
        return 0

    for band in ("2m", "70cm", "23cm"):
        sats = [s for s in SATELLITES.values() if s.band == band]
        if not sats:
            continue
        print("%s (%d):" % (band, len(sats)))
        for sat in sorted(sats, key=lambda s: s.name):
            print("  %-9s %10.4f MHz  %-4s %s"
                  % (sat.name, sat.downlink_hz / 1e6, sat.mode, sat.notes[:46]))
    return 0


def _resolve_observer(args: argparse.Namespace):
    """Return (receiver, latitude, longitude) for a satellite subcommand."""
    receiver = _catalogue_receiver(args.server) if args.server else None
    if receiver is None and args.server:
        receiver = catalogue.Receiver(url=args.server, kind=args.kind,
                                      latitude=args.lat, longitude=args.lon)
    latitude, longitude = args.lat, args.lon
    if latitude is None or longitude is None:
        if receiver is not None:
            latitude, longitude = receiver.location
        else:
            raise SystemExit("give --lat/--lon, or --server with a catalogue URL")
    return receiver, latitude, longitude


async def _passes(args: argparse.Namespace) -> int:
    sat = by_name(args.satellite)
    receiver, latitude, longitude = _resolve_observer(args)
    plan = satellite.plan_pass(
        sat, receiver, latitude=latitude, longitude=longitude,
        hours=args.hours, min_elevation=args.min_elevation,
        start=_parse_when(args.when) if args.when else None,
    )
    print(plan.summary())
    return 0


async def _satrecord(args: argparse.Namespace) -> int:
    sat = by_name(args.satellite)
    receiver, latitude, longitude = _resolve_observer(args)
    plan = satellite.plan_pass(
        sat, receiver, latitude=latitude, longitude=longitude,
        hours=args.hours, min_elevation=args.min_elevation,
        start=_parse_when(args.when) if args.when else None,
    )
    print(plan.summary())
    if args.dry_run:
        return 0
    await satellite.chase(plan, args.out, on_progress=print,
                          offline_tle=args.offline_tle)
    return 0


async def _observations(args: argparse.Namespace) -> int:
    """Show what SatNOGS ground stations have actually heard."""
    sat = by_name(args.satellite)
    print("%s (NORAD %d)" % (sat.name, sat.norad_id))
    best = satnogs.best_downlink(sat.norad_id)
    if best:
        print("  SatNOGS best downlink: %s %.4f MHz %s"
              % (best.get("description") or "(unnamed)",
                 (best.get("downlink_low") or 0) / 1e6,
                 best.get("mode") or "?"))
    print("  table says           : %.4f MHz %s"
          % (sat.downlink_hz / 1e6, sat.mode))

    found = satnogs.cached_observations(sat.norad_id, limit=args.limit)
    print("\n%d recent observations:" % len(found))
    for observation in found[:args.limit]:
        print("  " + observation.summary())

    counts = satnogs.observed_frequencies(sat.norad_id, limit=args.limit)
    if counts:
        print("\nobserved downlinks:")
        for frequency, count in counts.items():
            print("  %9.4f MHz  %3d observations" % (frequency / 1e6, count))
    return 0


async def _fetch(args: argparse.Namespace) -> int:
    """Download the data SatNOGS ground stations captured for a satellite."""
    sat = by_name(args.satellite)
    found = satnogs.cached_observations(sat.norad_id, limit=args.limit)
    with_data = [o for o in found
                 if (o.waterfall_url and args.waterfall)
                 or (o.demod_url and args.demod)]
    if not with_data:
        print("no observations of %s with downloadable data" % sat.name)
        return 1

    out_dir = Path(args.out)
    out_dir.mkdir(parents=True, exist_ok=True)
    saved = 0
    for observation in with_data[:args.count]:
        if args.waterfall and observation.waterfall_url:
            target = out_dir / ("%d_waterfall.png" % observation.id)
            satnogs.download(observation.waterfall_url, target)
            print("wrote %s" % target)
            saved += 1
        if args.demod and observation.demod_url:
            suffix = observation.demod_url.rsplit("/", 1)[-1]
            target = out_dir / ("%d_%s.dat" % (observation.id, suffix))
            satnogs.download(observation.demod_url, target)
            print("wrote %s (%d bytes)"
                  % (target, target.stat().st_size))
            saved += 1
    print("saved %d file(s) to %s" % (saved, out_dir))
    return 0


async def _frames(args: argparse.Namespace) -> int:
    """Fetch the raw telemetry frames SatNOGS stations submitted for a satellite."""
    sat = by_name(args.satellite)
    start = None
    if args.hours is not None:
        start = datetime.now(timezone.utc) - timedelta(hours=args.hours)
    frames = satnogs.telemetry_frames(
        sat.norad_id, start=start, limit=args.limit, token=args.token)
    if not frames:
        print("no telemetry frames found for %s (NORAD %d)"
              % (sat.name, sat.norad_id))
        return 1
    print("%d telemetry frames for %s (NORAD %d):"
          % (len(frames), sat.name, sat.norad_id))
    for frame in frames:
        print("  " + frame.summary())
    if args.save:
        out_dir = Path(args.save)
        out_dir.mkdir(parents=True, exist_ok=True)
        for frame in frames:
            payload = frame.frame or ""
            target = out_dir / ("frame_%d.bin" % frame.id)
            try:  # the DB stores frames as hex text
                target.write_bytes(bytes.fromhex(payload))
            except ValueError:
                target.write_text(payload)
        print("wrote %d frame file(s) to %s" % (len(frames), out_dir))
    return 0


async def _schedule(args: argparse.Namespace) -> int:
    """Manage and run the automatic pass-recording scheduler."""
    from .schedule import DEFAULT_STORE, Scheduler

    scheduler = Scheduler(DEFAULT_STORE)
    if args.add:
        job = scheduler.add(args.add, output_dir=args.dir,
                            min_elevation=args.min_elevation)
        print("following %s (passes above %.0f deg, saving to %s)"
              % (job.satellite, job.min_elevation, job.output_dir))
    if args.remove:
        print("removed" if scheduler.remove(args.remove)
              else "no job for %s" % args.remove)
    if args.list:
        jobs = scheduler.jobs()
        if not jobs:
            print("no scheduled satellites; use --add SAT")
        for job in jobs:
            print("  %-16s el >%-3.0f deg  next %2.0f h  -> %s (%d passes done)"
                  % (job.satellite, job.min_elevation,
                     job.lookahead_hours, job.output_dir, len(job.done)))
    if args.once or args.run:
        if not scheduler.jobs():
            print("nothing scheduled; use --add SAT first")
            return 1
        if args.run:
            scheduler.run(poll_seconds=args.poll, on_event=print)
        else:
            recorded = scheduler.run_once(on_event=print)
            print("recorded %d pass(es)" % recorded)
    if not (args.add or args.remove or args.list or args.once or args.run):
        print("nothing to do; use --add SAT, --list, --once or --run")
    return 0


async def _catalogue(args: argparse.Namespace) -> int:
    """Show the receiver catalogue, optionally refreshing it first."""
    if args.refresh:
        count = catalogue.refresh()
        print("refreshed the catalogue: %d receivers" % count)
    else:
        print("%d bundled receivers (use --refresh for the live list)"
              % len(catalogue.RECEIVERS))
    receivers = (catalogue.for_band(args.band) if args.band
                 else sorted(catalogue.RECEIVERS.values(),
                             key=lambda r: r.url))
    for receiver in receivers:
        tags = ",".join(receiver.satellite_bands) or "-"
        print("  %-45s %-8s %7.3f, %7.3f  %s"
              % (receiver.url, receiver.kind,
                 receiver.latitude, receiver.longitude, tags))
    return 0


async def _discover(args: argparse.Namespace) -> int:
    """Find OpenWebRX receivers that can hear a satellite downlink."""
    frequency = args.frequency
    if frequency is not None:
        frequency *= 1000.0  # kHz on the command line, Hz internally

    print("reading %s ..." % directory.DIRECTORY_URL)
    receivers = directory.discover(timeout=args.timeout, limit=args.limit)
    print("probed receivers that answered: %d" % len(receivers))

    if frequency is not None:
        receivers = directory.for_frequency(frequency, receivers, band=args.band)
        print("able to hear %.4f MHz: %d" % (frequency / 1e6, len(receivers)))
    elif args.band:
        receivers = [r for r in receivers if args.band in r.satellite_bands]

    for receiver in sorted(receivers, key=lambda r: r.url):
        print("  %-46s (%8.4f,%9.4f) %s"
              % (receiver.url, receiver.latitude, receiver.longitude,
                 ",".join(receiver.satellite_bands) or "-"))
    return 0


async def _undocumented(args: argparse.Namespace) -> int:
    """List satellites heard by the network but missing from our table."""
    print(discovery.report(limit=args.limit, status=args.status))
    return 0


async def _military(args: argparse.Namespace) -> int:
    """Show government and military downlinks a public receiver can reach."""
    if args.live:
        print("probing the public directory, this takes about 20s ...")
        receivers = directory.discover(timeout=args.timeout)
    else:
        receivers = list(catalogue.RECEIVERS.values())
    print(military.report(receivers))
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

    def add_satellite(p: argparse.ArgumentParser) -> None:
        p.add_argument("satellite", help="satellite name, e.g. SO-50 or ISS")
        p.add_argument("--lat", type=float, default=None, help="observer latitude")
        p.add_argument("--lon", type=float, default=None, help="observer longitude")
        p.add_argument("--hours", type=float, default=12.0,
                       help="how far ahead to search (default: 12)")
        p.add_argument("--min-elevation", type=float, default=10.0,
                       help="minimum peak elevation in degrees (default: 10)")
        p.add_argument("--when", default=None,
                       help="start time: ISO 8601 or a delay like +15m")
        p.add_argument("--server", default=None,
                       help="receiver URL, looked up in the catalogue")
        p.add_argument("--kind", choices=("openwebrx", "websdr"), default="websdr")

    fet = sub.add_parser("fetch",
                         help="download SatNOGS captures for a satellite")
    fet.add_argument("satellite", help="satellite name, e.g. ISS")
    fet.add_argument("--out", default="satnogs", help="output directory")
    fet.add_argument("--limit", type=int, default=25,
                     help="how many observations to look through (default: 25)")
    fet.add_argument("--count", type=int, default=1,
                     help="how many observations to download (default: 1)")
    fet.add_argument("--no-waterfall", dest="waterfall", action="store_false",
                     help="skip the waterfall images")
    fet.add_argument("--no-demod", dest="demod", action="store_false",
                     help="skip the demodulated data files")

    obs = sub.add_parser("observations",
                         help="show real SatNOGS receptions of a satellite")
    obs.add_argument("satellite", help="satellite name, e.g. SO-50")
    obs.add_argument("--limit", type=int, default=15,
                     help="how many observations to show (default: 15)")

    frm = sub.add_parser("frames",
                        help="fetch raw SatNOGS telemetry frames (needs a token)")
    frm.add_argument("satellite", help="satellite name, e.g. ISS")
    frm.add_argument("--limit", type=int, default=50,
                     help="how many frames to fetch (default: 50)")
    frm.add_argument("--hours", type=float, default=None,
                     help="only frames from the last N hours")
    frm.add_argument("--save", default=None,
                     help="write frames to this directory as one file each")
    frm.add_argument("--token", default=None,
                     help="SatNOGS API token (or set SATNOGS_TOKEN)")

    sch = sub.add_parser("schedule",
                         help="record satellite passes automatically")
    sch.add_argument("--add", metavar="SAT",
                    help="follow a satellite and record its passes")
    sch.add_argument("--remove", metavar="SAT", help="stop following a satellite")
    sch.add_argument("--list", action="store_true", help="list scheduled jobs")
    sch.add_argument("--once", action="store_true",
                    help="check for due passes, record them, exit")
    sch.add_argument("--run", action="store_true",
                    help="keep checking and recording until stopped")
    sch.add_argument("--dir", default="captures",
                    help="directory for recordings (default: captures)")
    sch.add_argument("--min-elevation", type=float, default=10.0,
                    help="minimum pass elevation to record (default: 10)")
    sch.add_argument("--poll", type=float, default=60.0,
                    help="seconds between checks in --run (default: 60)")

    cat = sub.add_parser("catalogue",
                         help="show or refresh the receiver catalogue")
    cat.add_argument("--refresh", action="store_true",
                     help="reload receivers from the live WebSDR list")
    cat.add_argument("--band", default=None,
                     help="list receivers tagged for this band (2m, 70cm, ...)")

    sats = sub.add_parser("satellites", help="list amateur satellites")
    sats.add_argument("satellite", nargs="?", default=None,
                      help="show details and receivers for one satellite")

    passes = sub.add_parser("passes", help="predict satellite passes")
    add_satellite(passes)

    srec = sub.add_parser("satrecord", help="record a pass, tracking Doppler")
    add_satellite(srec)
    srec.add_argument("--out", default="satellite.wav", help="output file")
    srec.add_argument("--dry-run", action="store_true",
                      help="show the plan without recording")
    srec.add_argument("--offline-tle", action="store_true",
                      help="use the cached TLE set even if stale")

    disc = sub.add_parser("discover",
                          help="find OpenWebRX receivers for a downlink")
    disc.add_argument("--frequency", type=float, default=None,
                      help="downlink in kHz, e.g. 145800")
    disc.add_argument("--band", default=None,
                      choices=("6m", "2m", "70cm", "23cm"),
                      help="keep only receivers covering this amateur band")
    disc.add_argument("--limit", type=int, default=None,
                      help="probe only the first N servers")
    disc.add_argument("--timeout", type=float, default=30.0,
                      help="directory fetch timeout in seconds (default: 30)")

    undoc = sub.add_parser("undocumented",
                           help="satellites heard recently but not in our table")
    undoc.add_argument("--limit", type=int, default=100,
                       help="how many observations to scan (default: 100)")
    undoc.add_argument("--status", default="good",
                       help="observation status to scan (default: good)")

    mil = sub.add_parser("military",
                         help="government and military downlinks in reach")
    mil.add_argument("--live", action="store_true",
                     help="check against a live directory sweep, not the "
                          "bundled catalogue")
    mil.add_argument("--timeout", type=float, default=30.0,
                     help="directory fetch timeout in seconds (default: 30)")

    web = sub.add_parser("web", help="serve the browser UI")
    web.add_argument("--host", default="127.0.0.1",
                     help="address to bind (default: 127.0.0.1)")
    web.add_argument("--port", type=int, default=8000,
                     help="port to bind (default: 8000)")

    return parser


def main(argv: List[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    if args.command == "web":
        from .web import serve

        serve(host=args.host, port=args.port)
        return 0
    if args.command == "record":
        coro = _record_websdr(args) if args.kind == "websdr" else _record_openwebrx(args)
    elif args.command == "info":
        coro = _info_websdr(args) if args.kind == "websdr" else _info_openwebrx(args)
    elif args.command == "satellites":
        coro = _satellites(args)
    elif args.command == "observations":
        coro = _observations(args)
    elif args.command == "frames":
        coro = _frames(args)
    elif args.command == "schedule":
        coro = _schedule(args)
    elif args.command == "catalogue":
        coro = _catalogue(args)
    elif args.command == "fetch":
        coro = _fetch(args)
    elif args.command == "passes":
        coro = _passes(args)
    elif args.command == "satrecord":
        coro = _satrecord(args)
    elif args.command == "discover":
        coro = _discover(args)
    elif args.command == "undocumented":
        coro = _undocumented(args)
    elif args.command == "military":
        coro = _military(args)
    else:
        raise SystemExit("unknown command %r" % args.command)
    try:
        return asyncio.run(coro)
    except KeyboardInterrupt:
        return 130
    except (ValueError, KeyError, FileNotFoundError, RuntimeError) as exc:
        print("error: %s" % exc, file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
