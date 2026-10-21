"""Read the public SatNOGS network for real satellite downlink data.

SatNOGS is a crowd-sourced network of ground stations. Its API is the most
reliable public source of *observed* amateur downlinks: each observation
records the transmitter frequency and mode that a station actually used, the
station's coordinates, and a link to the captured audio. That is a useful
cross-check on the curated table in :mod:`sdrstream.satellites`, which can only
say what a satellite is *supposed* to transmit.

Only the public, unauthenticated endpoints are used, and they are rate limited
(60 observation requests an hour), so responses are cached on disk.
"""

from __future__ import annotations

import json
import os
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any, Dict, List, Optional

NETWORK_API = "https://network.satnogs.org/api"
DB_API = "https://db.satnogs.org/api"
USER_AGENT = "sdrstream/0.1 (+satellite)"

DEFAULT_CACHE = Path(
    os.environ.get("SDRSTREAM_CACHE", Path.home() / ".cache" / "sdrstream")
)


@dataclass
class Observation:
    """One ground station's recording of one satellite."""

    id: int
    satellite: str
    norad_id: int
    station_name: str
    latitude: float
    longitude: float
    start: Optional[datetime]
    end: Optional[datetime]
    max_elevation: float
    frequency_hz: Optional[int]
    mode: str
    status: str
    waterfall_url: Optional[str] = None
    demod_url: Optional[str] = None
    tle_line1: Optional[str] = None
    tle_line2: Optional[str] = None

    @property
    def duration(self) -> float:
        if not (self.start and self.end):
            return 0.0
        return (self.end - self.start).total_seconds()

    def summary(self) -> str:
        return (
            "obs %-9d %-24s %-22s el %4.1f deg  %8.4f MHz %-10s %s"
            % (self.id, self.satellite[:24], self.station_name[:22],
               self.max_elevation,
               (self.frequency_hz or 0) / 1e6, self.mode, self.status)
        )


def _get(url: str, timeout: float = 30.0, attempts: int = 3) -> Any:
    request = urllib.request.Request(url, headers={"User-Agent": USER_AGENT})
    last_error: Optional[Exception] = None
    for attempt in range(attempts):
        try:
            with urllib.request.urlopen(request, timeout=timeout) as response:
                return json.loads(response.read().decode("utf-8", "replace"))
        except (urllib.error.URLError, OSError, ValueError) as error:
            # The network endpoint is busy and sometimes stalls mid-handshake;
            # retrying turns that into a slow call instead of a hard failure.
            last_error = error
            if attempt + 1 < attempts:
                time.sleep(1.0 + attempt)
    raise last_error  # type: ignore[misc]


def _parse_time(value: Optional[str]) -> Optional[datetime]:
    if not value:
        return None
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        return None
    return parsed if parsed.tzinfo else parsed.replace(tzinfo=timezone.utc)


def _observation(raw: Dict[str, Any]) -> Observation:
    transmitter = raw.get("transmitter") or {}
    demod = raw.get("demoddata") or []
    return Observation(
        id=raw.get("id", 0),
        satellite=raw.get("sat_id") or str(raw.get("norad_cat_id", "")),
        norad_id=raw.get("norad_cat_id", 0),
        station_name=raw.get("station_name", ""),
        latitude=raw.get("station_lat", 0.0) or 0.0,
        longitude=raw.get("station_lng", 0.0) or 0.0,
        start=_parse_time(raw.get("start")),
        end=_parse_time(raw.get("end")),
        max_elevation=raw.get("max_altitude", 0.0) or 0.0,
        frequency_hz=(raw.get("observation_frequency")
                      or transmitter.get("downlink_low")),
        mode=(raw.get("transmitter_mode")
              or transmitter.get("mode") or ""),
        status=raw.get("status", ""),
        waterfall_url=raw.get("waterfall"),
        demod_url=demod[0].get("payload_demod") if demod else None,
        tle_line1=raw.get("tle1"),
        tle_line2=raw.get("tle2"),
    )


