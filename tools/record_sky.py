"""Record every reachable satellite pass, using every receiver we can reach.

Three sources feed the receiver list, and all three are used because no single
one is complete:

* the bundled catalogue, which is curated but stale;
* a live sweep of the public OpenWebRX directory, which is current but noisy;
* both protocols -- WebSDR and OpenWebRX -- because a downlink is heard by
  whichever happens to be nearby.

A pass is visible to many receivers at once. The recorder tries the best few in
turn and keeps the first that actually delivers audio, because a public
receiver that answers a tune is not the same as one that is carrying the signal.
A short probe settles that before a whole pass is committed to the wrong one.

    python tools/record_sky.py --hours 3 --min-elevation 15 \
        --outdir /tmp/captures --max-passes 8 --live

Each capture is written as ``<satellite>_<riseUTC>.wav`` with a sidecar JSON of
what was recorded, so a partial or empty pass is still visible in the log.
"""

from __future__ import annotations

import argparse
import asyncio
import json
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from sdrstream import catalogue, gopass, openwebrx_directory, tle  # noqa: E402
from sdrstream.satellite import ChasePlan, chase, plan_pass  # noqa: E402
from sdrstream.satellites import SATELLITES, Satellite  # noqa: E402
from sdrstream.tracking import Tracker  # noqa: E402

#: Receivers to try per pass before giving up. Each attempt costs a probe, so
#: this is a small number; ordering by elevation makes the first one count.
FALLBACKS = 3

#: Seconds of audio a probe needs to tell "carrying signal" from "silent".
PROBE_SECONDS = 1.5

#: Live-discovered receivers, filled in by :func:`load_live_receivers`.
LIVE_RECEIVERS: list = []


def tle_block_for(sat, elements):
    """A single-satellite TLE block, renamed to the table's own name.

    The table uses the common name (PO-101) while Celestrak uses the launch
    name (DIWATA-2B), so a name lookup alone misses. Resolve by NORAD id and
    hand ``plan_pass`` a block whose name line matches, which is what its
    tracker matches on.
    """
    for lines in elements.values():
        if tle.catnr(lines["line1"]) == sat.norad_id:
            return "%s\n%s\n%s\n" % (sat.name, lines["line1"], lines["line2"])
    return None


def tracker_for(sat, elements):
    """A :class:`Tracker` for *sat*, falling back to a by-NORAD TLE fetch.

    The downloaded groups miss a few table entries (AO-7, CAS-4A/B), so a
    direct catalogue-number fetch fills the gap; the returned block is renamed
    to the table's own name so the tracker matches on it.
    """
    for lines in elements.values():
        if tle.catnr(lines["line1"]) == sat.norad_id:
            return Tracker(lines["line1"], lines["line2"]), tle_block_for(sat, elements)
    text = tle.load_catnr(sat.norad_id)
    if not text:
        return None, None
    parsed = tle.parse(text)
    for lines in parsed.values():
        if tle.catnr(lines["line1"]) == sat.norad_id:
            block = "%s\n%s\n%s\n" % (sat.name, lines["line1"], lines["line2"])
            return Tracker(lines["line1"], lines["line2"]), block
    return None, None


def candidate_receivers(sat, live: bool):
    """Every receiver that can hear *sat*'s downlink."""
    found = {r.url: r for r in catalogue.for_frequency(sat.downlink_hz)}
    if live:
        for receiver in LIVE_RECEIVERS:
            if receiver.can_hear(sat.downlink_hz):
                found.setdefault(receiver.url, receiver)
    return list(found.values())


class _GoPass:
    """A predicted pass from the Go helper, shaped like ``tracking.Pass``.

    Only the fields the grouping and recording code read are carried, so a Go
    pass and a Python pass are interchangeable downstream.
    """

    def __init__(self, rise, set_time, max_elevation):
        self.rise = rise
        self.set_time = set_time
        self.max_elevation = max_elevation

    @property
    def duration(self):
        return (self.set_time - self.rise).total_seconds()

    def summary(self):
        return "%s rise  peak %5.1f deg  set %s" % (
            self.rise.strftime("%H:%M:%S"), self.max_elevation,
            self.set_time.strftime("%H:%M:%S"))


def _parse_stamp(text):
    return datetime.strptime(text, "%Y-%m-%dT%H:%M:%SZ").replace(
        tzinfo=timezone.utc)


def _rows_go(active, start, hours, min_elevation):
    """Pass rows for every satellite, predicted by the Go helper."""
    rows = []
    for sat, tracker, block, receivers in active:
        observers, indices = gopass.observers_for(receivers)
        lines = [line for line in block.splitlines() if line.strip()]
        job = {
            "start": start.strftime("%Y-%m-%dT%H:%M:%SZ"),
            "hours": hours,
            "min_elevation": min_elevation,
            "step_seconds": 30.0,
            "satellites": [{"name": sat.name,
                            "line1": lines[-2], "line2": lines[-1]}],
            "observers": observers,
        }
        report = gopass.predict(job)
        per_observer = report["satellites"][0]["observers"]
        for receiver, index in zip(receivers, indices):
            for p in per_observer[index]:
                rows.append({
                    "satellite": sat, "receiver": receiver, "tle": block,
                    "pass": _GoPass(_parse_stamp(p["rise"]),
                                    _parse_stamp(p["set"]),
                                    p["max_elevation"]),
                })
    return rows


