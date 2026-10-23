"""Automatic satellite-pass scheduler (mirror of go/internal/satsched).

Ranks upcoming passes of priority satellites across the live-verified
receivers and drives recording, decoding and web integration for the top
pass. The Go implementation is authoritative; this module keeps the
planning data structures in sync for the Python port.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import List, Optional

# Default recording priority, in order. NOAA 15/18/19 were decommissioned
# in 2025 (NOAA OSPO) and their APT transmitters are unvalidated
# (2026-10-23 attempts were noise-only), so the automatic default does not
# schedule retired birds; they remain selectable by explicit request.
# ISS leads: current, validated traffic (SSTV events, APRS).
DEFAULT_PRIORITY = ["ISS"]

# A NOAA pass only counts if it rises above this elevation: APT decodes
# need signal, and low passes drown in noise.
MIN_ELEVATION_DEG = 25.0

# Recording window padding around rise/set, in seconds.
LEAD_S = 90
TAIL_S = 60


@dataclass
class Plan:
    """One chosen pass to record."""

    satellite: str
    frequency: float  # Hz
    mode: str
    receiver: str
    kind: str
    lat: float
    lon: float
    start_utc: str
    end_utc: str
    max_el: float
    doppler_lo: float  # Hz
    doppler_hi: float  # Hz


@dataclass
class Status:
    """One decision-log entry."""

    at_utc: str
    event: str
    satellite: str
    receiver: str
    detail: str


@dataclass
class Options:
    """Scheduler configuration; mirror of satsched.Options."""

    satellites: List[str] = field(default_factory=lambda: list(DEFAULT_PRIORITY))
    min_elevation: float = MIN_ELEVATION_DEG
    lookahead_h: int = 24
    captures_dir: str = ""
    server: object = None  # optional web.Server hook
    say: object = print  # log sink


def plan(passes, satellites, receivers, min_el=MIN_ELEVATION_DEG):
    """Pick the highest-priority upcoming pass.

    ``passes`` is a mapping satellite-name -> (pass, receiver) pairs
    pre-filtered to rise above ``min_el``. Priority order wins over
    start time: an earlier pass of a higher-priority satellite beats a
    later pass of a lower-priority one at any time.
    """
    for sat in satellites:
        best: Optional[Plan] = None
        for p, rx in passes.get(sat, []):
            if p["max_el"] < min_el:
                continue
            if best is None or p["rise"] < best.start_utc:
                best = Plan(
                    satellite=sat,
                    frequency=p.get("frequency", 0.0),
                    mode=p.get("mode", ""),
                    receiver=rx["name"],
                    kind=rx["kind"],
                    lat=rx["lat"],
                    lon=rx["lon"],
                    start_utc=p["rise"],
                    end_utc=p["set"],
                    max_el=p["max_el"],
                    doppler_lo=p.get("doppler_lo", 0.0),
                    doppler_hi=p.get("doppler_hi", 0.0),
                )
        if best is not None:
            return best
    return None
