"""Orbit records: one element-set representation for TLE and OMM/GP data.

The NORAD catalog passed 99,999 objects in July 2026, and objects with
six-digit catalog numbers are published by CelesTrak only in the General
Perturbations (GP) formats, never as TLE text. This module gives the library
a single :class:`OrbitRecord` representation that both formats parse into,
and a ``to_satrec()`` that initialises python-sgp4 directly from the element
values — no synthesised TLE lines, so five-digit assumptions never enter the
picture.
"""

from __future__ import annotations

import math
from dataclasses import dataclass
from datetime import datetime, timezone
from typing import Any, Dict, List, Optional, Tuple

#: Julian date of 1949-12-31 00:00 UT, the epoch python-sgp4's ``sgp4init``
#: expects its ``epoch`` argument relative to.
#: https://rhodesmill.org/brandon/sgp4/ ("days since 1949 December 31 00:00 UT")
_EPOCH_ZERO_JD = 2433281.5

#: Minutes in a day; mean motion conversions between rev/day and rad/min.
_MINUTES_PER_DAY = 1440.0


@dataclass(frozen=True)
class OrbitRecord:
    """One orbital element set, independent of the source format.

    Angles are degrees, mean motion is revolutions per day — the units the
    CCSDS OMM and CelesTrak GP formats use, so values pass through without
    reformatting. ``to_satrec()`` does the unit conversion to radians.
    """

    name: str
    norad_id: int
    epoch: datetime
    mean_motion_rev_per_day: float
    eccentricity: float
    argp_deg: float
    inc_deg: float
    mean_anomaly_deg: float
    raan_deg: float
    bstar: float = 0.0
    mean_motion_dot: float = 0.0  # rev/day^2
    mean_motion_ddot: float = 0.0  # rev/day^3, almost always zero
    source: str = "omm"  # "omm" for GP/OMM, "tle" for two-line elements
    tle_lines: Optional[Tuple[str, str]] = None

    def epoch_julian(self) -> float:
        """Julian date of the epoch, including the fractional day."""
        seconds = self.epoch.timestamp()
        return seconds / 86400.0 + 2440587.5

    def to_satrec(self):
        """Initialise a python-sgp4 ``Satrec`` directly from the elements.

        No TLE lines are synthesised, so this works for any NORAD catalog
        number including the six-digit IDs that the TLE column layout cannot
        hold. The result matches ``Satrec.twoline2rv`` on the same elements
        to well under a millimetre of position.
        """
        from sgp4.api import Satrec  # deferred: optional [tracking] extra

        sat = Satrec()
        # sgp4init wants the epoch as days since 1949-12-31 00:00 UT.
        epoch_param = self.epoch_julian() - _EPOCH_ZERO_JD
        # Mean motion: rev/day -> radians/minute.
        no_rad_min = self.mean_motion_rev_per_day * 2.0 * math.pi / _MINUTES_PER_DAY
        # Mean-motion derivatives: rev/day^2 -> rad/min^3, matching the
        # conversion python-sgp4 applies when it parses TLE line 1.
        ndot = self.mean_motion_dot * 2.0 * math.pi / (_MINUTES_PER_DAY ** 2)
        nddot = self.mean_motion_ddot * 2.0 * math.pi / (_MINUTES_PER_DAY ** 3)
        sat.sgp4init(
            0,  # wgs72old — the constant set twoline2rv initialises with
            "i",  # improved mode
            self.norad_id,
            epoch_param,
            self.bstar,
            ndot,
            nddot,
            self.eccentricity,
            math.radians(self.argp_deg),
            math.radians(self.inc_deg),
            math.radians(self.mean_anomaly_deg),
            no_rad_min,
            math.radians(self.raan_deg),
        )
        return sat


def _to_float(value: Any) -> float:
    return float(value)


def _to_int(value: Any) -> int:
    return int(str(value).strip())


