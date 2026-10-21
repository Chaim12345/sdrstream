"""Space-weather context from the NOAA Space Weather Prediction Center.

The SWPC products are free JSON, no key, no rate games — the same numbers
HF operators plan around. ``space_weather()`` gathers the current
planetary K index (geomagnetic disturbance), the 10.7 cm solar flux (the
F-layer ionisation proxy), the GOES X-ray flux (sudden ionospheric
disturbances) and the current sunspot regions, tolerating individual
product failures so one down endpoint never hides the rest.
"""

from __future__ import annotations

import json
import urllib.request
from dataclasses import dataclass, field
from datetime import datetime, timezone
from typing import List, Optional

SWPC = "https://services.swpc.noaa.gov/json"
USER_AGENT = "sdrstream/1.0 (satellite and SDR research)"


@dataclass
class SunspotRegion:
    """One numbered sunspot region as SWPC reports it."""

    number: int
    location: str = ""
    # SWPC reports locations like "S17W70"; derive a friendly bearing.
    latitude_deg: Optional[float] = None
    longitude_deg: Optional[float] = None

    def summary(self) -> str:
        return "region %-5d %s" % (self.number, self.location or "?")


@dataclass
class SpaceWeather:
    """The current space weather, as much of it as could be fetched."""

    kp: Optional[float] = None
    kp_time: Optional[datetime] = None
    flux_107cm: Optional[float] = None
    flux_time: Optional[datetime] = None
    xray_flux: Optional[float] = None
    xray_time: Optional[datetime] = None
    sunspot_regions: List[SunspotRegion] = field(default_factory=list)
    fetched_at: datetime = field(
        default_factory=lambda: datetime.now(timezone.utc))

    def xray_class(self) -> str:
        """The X-ray class letter and number, e.g. ``B5.7``.

        The GOES classes are decades in W/m^2: A1.0 is 1e-8, B1.0 is
        1e-7, C1.0 is 1e-6, M1.0 is 1e-5 and X1.0 is 1e-4.
        """
        if self.xray_flux is None:
            return "?"
        thresholds = [("X", 1e-4), ("M", 1e-5), ("C", 1e-6), ("B", 1e-7)]
        for letter, floor in thresholds:
            if self.xray_flux >= floor:
                return "%s%.1f" % (letter, self.xray_flux / floor)
        return "A%.1f" % (self.xray_flux / 1e-8)

    def verdict(self) -> str:
        """One line on what this means for HF and satellite work."""
        notes = []
        if self.kp is not None:
            notes.append(
                "HF conditions " + ("quiet" if self.kp < 4 else
                                    "disturbed") + " (Kp %.1f)" % self.kp)
        if self.flux_107cm is not None:
            notes.append(
                "%.0f sfu 10.7cm flux — %s HF absorption"
                % (self.flux_107cm,
                   "high" if self.flux_107cm > 150 else "normal"))
        if self.xray_flux is not None:
            notes.append("X-ray class %s" % self.xray_class())
        return "; ".join(notes) if notes else "no products available"

    def summary(self) -> str:
        lines = ["space weather %s (SWPC):" % self.fetched_at.strftime("%Y-%m-%d %H:%M UTC")]
        when = self.kp_time.strftime("%Y-%m-%d %H:%M") if self.kp_time else "?"
        lines.append("  planetary Kp      %.1f  (as of %s)" % (self.kp if self.kp is not None else float("nan"), when))
        when = self.flux_time.strftime("%Y-%m-%d %H:%M") if self.flux_time else "?"
        lines.append("  10.7 cm flux     %.0f sfu  (as of %s)" % (self.flux_107cm if self.flux_107cm is not None else float("nan"), when))
        when = self.xray_time.strftime("%Y-%m-%d %H:%M") if self.xray_time else "?"
        lines.append("  GOES X-ray       %.2e W/m^2  class %s  (as of %s)"
                    % (self.xray_flux or float("nan"), self.xray_class(), when))
        if self.sunspot_regions:
            lines.append("  sunspot regions   %d, e.g. %s"
                         % (len(self.sunspot_regions),
                            ", ".join(r.summary() for r in self.sunspot_regions[:3])))
        lines.append("  " + self.verdict())
        return "\n".join(lines)


def space_weather(timeout: float = 30.0) -> SpaceWeather:
    """Fetch the current space weather from SWPC.

    Each product is fetched independently; a failure leaves that field
    empty rather than failing the whole report.
    """
    report = SpaceWeather()

    kp = _last("%s/planetary_k_index_1m.json" % SWPC, timeout)
    if kp is not None:
        report.kp = _as_float(kp.get("kp_index"))
        report.kp_time = _as_time(kp.get("time_tag"))

    flux = _last("%s/f107_cm_flux.json" % SWPC, timeout)
    if flux is not None:
        report.flux_107cm = _as_float(flux.get("flux"))
        report.flux_time = _as_time(flux.get("time_tag"))

    xray = _last("%s/goes/primary/xrays-1-day.json" % SWPC, timeout)
    if xray is not None:
        # Prefer the long-channel flux used for the class; fall back to any.
        report.xray_flux = _as_float(xray.get("flux")
                                     or xray.get("observed_flux"))
        report.xray_time = _as_time(xray.get("time_tag"))

    regions = _get("%s/sunspot_report.json" % SWPC, timeout)
    if isinstance(regions, list):
        seen = set()
        for entry in regions:
            number = entry.get("Region")
            if number is None or number in seen:
                continue
            seen.add(number)
            report.sunspot_regions.append(
                SunspotRegion(number=number,
                              location=entry.get("Location") or "",
                              latitude_deg=_as_float(entry.get("Latitude")),
                              longitude_deg=_as_float(entry.get("Longitude"))))
    return report


def _get(url: str, timeout: float):
    request = urllib.request.Request(url, headers={"User-Agent": USER_AGENT})
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            return json.loads(response.read().decode("utf-8", "replace"))
    except Exception:
        return None


def _last(url: str, timeout: float):
    """The newest row of a time-series product.

    The rows are not guaranteed to arrive in order — the flux product keeps
    trailing average rows at the end — so the newest time tag wins instead
    of the list's last element.
    """
    payload = _get(url, timeout)
    if isinstance(payload, list) and payload:
        rows = [row for row in payload if isinstance(row, dict) and row.get("time_tag")]
        if rows:
            return max(rows, key=lambda row: row["time_tag"])
    return None


def _as_float(value) -> Optional[float]:
    try:
        return float(value)
    except (TypeError, ValueError):
        return None


def _as_time(value) -> Optional[datetime]:
    if not isinstance(value, str):
        return None
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        return None
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=timezone.utc)
    return parsed
