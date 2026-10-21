"""Satellite orbit propagation, pass prediction and Doppler correction.

Propagation uses SGP4 via the optional :mod:`sgp4` package. Install it with
``pip install sdrstream[tracking]``. The module is importable without it; only
the functions that need propagation fail, and they say so clearly.

Coordinates follow the usual conventions: latitude and longitude in degrees
(east and north positive), altitude in kilometres above the WGS-84 ellipsoid.
"""

from __future__ import annotations

import math
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from typing import Dict, List, Optional, Tuple

try:  # pragma: no cover - exercised only when the extra is installed
    from sgp4.api import Satrec, jday

    HAVE_SGP4 = True
except ImportError:  # pragma: no cover
    HAVE_SGP4 = False
    Satrec = None  # type: ignore[assignment]
    jday = None  # type: ignore[assignment]

#: Speed of light in m/s.
C = 299_792_458.0

#: WGS-84 ellipsoid radii, in kilometres.
EARTH_EQUATORIAL_KM = 6378.137
EARTH_FLATTENING = 1.0 / 298.257223563

#: Mean Earth rotation rate, rad/s (sidereal).
EARTH_ROTATION_RATE = 7.2921150e-5

#: Frequency of the observer's 10 MHz reference would be affected by gravity;
#: for amateur work only the relative motion matters.
DEG = math.pi / 180.0


def _require_sgp4() -> None:
    if not HAVE_SGP4:
        raise RuntimeError(
            "satellite tracking needs the 'sgp4' package; install it with "
            "'pip install sdrstream[tracking]'"
        )


def gmst(dt: datetime) -> float:
    """Greenwich mean sidereal time, in radians.

    Uses the standard IAU 1982 polynomial, accurate to well under a second of
    time, which is far better than the TLEs themselves.
    """
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    cached = _GMST_CACHE.get(dt)
    if cached is not None:
        return cached
    jd = _julian_date(dt)
    t = (jd - 2451545.0) / 36525.0
    theta = (
        280.46061837
        + 360.98564736629 * (jd - 2451545.0)
        + 0.000387933 * t * t
        - t * t * t / 38710000.0
    )
    value = (theta % 360.0) * DEG
    if len(_GMST_CACHE) >= _GMST_CACHE_LIMIT:
        _GMST_CACHE.clear()
    _GMST_CACHE[dt] = value
    return value


#: Sidereal time is a pure function of the instant, and a pass scan revisits the
#: same instants once per receiver, so caching it is worth the small dict.
_GMST_CACHE: Dict[datetime, float] = {}
_GMST_CACHE_LIMIT = 65536


def _julian_date(dt: datetime) -> float:
    """Julian date for a datetime, including the fractional day."""
    y, m = dt.year, dt.month
    d = dt.day
    if m <= 2:
        y -= 1
        m += 12
    a = y // 100
    b = 2 - a + a // 4
    day = (
        d
        + (dt.hour + (dt.minute + (dt.second + dt.microsecond / 1e6) / 60.0) / 60.0)
        / 24.0
    )
    return (
        math.floor(365.25 * (y + 4716))
        + math.floor(30.6001 * (m + 1))
        + day
        + b
        - 1524.5
    )


def eci_to_geodetic(x: float, y: float, z: float, dt: datetime
                    ) -> Tuple[float, float, float]:
    """Convert TEME/ECI kilometres to latitude, longitude and altitude.

    Latitude and longitude are degrees, altitude is kilometres. The conversion
    iterates the geodetic latitude, which converges in a couple of steps at
    orbital altitudes.
    """
    lon = (math.atan2(y, x) - gmst(dt)) % (2 * math.pi)
    if lon > math.pi:
        lon -= 2 * math.pi
    r = math.hypot(x, y)
    lat = math.atan2(z, r)
    e2 = EARTH_FLATTENING * (2 - EARTH_FLATTENING)
    for _ in range(6):
        sin_lat = math.sin(lat)
        n = EARTH_EQUATORIAL_KM / math.sqrt(1 - e2 * sin_lat * sin_lat)
        alt = r / math.cos(lat) - n
        lat = math.atan2(z, r * (1 - e2 * n / (n + alt)))
    sin_lat = math.sin(lat)
    n = EARTH_EQUATORIAL_KM / math.sqrt(1 - e2 * sin_lat * sin_lat)
    alt = r / math.cos(lat) - n
    return lat / DEG, lon / DEG, alt


