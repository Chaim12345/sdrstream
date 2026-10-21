"""Discover public KiwiSDR receivers from the mirrored public directory.

The KiwiSDR project's public list is mirrored by AetherSDR at the
maintainer's own request — individual clients fetching the origin directly
was a load problem for his server, so the mirror is the polite source:
``https://cdn.aethersdr.com/kiwi.json``, refreshed hourly, served with
``cache-control: max-age=1800``. This module never touches the origin.

Every receiver entry carries ``ext_api``, the operator's published policy for
external API clients: ``0`` means the API is disabled and the receiver is
web-browser-only, a positive value is the number of channels the operator
allows API clients, and a missing key means the policy is not published.
:func:`api_permitted` returns only receivers that explicitly allow API
connections, so a native-protocol client (like :mod:`sdrstream.kiwisdr`)
respects each operator's choice.

    >>> from sdrstream import kiwisdr_directory
    >>> receivers = kiwisdr_directory.parse_directory('{"schema": 1, ...}')
    >>> ok = kiwisdr_directory.api_permitted(receivers)
    >>> kiwisdr_directory.for_frequency(14_070_000, ok)
    [...]
"""

from __future__ import annotations

import json
import os
import time
import urllib.request
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Dict, List, Optional

#: The maintainer-approved mirror of the KiwiSDR public list.
DIRECTORY_URL = "https://cdn.aethersdr.com/kiwi.json"

#: The only schema this parser understands; anything else is refused rather
#: than parsed hopefully (the mirror bumps it on any field change).
SUPPORTED_SCHEMA = 1

#: Client-side sanity bounds from the published contract.
MAX_BODY_BYTES = 32 * 1024 * 1024
MAX_RECEIVERS = 20_000

#: The mirror serves ``max-age=1800``; a local cache honours it so repeated
#: discovery calls do not poll the mirror faster than it asks.
CACHE_TTL_SECONDS = 1800.0

USER_AGENT = "sdrstream/0.1 (+kiwisdr-directory)"

_CACHE_PATH = Path(
    os.environ.get("SDRSTREAM_CACHE",
                   str(Path.home() / ".cache" / "sdrstream"))
) / "kiwisdr_directory.json"


@dataclass(frozen=True)
class KiwiReceiver:
    """One public KiwiSDR receiver, as the mirror publishes it."""

    url: str
    name: str = ""
    location: str = ""
    grid: str = ""
    latitude: Optional[float] = None
    longitude: Optional[float] = None
    bands: tuple = ()  # (low_hz, high_hz) pairs
    users: int = 0
    users_max: int = 0
    #: The operator's external-API allowance: 0 = disabled, >0 = channels
    #: allowed for API clients, -1 = policy not published.
    ext_api: int = -1
    offline: bool = False

    @property
    def api_policy_published(self) -> bool:
        return self.ext_api >= 0

    @property
    def api_allowed(self) -> bool:
        """Whether the operator explicitly allows API connections."""
        return self.ext_api > 0

    def can_hear(self, frequency_hz: float) -> bool:
        """Whether a published band covers *frequency_hz*."""
        for low, high in self.bands:
            if low <= frequency_hz <= high:
                return True
        return False


def _body_bounds(text: str) -> None:
    if len(text.encode("utf-8", "replace")) > MAX_BODY_BYTES:
        raise ValueError("directory body exceeds the size bound")
    if len(text) > MAX_BODY_BYTES:
        raise ValueError("directory body exceeds the size bound")


