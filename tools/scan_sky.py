"""Scan the sky: which satellites can we reach, and when.

Pairs every satellite in the amateur table with the receivers whose published
bands cover its downlink, predicts passes over each, and ranks the results by
how soon they happen and how high they get. Pure prediction -- no network
beyond the one TLE fetch, so it is safe to run anywhere.

    python tools/scan_sky.py --hours 6 --min-elevation 10
    python tools/scan_sky.py --hours 3 --json plan.json

The output is the input to a recording run: each row names a satellite, a
receiver and the exact UTC window to record.
"""

from __future__ import annotations

import argparse
import json
import sys
from datetime import datetime, timezone
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from sdrstream import catalogue, tle  # noqa: E402
from sdrstream.satellites import SATELLITES  # noqa: E402
from sdrstream.tracking import Tracker  # noqa: E402


def build_trackers(group: str = "amateur"):
    """Return {norad_id: (name, Tracker)} for every satellite with a TLE."""
    elements = tle.parse(tle.load(group))
    by_name = {}
    for name, lines in elements.items():
        try:
            by_name[name.upper()] = Tracker(lines["line1"], lines["line2"])
        except Exception:
            continue
    return elements, by_name


def tracker_for(sat, elements, trackers):
    """Find the TLE for a table entry by NORAD id, then by name fragment."""
    want = sat.norad_id
    for name, lines in elements.items():
        if tle.catnr(lines["line1"]) == want:
            return Tracker(lines["line1"], lines["line2"])
    key = sat.name.upper()
    if key in trackers:
        return trackers[key]
    for name, tracker in trackers.items():
        if key in name:
            return tracker
    return None


def scan(hours: float, min_elevation: float, max_per_sat: int):
    elements, trackers = build_trackers()
    start = datetime.now(timezone.utc)
    rows = []
    for sat in SATELLITES.values():
        tracker = tracker_for(sat, elements, trackers)
        if tracker is None:
            rows.append({"satellite": sat.name, "band": sat.band,
                         "note": "no TLE", "receiver": None})
            continue
        receivers = catalogue.for_frequency(sat.downlink_hz)
        if not receivers:
            rows.append({"satellite": sat.name, "band": sat.band,
                         "note": "no receiver covers band", "receiver": None})
            continue
        best = []
        for receiver in receivers:
            passes = tracker.find_passes(
                receiver.latitude, receiver.longitude,
                start=start, hours=hours, min_elevation=min_elevation,
            )
            for p in passes[:max_per_sat]:
                best.append((p.rise, p.max_elevation, p, receiver))
        best.sort(key=lambda item: item[0])
        if not best:
            rows.append({"satellite": sat.name, "band": sat.band,
                         "note": "no pass in window", "receiver": None})
            continue
        rise, max_el, p, receiver = best[0]
        rows.append({
            "satellite": sat.name,
            "band": sat.band,
            "mode": sat.mode,
            "downlink_mhz": sat.downlink_hz / 1e6,
            "receiver": receiver.url,
            "receiver_lat": receiver.latitude,
            "receiver_lon": receiver.longitude,
            "rise_utc": rise.isoformat(),
            "minutes_away": (rise - start).total_seconds() / 60.0,
            "max_elevation": max_el,
            "duration_s": p.duration,
            "note": "",
        })
    rows.sort(key=lambda r: (r.get("minutes_away") is None,
                             r.get("minutes_away", 1e9)))
    return rows


def main(argv=None) -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--hours", type=float, default=6.0,
                    help="prediction window (default: 6)")
    ap.add_argument("--min-elevation", type=float, default=10.0,
                    help="minimum peak elevation in degrees (default: 10)")
    ap.add_argument("--max-per-sat", type=int, default=1,
                    help="passes to keep per satellite (default: 1)")
    ap.add_argument("--json", default=None, help="write the plan to this file")
    args = ap.parse_args(argv)

    rows = scan(args.hours, args.min_elevation, args.max_per_sat)
    reachable = [r for r in rows if r["receiver"]]
    print("scanned %d satellites, %d have a pass in the next %.1f h"
          % (len(rows), len(reachable), args.hours))
    print("%-9s %-6s %-38s %8s %6s %6s"
          % ("SAT", "BAND", "RECEIVER", "IN (min)", "MAX EL", "DUR (s)"))
    for r in reachable:
        print("%-9s %-6s %-38s %8.1f %6.1f %6.0f"
              % (r["satellite"], r["band"], r["receiver"][:38],
                 r["minutes_away"], r["max_elevation"], r["duration_s"]))
    skipped = [r for r in rows if not r["receiver"]]
    if skipped:
        print("\nnot reachable now:")
        for r in skipped:
            print("  %-9s %-6s %s" % (r["satellite"], r["band"], r["note"]))
    if args.json:
        Path(args.json).write_text(json.dumps(rows, indent=2))
        print("\nwrote %s" % args.json)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