def observations(norad_id: Optional[int] = None, status: str = "good",
                 limit: int = 25, hours: Optional[float] = None,
                 timeout: float = 30.0) -> List[Observation]:
    """Fetch recent observations, newest first.

    *norad_id* filters to one satellite, *status* to SatNOGS' own verdict
    (``"good"``, ``"bad"``, ``"unknown"`` or ``"future"``), and *hours* to
    observations that started within that many hours of now.
    """
    query = {"format": "json", "limit": min(limit, 100)}
    if norad_id is not None:
        query["norad_cat_id"] = norad_id
    if status:
        query["status"] = status
    if hours is not None:
        since = datetime.now(timezone.utc) - timedelta(hours=hours)
        query["start__gte"] = since.strftime("%Y-%m-%dT%H:%M:%SZ")
    raw = _get("%s/observations/?%s" % (NETWORK_API, urllib.parse.urlencode(query)),
               timeout=timeout)
    if not isinstance(raw, list):
        raise ValueError("unexpected SatNOGS response: %r" % type(raw))
    return [_observation(item) for item in raw]


def _transmitter_table(timeout: float = 60.0,
                       cache_dir: Optional[Path] = None) -> List[Dict[str, Any]]:
    """Download the whole transmitter table, cached for a day.

    The DB API ignores the ``norad_cat_id`` filter and returns every record,
    so there is no point asking for one satellite: fetch the table once and
    filter it here.
    """
    directory = cache_dir or DEFAULT_CACHE
    directory.mkdir(parents=True, exist_ok=True)
    path = directory / "satnogs_transmitters.json"
    if path.exists() and (time.time() - path.stat().st_mtime) < 24 * 3600:
        return json.loads(path.read_text())
    try:
        raw = _get("%s/transmitters/?format=json" % DB_API, timeout=timeout)
    except (urllib.error.URLError, ValueError):
        if not path.exists():
            raise
        return json.loads(path.read_text())
    if not isinstance(raw, list):
        raise ValueError("unexpected SatNOGS transmitter response")
    path.write_text(json.dumps(raw))
    return raw


def transmitter(norad_id: int, timeout: float = 60.0) -> List[Dict[str, Any]]:
    """Return the transmitter records SatNOGS has for a satellite."""
    return [record for record in _transmitter_table(timeout=timeout)
            if record.get("norad_cat_id") == norad_id]


def best_downlink(norad_id: int, timeout: float = 60.0) -> Optional[Dict[str, Any]]:
    """Pick the most likely downlink from a satellite's transmitters.

    Prefers a live transmitter in an amateur band, since a satellite often has
    an S-band or experimental beacon that no public receiver can hear.
    """
    records = transmitter(norad_id, timeout=timeout)
    if not records:
        return None

    def in_amateur_band(record: Dict[str, Any]) -> bool:
        low = record.get("downlink_low") or 0
        return any(low_band <= low <= high_band for low_band, high_band in (
            (28_000_000, 29_700_000), (144_000_000, 148_000_000),
            (430_000_000, 440_000_000), (1_240_000_000, 1_300_000_000),
        ))

    def rank(record: Dict[str, Any]):
        alive = 1 if record.get("alive") or record.get("status") == "active" else 0
        return (alive, 1 if in_amateur_band(record) else 0,
                record.get("success_rate", 0) or 0,
                record.get("observation_count", 0) or 0)

    return max(records, key=rank)