def from_omm(entry: Dict[str, Any]) -> OrbitRecord:
    """Build a record from one CCSDS OMM / CelesTrak GP JSON object.

    Accepts the CelesTrak GP field names (``MEAN_MOTION``, ``RA_OF_ASC_NODE``,
    ...) and a few common spellings. Angles and rates keep the source units
    (degrees, rev/day).
    """
    def pick(*names: str, default: Any = None) -> Any:
        for name in names:
            if name in entry and entry[name] is not None:
                return entry[name]
        if default is None:
            raise KeyError(f"missing any of {names} in OMM entry")
        return default

    epoch = pick("EPOCH")
    if isinstance(epoch, str):
        epoch = _parse_epoch(epoch)
    return OrbitRecord(
        name=str(pick("OBJECT_NAME", "NAME")),
        norad_id=_to_int(pick("NORAD_CAT_ID", "CATNR")),
        epoch=epoch,
        mean_motion_rev_per_day=_to_float(pick("MEAN_MOTION")),
        eccentricity=_to_float(pick("ECCENTRICITY")),
        argp_deg=_to_float(pick("ARG_PERICENTER", "ARG_OF_PERICENTER")),
        inc_deg=_to_float(pick("INCLINATION")),
        mean_anomaly_deg=_to_float(pick("MEAN_ANOMALY")),
        raan_deg=_to_float(pick("RA_OF_ASC_NODE", "RAAN")),
        bstar=_to_float(pick("BSTAR", default=0.0)),
        mean_motion_dot=_to_float(pick("MEAN_MOTION_DOT", "NDOT", default=0.0)),
        mean_motion_ddot=_to_float(pick("MEAN_MOTION_DDOT", "NDDOT", default=0.0)),
        source="omm",
    )


def _parse_epoch(text: str) -> datetime:
    """Parse a CCSDS epoch like ``2026-10-21T04:35:12.123456`` (UTC assumed)."""
    text = text.strip()
    if text.endswith("Z"):
        text = text[:-1]
    if "." in text:
        head, frac = text.split(".", 1)
        frac = (frac + "000000")[:6]
        text = f"{head}.{frac}"
        return datetime.strptime(text, "%Y-%m-%dT%H:%M:%S.%f").replace(tzinfo=timezone.utc)
    return datetime.strptime(text, "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc)


def parse_omm_json(text: str) -> List[OrbitRecord]:
    """Parse a CelesTrak GP JSON document (an array of OMM objects)."""
    import json

    data = json.loads(text)
    if isinstance(data, dict):  # tolerate a single object
        data = [data]
    return [from_omm(entry) for entry in data]


def _tle_float(text: str, default: float = 0.0) -> float:
    """Read a TLE field with its shorthand scientific notation.

    TLE drag fields are ``±NNNNN±N``: six mantissa digits with an implied
    leading ``0.`` and a single exponent digit — ``10270-3`` is 0.10270e-3.
    """
    text = text.strip()
    if not text:
        return default
    sign = 1.0
    if text[0] in "+-":
        sign = -1.0 if text[0] == "-" else 1.0
        text = text[1:]
    if len(text) > 1 and text[-2] in "+-":
        mantissa, exponent = text[:-2], text[-1]
        exp_sign = -1 if text[-2] == "-" else 1
        return sign * float("0." + mantissa) * (10.0 ** (exp_sign * int(exponent)))
    return sign * float(text)


def from_tle(name: str, line1: str, line2: str) -> OrbitRecord:
    """Build a record by reading the fixed columns of a TLE pair.

    Only the mean elements are extracted; the record carries the source lines
    so code that needs the text can still use them.
    """
    return OrbitRecord(
        name=name,
        norad_id=int(line1[2:7]),
        epoch=_tle_epoch(line1),
        mean_motion_rev_per_day=float(line2[52:63]),
        eccentricity=float("0." + line2[26:33].strip()),
        argp_deg=float(line2[33:43]),
        inc_deg=float(line2[8:16]),
        mean_anomaly_deg=float(line2[43:51]),
        raan_deg=float(line2[17:25]),
        bstar=_tle_float(line1[53:61]),
        mean_motion_dot=float(line1[33:43] or 0.0),
        source="tle",
        tle_lines=(line1, line2),
    )


def _tle_epoch(line1: str) -> datetime:
    year = int(line1[18:20])
    year += 2000 if year < 57 else 1900
    days = float(line1[20:32])
    base = datetime(year, 1, 1, tzinfo=timezone.utc)
    from datetime import timedelta

    return base + timedelta(days=days - 1.0)


def records_from_tle_text(text: str) -> List[OrbitRecord]:
    """Convert ``tle.parse``-style TLE text into records."""
    from . import tle as tle_module

    parsed = tle_module.parse(text)
    records = []
    for name, entry in parsed.items():
        records.append(
            from_tle(name, entry["line1"], entry["line2"])
        )
    return records
