"""Dated satellite transmission events (mirror of go/internal/satellites/events.go).

Event entries temporarily override a satellite's usual downlink plan and
are added only from current, dated source evidence; remove them when they
expire. The Go implementation is authoritative.
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime, timezone
from typing import Optional


@dataclass
class Event:
    satellite: str
    name: str
    start_utc: str
    end_utc: str
    downlink_hz: int
    mode: str
    image_mode: str
    callsign: str
    source: str
    notes: str


EVENTS = [
    Event(
        satellite="ISS",
        name="ARISS SSTV Series 34 (ARISS Hardware)",
        start_utc="2026-10-22T15:30:00Z",
        end_utc="2026-10-26T17:45:00Z",
        downlink_hz=437_550_000,
        mode="FM",
        image_mode="Robot36",
        callsign="RS0ISS",
        # End resolved from the owning source 2026-10-23: the ARISS
        # announcement (@arissintl.bsky.social, 2026-10-14, verbatim
        # republication via sbarc.co.uk) states Start 15:30 UTC 22/10,
        # End 17:45 UTC 26/10; its 2026-10-21 update EXTENDED the end.
        # Secondary "Oct 22-25" renderings are lossy summaries, not a
        # disagreement. Dates remain subject to ISS operational changes.
        source=(
            "Owning source: ARISS @arissintl.bsky.social 2026-10-14 post, verbatim republication "
            "https://www.sbarc.co.uk/news/expedition-75-series-34-sstv-from-the-iss/ "
            "(updates 2026-10-20 confirmed, 2026-10-21 end extended to Oct 26 17:45 UTC). "
            "Corroborated by darc.de 2026-10-20, darc-e39.org 2026-10-21, f6kmf.fr 2026-10-15. "
            "Verified 2026-10-23."
        ),
        notes=(
            "36 s on, 2 min off; allow +/-10 kHz Doppler. After the event the Service Module "
            "transceiver stays on 437.825 MHz APRS until end of 2026. Series 34 uses 70cm, not "
            "the legacy 145.800 MHz downlink."
        ),
    ),
]


def _parse_when(text: str) -> Optional[datetime]:
    try:
        return datetime.fromisoformat(text.replace("Z", "+00:00"))
    except ValueError:
        return None


def active_event(satellite: str, at: Optional[datetime] = None) -> Optional[Event]:
    """The dated event overriding the satellite's downlink plan at ``at``
    (UTC, default now), or None. Bounds are inclusive; entries whose dates
    do not parse are skipped rather than trusted."""
    at = at or datetime.now(timezone.utc)
    want = satellite.strip().upper()
    for ev in EVENTS:
        if ev.satellite.strip().upper() != want:
            continue
        start, end = _parse_when(ev.start_utc), _parse_when(ev.end_utc)
        if start is None or end is None:
            continue
        if start <= at <= end:
            return ev
    return None