def cached_observations(norad_id: int, limit: int = 25,
                        max_age_hours: float = 6.0,
                        cache_dir: Optional[Path] = None) -> List[Observation]:
    """Observations for a satellite, cached because the API is rate limited."""
    directory = cache_dir or DEFAULT_CACHE
    directory.mkdir(parents=True, exist_ok=True)
    path = directory / ("satnogs_%d.json" % norad_id)
    if path.exists() and (time.time() - path.stat().st_mtime) < max_age_hours * 3600:
        payload = json.loads(path.read_text())
    else:
        try:
            found = observations(norad_id=norad_id, limit=limit)
            payload = [
                {
                    "id": o.id, "satellite": o.satellite, "norad_id": o.norad_id,
                    "station_name": o.station_name, "latitude": o.latitude,
                    "longitude": o.longitude,
                    "start": o.start.isoformat() if o.start else None,
                    "end": o.end.isoformat() if o.end else None,
                    "max_elevation": o.max_elevation,
                    "frequency_hz": o.frequency_hz, "mode": o.mode,
                    "status": o.status, "waterfall_url": o.waterfall_url,
                    "demod_url": o.demod_url, "tle_line1": o.tle_line1,
                    "tle_line2": o.tle_line2,
                }
                for o in found
            ]
            path.write_text(json.dumps(payload, indent=1))
        except (urllib.error.URLError, ValueError):
            if not path.exists():
                raise
            payload = json.loads(path.read_text())
    return [
        Observation(
            id=item["id"], satellite=item["satellite"], norad_id=item["norad_id"],
            station_name=item["station_name"], latitude=item["latitude"],
            longitude=item["longitude"], start=_parse_time(item["start"]),
            end=_parse_time(item["end"]), max_elevation=item["max_elevation"],
            frequency_hz=item["frequency_hz"], mode=item["mode"],
            status=item["status"], waterfall_url=item["waterfall_url"],
            demod_url=item["demod_url"], tle_line1=item["tle_line1"],
            tle_line2=item["tle_line2"],
        )
        for item in payload
    ]


def download(url: str, out_path, timeout: float = 120.0,
             chunk_size: int = 64 * 1024) -> str:
    """Save a SatNOGS artifact (audio, waterfall) to a local file.

    Returns the path written. Artifacts are plain files behind the API, so this
    is a streamed copy rather than anything protocol-specific.
    """
    request = urllib.request.Request(url, headers={"User-Agent": USER_AGENT})
    destination = Path(out_path)
    destination.parent.mkdir(parents=True, exist_ok=True)
    with urllib.request.urlopen(request, timeout=timeout) as response:
        with open(destination, "wb") as handle:
            while True:
                block = response.read(chunk_size)
                if not block:
                    break
                handle.write(block)
    return str(destination)


def observed_frequencies(norad_id: int, **kwargs) -> Dict[int, int]:
    """Count observations per downlink frequency, most observed first."""
    counts: Dict[int, int] = {}
    for observation in cached_observations(norad_id, **kwargs):
        if observation.frequency_hz:
            counts[observation.frequency_hz] = counts.get(observation.frequency_hz, 0) + 1
    return dict(sorted(counts.items(), key=lambda kv: -kv[1]))


@dataclass
class TelemetryFrame:
    """One raw frame a SatNOGS station submitted to the DB.

    ``frame`` is the payload as the DB stores it (typically a hex string),
    ``decoded`` the decoder's structured output when the frame was decoded.
    """

    id: int
    sat_id: str
    norad_id: int
    transmitter: str
    frame: Optional[str] = None
    decoded: Optional[str] = None
    observation_id: Optional[int] = None
    station_id: Optional[int] = None
    timestamp: Optional[datetime] = None

    def summary(self) -> str:
        when = self.timestamp.strftime("%Y-%m-%d %H:%M:%S") if self.timestamp else "?"
        size = len(self.frame or "") // 2 if self.frame else 0
        return ("frame %-8d NORAD %-6d obs %-9d st %-5d %s  %3d bytes%s"
                % (self.id, self.norad_id, self.observation_id or 0,
                   self.station_id or 0, when, size,
                   "  (decoded)" if self.decoded else ""))


def api_token(explicit: Optional[str] = None) -> Optional[str]:
    """The SatNOGS API token: an explicit value, else ``SATNOGS_TOKEN``.

    The token is never written to the cache or logs; it is only sent as a
    request header. Generate one from your profile page on satnogs.org.
    """
    if explicit:
        return explicit
    return os.environ.get("SATNOGS_TOKEN") or None


