"""Find satellites that are being heard but are missing from our table.

:mod:`sdrstream.satellites` is a small curated list of the downlinks most likely
to be audible. It cannot keep up with the roughly ten thousand observations a
day the SatNOGS network records, which is where newly launched and lightly
documented spacecraft show up first.

This module reads the live observation feed, which is unfiltered, and reports
every satellite it sees that our table does not know about, with the
frequencies ground stations actually observed. That is how an undocumented
transmitter is found: it appears in the feed before anyone writes it down.

    >>> from sdrstream import discovery
    >>> new = discovery.unknown_satellites()
    >>> new[0].summary()
    '98371  BLACK KITE-2  437.5000 MHz  FSK'
"""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime
from typing import Any, Dict, List, Optional

from .satnogs import _get, _observation, NETWORK_API
from .satellites import SATELLITES


@dataclass
class SeenSatellite:
    """A satellite observed by the network, with what was heard."""

    norad_id: int
    name: str
    frequencies_hz: List[int] = field(default_factory=list)
    modes: List[str] = field(default_factory=list)
    observation_count: int = 0
    last_seen: Optional[datetime] = None
    #: ``True`` when the satellite is in our curated table already.
    known: bool = False

    @property
    def downlink_hz(self) -> Optional[int]:
        """The most frequently observed downlink, which is the likeliest one."""
        if not self.frequencies_hz:
            return None
        counts: Dict[int, int] = {}
        for frequency in self.frequencies_hz:
            counts[frequency] = counts.get(frequency, 0) + 1
        return max(counts, key=lambda frequency: (counts[frequency], -frequency))

    def summary(self) -> str:
        frequency = self.downlink_hz
        return "%d  %-28s %s  %s" % (
            self.norad_id, self.name[:28],
            "%.4f MHz" % (frequency / 1e6) if frequency else "no frequency",
            "/".join(sorted(set(self.modes))) or "unknown mode")


def _record(raw: Dict[str, Any]) -> Optional[SeenSatellite]:
    norad_id = raw.get("norad_cat_id")
    if not norad_id:
        return None
    name = (raw.get("tle0") or raw.get("satellite") or "").strip()
    return SeenSatellite(norad_id=int(norad_id), name=name or "NORAD %d" % norad_id)


def recent(
    limit: int = 100,
    *,
    status: str = "good",
    timeout: float = 45.0,
) -> Dict[int, SeenSatellite]:
    """Every satellite in the most recent observations, keyed by NORAD id.

    The observation feed is used rather than the satellite catalogue because
    the feed needs no filter and always reflects what is actually on the air.
    """
    url = "%s/observations/?format=json&limit=%d" % (NETWORK_API, limit)
    if status:
        url += "&status=%s" % status
    raw_observations = _get(url, timeout=timeout)

    seen: Dict[int, SeenSatellite] = {}
    for raw in raw_observations:
        entry = _record(raw)
        if entry is None:
            continue
        observation = _observation(raw)
        satellite = seen.setdefault(entry.norad_id, entry)
        satellite.observation_count += 1
        if observation.frequency_hz:
            satellite.frequencies_hz.append(observation.frequency_hz)
        if observation.mode:
            satellite.modes.append(observation.mode)
        when = observation.start or observation.end
        if when and (satellite.last_seen is None or when > satellite.last_seen):
            satellite.last_seen = when
    return seen


def known_ids() -> set:
    """NORAD ids already covered by the curated table."""
    return {satellite.norad_id for satellite in SATELLITES.values()
            if satellite.norad_id}


def unknown_satellites(limit: int = 100, *, status: str = "good",
                       timeout: float = 45.0) -> List[SeenSatellite]:
    """Satellites heard recently that the curated table does not list.

    Sorted by how often they were heard, so the busiest unknown transmitter
    comes first.
    """
    known = known_ids()
    found = [satellite for satellite in recent(limit, status=status,
                                               timeout=timeout).values()
             if satellite.norad_id not in known]
    for satellite in found:
        satellite.known = False
    found.sort(key=lambda satellite: (-satellite.observation_count,
                                      satellite.name))
    return found


def in_amateur_bands(satellite: SeenSatellite) -> bool:
    """Whether the observed downlink lands in a band our receivers can hear."""
    frequency = satellite.downlink_hz
    if not frequency:
        return False
    return any(low <= frequency <= high for low, high in (
        (50e6, 54e6), (144e6, 148e6), (430e6, 440e6), (1240e6, 1300e6)))


def report(limit: int = 100, *, status: str = "good",
           timeout: float = 45.0) -> str:
    """A readable list of recently heard satellites missing from the table."""
    found = unknown_satellites(limit, status=status, timeout=timeout)
    if not found:
        return "every satellite heard recently is already in the table"

    lines = ["%d satellite(s) heard recently that the table does not list:"
             % len(found)]
    for satellite in found:
        marker = "sat-band" if in_amateur_bands(satellite) else "out-of-band"
        lines.append("  [%s] %s  (%d observation(s))"
                     % (marker, satellite.summary(),
                        satellite.observation_count))
    return "\n".join(lines)
