"""Record a live satellite pass, tracking Doppler, from the command line.

A thin wrapper over ``sdrstream.satellite`` that also accepts satellites which
are not in the bundled table (PO-101 and friends), because the table is a
starting point, not a boundary. Give it a name that appears in the TLE set and
a receiver URL and it does the rest.

    python tools/record_pass.py PO-101 --server http://websdr.78dx.ru:8901 \
        --out po101.wav
"""

from __future__ import annotations

import argparse
import asyncio
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from sdrstream import catalogue  # noqa: E402
from sdrstream.satellite import chase, plan_pass  # noqa: E402
from sdrstream.satellites import Satellite, SATELLITES, by_name  # noqa: E402


def make_satellite(name: str, downlink_hz: int, mode: str) -> Satellite:
    if name.upper() in {n.upper() for n in SATELLITES}:
        return by_name(name)
    return Satellite(name=name, norad_id=0, downlink_hz=downlink_hz,
                     mode=mode, notes="ad hoc")


def main(argv=None) -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("satellite")
    ap.add_argument("--server", required=True, help="receiver URL")
    ap.add_argument("--frequency", type=float, default=None,
                    help="downlink in MHz if the satellite is not in the table")
    ap.add_argument("--mode", default="FM", help="modulation (default FM)")
    ap.add_argument("--out", default="pass.wav")
    ap.add_argument("--min-elevation", type=float, default=5.0)
    ap.add_argument("--hours", type=float, default=12.0)
    ap.add_argument("--lead", type=float, default=60.0,
                    help="seconds to start before rise (default 60)")
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args(argv)

    if args.frequency:
        sat = make_satellite(args.satellite, int(args.frequency * 1e6),
                             args.mode)
    else:
        sat = by_name(args.satellite)

    receiver = catalogue.RECEIVERS.get(args.server)
    if receiver is None:
        receiver = catalogue.Receiver(url=args.server, kind="websdr",
                                      latitude=0.0, longitude=0.0)
        print("warning: %s not in the catalogue; give --lat/--lon to plan "
              "properly" % args.server)
        return 2

    plan = plan_pass(sat, receiver, hours=args.hours,
                     min_elevation=args.min_elevation,
                     lead_seconds=args.lead)
    print(plan.summary())
    if args.dry_run:
        return 0

    def say(message):
        print(message, flush=True)

    asyncio.run(chase(plan, args.out, on_progress=say))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