def telemetry_frames(
    norad_id: Optional[int] = None,
    *,
    satellite: Optional[str] = None,
    transmitter: Optional[str] = None,
    start: Optional[datetime] = None,
    end: Optional[datetime] = None,
    limit: int = 100,
    token: Optional[str] = None,
    timeout: float = 30.0,
) -> List[TelemetryFrame]:
    """Fetch submitted telemetry frames from the SatNOGS DB.

    Unlike the observation endpoints, ``/api/telemetry/`` requires an account
    token (anonymous requests are rejected with an authentication error), so
    this needs ``token`` or ``SATNOGS_TOKEN`` in the environment. The frames
    are the community's raw captures — the deepest pool of amateur-satellite
    telemetry that exists — filtered server-side by satellite, transmitter or
    observation window. Results are newest first.

    The endpoint is cursor-paginated; *limit* pages through it up to that
    many frames (capped at 500 to stay polite).
    """
    resolved = api_token(token)
    if not resolved:
        raise RuntimeError(
            "the SatNOGS telemetry endpoint needs an API token: pass one "
            "or set SATNOGS_TOKEN (profile page on satnogs.org)"
        )
    query: Dict[str, Any] = {"format": "json"}
    if norad_id is not None:
        # The DB keys frames by its own UUID; look it up from the satellite
        # record so the NORAD id the rest of this package uses still works.
        found = _satellite_by_norad(norad_id, timeout=timeout)
        if found is None:
            return []
        query["satellite"] = found
    if satellite:
        query["satellite"] = satellite
    if transmitter:
        query["transmitter"] = transmitter
    if start is not None:
        query["start"] = _api_time(start)
    if end is not None:
        query["end"] = _api_time(end)

    frames: List[TelemetryFrame] = []
    cursor: Optional[str] = None
    while len(frames) < min(limit, 500):
        page_query = dict(query)
        if cursor:
            page_query["cursor"] = cursor
        request = urllib.request.Request(
            "%s/telemetry/?%s" % (DB_API, urllib.parse.urlencode(page_query)),
            headers={
                "User-Agent": USER_AGENT,
                "Authorization": "Token %s" % resolved,
            },
        )
        with urllib.request.urlopen(request, timeout=timeout) as response:
            payload = json.loads(response.read().decode("utf-8", "replace"))
        if isinstance(payload, dict):
            items = payload.get("results", [])
            cursor = payload.get("next") or None
        else:  # plain list when unpaginated
            items = payload
            cursor = None
        frames.extend(_telemetry_frame(item) for item in items)
        if not cursor:
            break
    return frames[:max(limit, 0)]


def _api_time(value: datetime) -> str:
    if value.tzinfo is None:
        value = value.replace(tzinfo=timezone.utc)
    return value.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def _satellite_by_norad(norad_id: int, timeout: float = 30.0) -> Optional[str]:
    """The DB's internal UUID for a NORAD catalog number."""
    request = urllib.request.Request(
        "%s/satellites/?norad_cat_id=%d&format=json" % (DB_API, norad_id),
        headers={"User-Agent": USER_AGENT},
    )
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            found = json.loads(response.read().decode("utf-8", "replace"))
    except (urllib.error.URLError, ValueError):
        return None
    if isinstance(found, list) and found:
        return found[0].get("sat_id")
    return None


def _telemetry_frame(raw: Dict[str, Any]) -> TelemetryFrame:
    return TelemetryFrame(
        id=raw.get("id", 0),
        sat_id=raw.get("sat_id") or "",
        norad_id=raw.get("norad_cat_id", 0) or 0,
        transmitter=raw.get("transmitter") or "",
        frame=raw.get("frame"),
        decoded=raw.get("decoded"),
        observation_id=raw.get("observation_id"),
        station_id=raw.get("station_id"),
        timestamp=_parse_time(raw.get("timestamp") or raw.get("created")),
    )