def geodetic_to_eci(lat: float, lon: float, alt_km: float, dt: datetime
                    ) -> Tuple[float, float, float]:
    """Observer position in ECI kilometres, the inverse of :func:`eci_to_geodetic`."""
    e2 = EARTH_FLATTENING * (2 - EARTH_FLATTENING)
    lat_r = lat * DEG
    sin_lat = math.sin(lat_r)
    n = EARTH_EQUATORIAL_KM / math.sqrt(1 - e2 * sin_lat * sin_lat)
    x = (n + alt_km) * math.cos(lat_r)
    z = (n * (1 - e2) + alt_km) * sin_lat
    theta = lon * DEG + gmst(dt)
    return x * math.cos(theta), x * math.sin(theta), z


@dataclass
class LookAngle:
    """Where a satellite is, as seen from one place on the ground."""

    time: datetime
    azimuth: float
    elevation: float
    range_km: float
    range_rate_km_s: float
    latitude: float
    longitude: float
    altitude_km: float

    @property
    def visible(self) -> bool:
        return self.elevation > 0.0

    def doppler_shift(self, frequency_hz: float) -> float:
        """Received-frequency offset in Hz for a signal at *frequency_hz*.

        Positive means the satellite is approaching, so the received frequency
        is higher than the transmitted one.
        """
        return -self.range_rate_km_s * 1000.0 / C * frequency_hz

    def corrected_frequency(self, frequency_hz: float) -> float:
        """Tune here to hear a signal transmitted at *frequency_hz*."""
        return frequency_hz + self.doppler_shift(frequency_hz)