def _rows_python(active, start, hours, min_elevation):
    """Pass rows for every satellite, predicted in pure Python."""
    rows = []
    for sat, tracker, block, receivers in active:
        observers = [(r.latitude, r.longitude) for r in receivers]
        per_observer = tracker.find_passes_multi(
            observers, start=start, hours=hours, min_elevation=min_elevation)
        for receiver, passes in zip(receivers, per_observer):
            for p in passes:
                rows.append({"satellite": sat, "receiver": receiver,
                             "pass": p, "tle": block})
    return rows


def schedule_passes(hours: float, min_elevation: float, live: bool):
    """Return one entry per physical pass, with all candidate receivers.

    A single orbital pass is seen by many receivers, and each sees it rise
    several minutes apart because they sit in different places, so group passes
    that overlap in time rather than comparing rise instants. Consecutive passes
    of one satellite are ~90 min apart, far wider than any single pass.

    Prediction runs through the Go helper when it is built, and falls back to
    the pure-Python finder otherwise; both produce the same passes.
    """
    elements = tle.parse(tle.load("amateur"))
    start = datetime.now(timezone.utc)
    active = []
    for sat in SATELLITES.values():
        if sat.status != "active":
            continue
        tracker, block = tracker_for(sat, elements)
        if tracker is None:
            continue
        active.append((sat, tracker, block, candidate_receivers(sat, live)))

    if gopass.available():
        rows = _rows_go(active, start, hours, min_elevation)
    else:
        rows = _rows_python(active, start, hours, min_elevation)
    rows.sort(key=lambda r: r["pass"].rise)

    # Group per satellite, then merge passes that overlap in time. Grouping the
    # interleaved list directly would break whenever another satellite's pass
    # fell between two receivers' views of the same pass.
    by_sat = {}
    for row in rows:
        by_sat.setdefault(row["satellite"].name, []).append(row)

    grouped = []
    for name in sorted(by_sat, key=lambda n: by_sat[n][0]["pass"].rise):
        sat_rows = sorted(by_sat[name], key=lambda r: r["pass"].rise)
        for row in sat_rows:
            p = row["pass"]
            if (grouped and grouped[-1]["satellite"].name == name
                    and p.rise <= grouped[-1]["pass"].set_time):
                grouped[-1]["candidates"].append(row)
                if p.max_elevation > grouped[-1]["pass"].max_elevation:
                    grouped[-1]["pass"] = p
            else:
                grouped.append({"satellite": row["satellite"],
                                "tle": row["tle"], "pass": p,
                                "candidates": [row]})
    grouped.sort(key=lambda g: g["pass"].rise)
    for group in grouped:
        group["candidates"].sort(key=lambda r: -r["pass"].max_elevation)
    return grouped


def _probe_plan(receiver, downlink_hz: float, mode: str) -> ChasePlan:
    sat = Satellite(name="probe", norad_id=0, downlink_hz=int(downlink_hz),
                    mode=mode)
    now = datetime.now(timezone.utc)
    return ChasePlan(satellite=sat, receiver=receiver, passes=[], start=now,
                     end=now, start_frequency_hz=downlink_hz,
                     doppler_range_hz=(0, 0))


async def probe(receiver, downlink_hz: float, mode: str) -> int:
    """Return how many audio samples a receiver delivers, fast.

    A receiver that answers the tune but sends nothing is the common failure,
    and one short round trip settles it before a whole pass is committed.
    """
    from sdrstream import satellite as sat_mod

    try:
        samples, _ = await sat_mod._segment(
            _probe_plan(receiver, downlink_hz, mode), downlink_hz,
            PROBE_SECONDS)
        return len(samples)
    except Exception:
        return 0