def parse_directory(payload: str) -> List[KiwiReceiver]:
    """Parse the mirror's JSON document into receivers.

    A top-level array is a hard failure (the envelope is an object), an
    unknown ``schema`` is refused, and entries without a URL are skipped.
    """
    _body_bounds(payload)
    document = json.loads(payload)
    if not isinstance(document, dict):
        raise ValueError("directory envelope must be a JSON object")
    schema = document.get("schema")
    if schema != SUPPORTED_SCHEMA:
        raise ValueError("unsupported directory schema %r (expected %d)"
                         % (schema, SUPPORTED_SCHEMA))
    receivers = document.get("receivers")
    if not isinstance(receivers, list):
        raise ValueError("directory carries no receivers array")
    if len(receivers) > MAX_RECEIVERS:
        raise ValueError("directory exceeds the receiver-count bound")

    out: List[KiwiReceiver] = []
    for entry in receivers:
        url = (entry.get("url") or "").strip() if isinstance(entry, dict) else ""
        if not url:
            continue  # the contract: an entry with no url is skipped
        gps = entry.get("gps") or []
        latitude = longitude = None
        if (isinstance(gps, list) and len(gps) == 2
                and isinstance(gps[0], (int, float))
                and isinstance(gps[1], (int, float))):
            # [0, 0] means no fix, not null island.
            if not (gps[0] == 0 and gps[1] == 0):
                if -90.0 <= gps[0] <= 90.0 and -180.0 <= gps[1] <= 180.0:
                    latitude, longitude = float(gps[0]), float(gps[1])
        bands = []
        raw_bands = entry.get("bands")
        if isinstance(raw_bands, list) and len(raw_bands) == 2:
            low, high = raw_bands
            if (isinstance(low, (int, float)) and isinstance(high, (int, float))
                    and high > low):
                bands.append((float(low), float(high)))
        out.append(KiwiReceiver(
            url=url,
            name=str(entry.get("name") or ""),
            location=str(entry.get("loc") or ""),
            grid=str(entry.get("grid") or ""),
            latitude=latitude,
            longitude=longitude,
            bands=tuple(bands),
            users=max(0, int(entry.get("users") or 0)),
            users_max=max(0, int(entry.get("users_max") or 0)),
            ext_api=_ext_api(entry),
            offline=bool(entry.get("offline") or False),
        ))
    return out


def _ext_api(entry: Dict[str, Any]) -> int:
    """The operator's published API allowance; -1 when not published.

    Reading a missing key as 0 would silently reclassify operators that
    simply do not publish a policy as having disabled the API, so the key's
    absence is kept distinct.
    """
    if "ext_api" not in entry:
        return -1
    try:
        return int(entry["ext_api"])
    except (TypeError, ValueError):
        return -1


def api_permitted(receivers: List[KiwiReceiver],
                  include_offline: bool = False) -> List[KiwiReceiver]:
    """Only receivers whose operators allow API connections.

    Operators publish ``ext_api`` for a reason; a native-protocol client
    should only ever present these. Receivers with no published policy are
    excluded too — the safe reading, since the operator never opted in.
    """
    return [receiver for receiver in receivers
            if receiver.api_allowed
            and (include_offline or not receiver.offline)]


def for_frequency(frequency_hz: float,
                  receivers: List[KiwiReceiver]) -> List[KiwiReceiver]:
    """The receivers that can hear *frequency_hz*."""
    return [receiver for receiver in receivers
            if receiver.can_hear(frequency_hz)]


def discover(timeout: float = 30.0, cache: bool = True,
             url: str = DIRECTORY_URL) -> List[KiwiReceiver]:
    """Fetch the mirrored public directory and parse it.

    The mirror asks clients not to poll faster than every 30 minutes, so a
    cached copy is reused inside that window (and an offline call falls back
    to it) instead of hitting the CDN again.
    """
    if cache and _CACHE_PATH.exists():
        age = time.time() - _CACHE_PATH.stat().st_mtime
        if age < CACHE_TTL_SECONDS:
            try:
                return parse_directory(_CACHE_PATH.read_text())
            except ValueError:
                pass  # a stale or corrupt cache is refetched, never fatal
    request = urllib.request.Request(
        url, headers={"User-Agent": USER_AGENT})
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            payload = response.read().decode("utf-8", "replace")
    except Exception:
        # Offline: a stale cached copy beats no directory at all.
        if not cache or not _CACHE_PATH.exists():
            raise
        return parse_directory(_CACHE_PATH.read_text())
    receivers = parse_directory(payload)
    if cache:
        _CACHE_PATH.parent.mkdir(parents=True, exist_ok=True)
        _CACHE_PATH.write_text(payload)
    return receivers