class Tracker:
    """Propagates one satellite and predicts passes for one observer."""

    def __init__(self, tle_line1: str, tle_line2: str):
        _require_sgp4()
        self.satellite = Satrec.twoline2rv(tle_line1, tle_line2)
        self.tle_line1 = tle_line1
        self.tle_line2 = tle_line2
        # SGP4 propagation depends only on the orbit, not the observer, so the
        # same times recur for every receiver a pass is predicted for. Caching
        # the ECI position collapses that repeated work; the cap keeps a long
        # scan from growing without bound.
        self._position_cache: Dict[Tuple[float, float], Tuple[float, float, float]] = {}
        self._position_cache_limit = 8192

    @classmethod
    def from_record(cls, record) -> "Tracker":
        """Build a tracker from an :class:`~sdrstream.orbit.OrbitRecord`.

        The record is initialised straight into python-sgp4, so satellites
        whose catalog numbers do not fit the TLE layout (the six-digit IDs
        CelesTrak publishes only as GP JSON) can be tracked too.
        """
        _require_sgp4()
        tracker = cls.__new__(cls)
        tracker.satellite = record.to_satrec()
        tracker.tle_line1 = record.tle_lines[0] if record.tle_lines else ""
        tracker.tle_line2 = record.tle_lines[1] if record.tle_lines else ""
        tracker.orbit_record = record
        tracker._position_cache = {}
        tracker._position_cache_limit = 8192
        return tracker

    @classmethod
    def from_tle_text(cls, text: str, name: str) -> "Tracker":
        """Build a tracker from a TLE block, matching *name* case-insensitively.

        *text* may hold one TLE or a whole file of them, as served by Celestrak.
        """
        lines = [line.rstrip() for line in text.splitlines() if line.strip()]
        want = name.strip().upper()
        for i in range(len(lines) - 2):
            if want in lines[i].upper() and lines[i + 1].startswith("1 "):
                return cls(lines[i + 1], lines[i + 2])
        raise ValueError("no TLE found for %r" % name)

    @classmethod
    def from_records(cls, records, name: str) -> "Tracker":
        """Build a tracker from a list of records, matching *name* loosely.

        Mirrors :meth:`from_tle_text`'s partial, case-insensitive matching.
        """
        want = name.strip().upper()
        for record in records:
            if want in record.name.upper():
                return cls.from_record(record)
        raise ValueError("no element record found for %r" % name)

    def propagate(self, dt: datetime) -> Tuple[float, float, float]:
        """Position in ECI kilometres at *dt*."""
        if dt.tzinfo is None:
            dt = dt.replace(tzinfo=timezone.utc)
        jd, fr = jday(
            dt.year, dt.month, dt.day, dt.hour, dt.minute,
            dt.second + dt.microsecond / 1e6,
        )
        key = (jd, fr)
        cached = self._position_cache.get(key)
        if cached is not None:
            return cached
        error, position, _velocity = self.satellite.sgp4(jd, fr)
        if error != 0:
            raise RuntimeError("SGP4 propagation failed with code %d" % error)
        if len(self._position_cache) >= self._position_cache_limit:
            self._position_cache.clear()
        self._position_cache[key] = position
        return position

    def look_angle(self, dt: datetime, observer_lat: float,
                   observer_lon: float, observer_alt_km: float = 0.0
                   ) -> LookAngle:
        """Azimuth, elevation, range and range rate at *dt*."""
        position = self.propagate(dt)
        sat_geodetic = eci_to_geodetic(position[0], position[1], position[2], dt)
        return self._look_from(position, sat_geodetic, dt, observer_lat,
                               observer_lon, observer_alt_km)

    def _look_from(self, position, sat_geodetic, dt, observer_lat,
                   observer_lon, observer_alt_km) -> LookAngle:
        """Look angle from an already-propagated ECI position.

        Split out so a caller with the position in hand skips re-propagating
        it, which is what makes a multi-receiver sweep cheap.
        """
        obs = geodetic_to_eci(observer_lat, observer_lon, observer_alt_km, dt)
        topocentric = [position[i] - obs[i] for i in range(3)]
        return self._topocentric_to_look(
            topocentric, dt, observer_lat, observer_lon, observer_alt_km,
            sat_geodetic,
        )

    def _topocentric_to_look(self, topocentric, dt, obs_lat, obs_lon, obs_alt,
                             sat_geodetic) -> LookAngle:
        lat_r = obs_lat * DEG
        theta = obs_lon * DEG + gmst(dt)
        sin_lat, cos_lat = math.sin(lat_r), math.cos(lat_r)
        sin_th, cos_th = math.sin(theta), math.cos(theta)
        # Local east/north/up basis, dotted with the topocentric vector.
        east = (-sin_th, cos_th, 0.0)
        north = (-sin_lat * cos_th, -sin_lat * sin_th, cos_lat)
        up = (cos_lat * cos_th, cos_lat * sin_th, sin_lat)
        e = sum(east[i] * topocentric[i] for i in range(3))
        n = sum(north[i] * topocentric[i] for i in range(3))
        u = sum(up[i] * topocentric[i] for i in range(3))
        rng = math.sqrt(e * e + n * n + u * u)
        az = math.atan2(e, n) % (2 * math.pi)
        el = math.asin(max(-1.0, min(1.0, u / rng))) if rng else 0.0

        # Range rate by finite difference, which is accurate and simple.
        step = 1.0
        ahead = self.propagate(dt + timedelta(seconds=step))
        behind = self.propagate(dt - timedelta(seconds=step))
        obs_a = geodetic_to_eci(obs_lat, obs_lon, obs_alt, dt + timedelta(seconds=step))
        obs_b = geodetic_to_eci(obs_lat, obs_lon, obs_alt, dt - timedelta(seconds=step))
        ra = math.dist(ahead, obs_a)
        rb = math.dist(behind, obs_b)
        range_rate = (ra - rb) / (2 * step)

        return LookAngle(
            time=dt,
            azimuth=az / DEG,
            elevation=el / DEG,
            range_km=rng,
            range_rate_km_s=range_rate,
            latitude=sat_geodetic[0],
            longitude=sat_geodetic[1],
            altitude_km=sat_geodetic[2],
        )

    def find_passes(self, observer_lat: float, observer_lon: float,
                    start: Optional[datetime] = None, hours: float = 24.0,
                    min_elevation: float = 5.0, observer_alt_km: float = 0.0,
                    step_seconds: float = 30.0) -> List["Pass"]:
        """Find passes above *min_elevation* over the next *hours*.

        The scan uses a coarse time step and then refines the rise, peak and set
        times, which keeps a full day of prediction to a few hundred SGP4 calls.
        """
        return self.find_passes_multi(
            [(observer_lat, observer_lon, observer_alt_km)],
            start=start, hours=hours, min_elevation=min_elevation,
            step_seconds=step_seconds,
        )[0]

    def find_passes_multi(self, observers, start: Optional[datetime] = None,
                          hours: float = 24.0, min_elevation: float = 5.0,
                          step_seconds: float = 30.0
                          ) -> List[List["Pass"]]:
        """Passes for many observers in one sweep, one list per observer.

        *observers* is a sequence of ``(lat, lon)`` or ``(lat, lon, alt_km)``.
        The orbit does not depend on the observer, so propagating once per time
        step and reusing that position for every observer turns an N-receiver
        scan from N SGP4 runs per step into one. Each result list is in the
        same order as *observers*.
        """
        start = start or datetime.now(timezone.utc)
        if start.tzinfo is None:
            start = start.replace(tzinfo=timezone.utc)
        end = start + timedelta(hours=hours)

        obs = [tuple(o) if len(o) == 3 else (o[0], o[1], 0.0) for o in observers]
        results: List[List[Pass]] = [[] for _ in obs]
        rise_times: List[Optional[datetime]] = [None] * len(obs)
        sample_sets: List[List[LookAngle]] = [[] for _ in obs]

        t = start
        while t <= end:
            position = self.propagate(t)
            sat_geodetic = eci_to_geodetic(position[0], position[1],
                                           position[2], t)
            previous = t - timedelta(seconds=step_seconds)
            for i, (lat, lon, alt) in enumerate(obs):
                current = self._look_from(position, sat_geodetic, t, lat, lon, alt)
                if current.visible and rise_times[i] is None:
                    rise_times[i] = self._refine_crossing(
                        lat, lon, alt, previous, t, rising=True)
                    sample_sets[i] = []
                if current.visible:
                    sample_sets[i].append(current)
                elif rise_times[i] is not None:
                    set_time = self._refine_crossing(
                        lat, lon, alt, previous, t, rising=False)
                    peak = self._refine_peak(lat, lon, alt, sample_sets[i])
                    if peak.elevation >= min_elevation:
                        results[i].append(Pass(
                            rise=rise_times[i],
                            peak=peak,
                            set_time=set_time,
                            duration=(set_time - rise_times[i]).total_seconds(),
                        ))
                    rise_times[i] = None
                    sample_sets[i] = []
            t += timedelta(seconds=step_seconds)
        return results

    def _refine_peak(self, lat, lon, alt, samples: List[LookAngle],
                     iterations: int = 25) -> LookAngle:
        """Return the highest point of a pass.

        A near-zenith pass climbs several degrees per sampling step, so the
        tallest sample can fall well short of the real peak. Elevation is
        unimodal across a single pass, so a ternary search between the samples
        either side of the coarse maximum finds the true apex.
        """
        best = max(range(len(samples)), key=lambda i: samples[i].elevation)
        lo = samples[best - 1].time if best > 0 else samples[best].time
        hi = (samples[best + 1].time if best + 1 < len(samples)
              else samples[best].time)
        if lo >= hi:
            return samples[best]
        for _ in range(iterations):
            a = lo + (hi - lo) / 3
            b = hi - (hi - lo) / 3
            if (self.look_angle(a, lat, lon, alt).elevation
                    < self.look_angle(b, lat, lon, alt).elevation):
                lo = a
            else:
                hi = b
        return self.look_angle(lo + (hi - lo) / 2, lat, lon, alt)

    def _refine_crossing(self, lat, lon, alt, before, after, rising: bool,
                         iterations: int = 12) -> datetime:
        """Bisect between *before* and *after* to find the horizon crossing."""
        lo, hi = before, after
        for _ in range(iterations):
            mid = lo + (hi - lo) / 2
            look = self.look_angle(mid, lat, lon, alt)
            above = look.visible
            if above == rising:
                hi = mid
            else:
                lo = mid
        return lo + (hi - lo) / 2


@dataclass
class Pass:
    """A single pass of a satellite over an observer."""

    rise: datetime
    peak: LookAngle
    set_time: datetime
    duration: float

    @property
    def max_elevation(self) -> float:
        return self.peak.elevation

    def summary(self) -> str:
        return (
            "%s rise  peak %5.1f deg at %s  set  duration %4.1f min"
            % (
                self.rise.strftime("%H:%M:%S"),
                self.max_elevation,
                self.peak.time.strftime("%H:%M:%S"),
                self.duration / 60.0,
            )
        )