async def record_pass(group, outdir: Path, min_elevation: float, lead: float):
    sat = group["satellite"]
    rise = group["pass"].rise
    set_time = group["pass"].set_time
    now = datetime.now(timezone.utc)
    if now > set_time:
        print("[%s] missed: pass ended at %s"
              % (sat.name, set_time.strftime("%H:%M:%S")), flush=True)
        return {"satellite": sat.name, "samples": 0,
                "error": "pass already ended"}
    wait = (rise - now).total_seconds()
    if wait > 0:
        print("[%s] waiting %.0f s for rise" % (sat.name, wait), flush=True)
        await asyncio.sleep(wait)

    attempts = []
    for candidate in group["candidates"][:FALLBACKS]:
        receiver = candidate["receiver"]
        delivered = await probe(receiver, sat.downlink_hz, sat.mode)
        print("[%s] probe %s -> %d samples"
              % (sat.name, receiver.url, delivered), flush=True)
        if delivered == 0:
            attempts.append({"receiver": receiver.url,
                             "error": "probe returned no audio"})
            continue
        try:
            # Start the search a few minutes early so a receiver whose own
            # rise is a little ahead of the group's still matches this pass
            # rather than the next one.
            plan = plan_pass(sat, receiver, hours=2.0,
                             start=rise - timedelta(minutes=6),
                             min_elevation=min_elevation, lead_seconds=lead,
                             tle_text=group["tle"])
        except Exception as exc:
            attempts.append({"receiver": receiver.url, "error": str(exc)})
            continue
        stamp = plan.start.strftime("%Y%m%dT%H%M%S")
        wav = outdir / ("%s_%s.wav" % (sat.name.replace(" ", ""), stamp))
        record = {"satellite": sat.name, "norad_id": sat.norad_id,
                  "receiver": receiver.url, "kind": receiver.kind,
                  "band": sat.band, "downlink_mhz": sat.downlink_hz / 1e6,
                  "start_utc": plan.start.isoformat(),
                  "end_utc": plan.end.isoformat(),
                  "file": str(wav), "samples": 0, "error": None}
        try:
            samples = await chase(plan, str(wav),
                                  on_progress=lambda m: print(m, flush=True))
            record["samples"] = samples
            return record
        except Exception as exc:
            record["error"] = "%s: %s" % (type(exc).__name__, exc)
            attempts.append(record)
            print("[%s] %s gave no audio (%s); trying next"
                  % (sat.name, receiver.url, exc), flush=True)
    return attempts[-1] if attempts else {"satellite": sat.name, "samples": 0,
                                          "error": "no candidate receivers"}


def wav_stats(path: Path):
    """Sample rate and peak of a written WAV, so the UI can show them."""
    import wave

    with wave.open(str(path), "rb") as handle:
        rate = handle.getframerate()
        frames = handle.readframes(handle.getnframes())
    peak = 0
    for i in range(0, len(frames) - 1, 2):
        value = int.from_bytes(frames[i:i + 2], "little", signed=True)
        peak = max(peak, abs(value))
    return rate, peak


async def record_all(groups, outdir: Path, min_elevation: float, lead: float):
    results = []
    summary = outdir / "captures.json"
    for group in groups:
        result = await record_pass(group, outdir, min_elevation, lead)
        if result.get("samples"):
            try:
                result["samplerate"], result["peak"] = wav_stats(
                    Path(result["file"]))
            except (OSError, EOFError):
                pass
        results.append(result)
        # Rewrite the sidecar after every pass so a live view can follow along
        # instead of waiting for the whole campaign to finish.
        summary.write_text(json.dumps(results, indent=2))
    return results


def load_live_receivers(timeout: float = 30.0) -> int:
    """Sweep the public OpenWebRX directory for satellite-band receivers."""
    try:
        found = openwebrx_directory.discover(timeout=timeout)
    except Exception as exc:
        print("live directory sweep failed (%s); using the catalogue only" % exc)
        return 0
    LIVE_RECEIVERS.extend(found)
    sat_band = [r for r in found if r.satellite_bands]
    print("live sweep: %d OpenWebRX receivers, %d cover a satellite band"
          % (len(found), len(sat_band)), flush=True)
    return len(sat_band)


def main(argv=None) -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--hours", type=float, default=3.0)
    ap.add_argument("--min-elevation", type=float, default=15.0)
    ap.add_argument("--lead", type=float, default=45.0)
    ap.add_argument("--max-passes", type=int, default=8)
    ap.add_argument("--outdir", default="/tmp/captures")
    ap.add_argument("--live", action="store_true",
                    help="add live-discovered OpenWebRX receivers")
    args = ap.parse_args(argv)

    outdir = Path(args.outdir)
    outdir.mkdir(parents=True, exist_ok=True)
    if args.live:
        load_live_receivers()

    groups = schedule_passes(args.hours, args.min_elevation, args.live)
    print("planning %d passes in the next %.1f h (min elev %.0f deg)"
          % (len(groups), args.hours, args.min_elevation), flush=True)
    for group in groups[:args.max_passes]:
        p = group["pass"]
        best = group["candidates"][0]["receiver"]
        print("  %-8s %-40s rise %s peak %5.1f el (%d receivers)"
              % (group["satellite"].name, best.url[:40],
                 p.rise.strftime("%H:%M:%S"), p.max_elevation,
                 len(group["candidates"])), flush=True)

    groups = groups[:args.max_passes]
    # Publish the whole plan so the web UI can show what is queued as well as
    # what has been captured.
    plan = [{"satellite": g["satellite"].name, "band": g["satellite"].band,
             "downlink_mhz": g["satellite"].downlink_hz / 1e6,
             "rise_utc": g["pass"].rise.isoformat(),
             "set_utc": g["pass"].set_time.isoformat(),
             "max_elevation": g["pass"].max_elevation,
             "candidates": [c["receiver"].url for c in g["candidates"]]}
            for g in groups]
    (outdir / "plan.json").write_text(json.dumps(plan, indent=2))
    results = asyncio.run(record_all(groups, outdir, args.min_elevation,
                                     args.lead))
    summary = outdir / "captures.json"
    summary.write_text(json.dumps(results, indent=2))
    print("\nwrote %s" % summary)
    ok = [r for r in results if r.get("samples")]
    print("%d/%d captures produced audio" % (len(ok), len(results)))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
