"""Discover OpenWebRX receivers and the bands they can hear.

OpenWebRX publishes a small JSON document at ``/status.json`` on every server.
It carries the operator's coordinates (often from the receiver's own GPS) and,
for every SDR attached, the list of tuning profiles with their centre frequency
and sample rate. That is enough to work out exactly which receivers can hear a
given satellite downlink, without connecting to any of them.

The WebSDR catalogue in :mod:`sdrstream.catalogue` is a static file built from
the WebSDR project's server list. This module covers the other large public
network, and it is queried live because OpenWebRX servers come and go quickly.

    >>> from sdrstream import openwebrx_directory as directory
    >>> found = directory.probe(["http://sdr.example.org:8073"])
    >>> found[0].can_hear(145_800_000)
    True
"""

from __future__ import annotations

import json
import urllib.error
import urllib.request
from concurrent.futures import ThreadPoolExecutor
from typing import Any, Dict, Iterable, List, Optional

from .catalogue import Band, Receiver

#: Where the crowdsourced list of public receivers lives.
DIRECTORY_URL = "https://www.receiverbook.de/map"

#: Amateur satellite bands, in MHz, used to label a receiver's coverage.
SAT_BANDS = {
    "6m": (50.0, 54.0),
    "2m": (144.0, 148.0),
    "70cm": (430.0, 440.0),
    "23cm": (1240.0, 1300.0),
}

USER_AGENT = "sdrstream/0.1 (+openwebrx-directory)"


def fetch(url: str, timeout: float = 30.0) -> str:
    request = urllib.request.Request(url, headers={"User-Agent": USER_AGENT})
    with urllib.request.urlopen(request, timeout=timeout) as response:
        return response.read().decode("utf-8", "replace")


def parse_directory(html: str) -> List[Dict[str, Any]]:
    """Pull the ``var receivers = [...]`` array out of the directory page.

    The page embeds the whole directory as one JSON literal, which is far
    easier to read than scraping the map markers.
    """
    marker = "var receivers ="
    start = html.find(marker)
    if start < 0:
        raise ValueError("no receiver array in the directory page")
    start = html.find("[", start)
    if start < 0:
        raise ValueError("no receiver array in the directory page")

    # Walk the brackets so a stray '];' inside a label cannot cut the array
    # short. Labels are operator-written, so this is not hypothetical.
    depth = 0
    in_string = False
    escaped = False
    for index in range(start, len(html)):
        character = html[index]
        if in_string:
            if escaped:
                escaped = False
            elif character == "\\":
                escaped = True
            elif character == '"':
                in_string = False
            continue
        if character == '"':
            in_string = True
        elif character == "[":
            depth += 1
        elif character == "]":
            depth -= 1
            if depth == 0:
                return json.loads(html[start:index + 1])
    raise ValueError("unterminated receiver array in the directory page")


def directory_urls(html: str, kind: str = "OpenWebRX") -> List[str]:
    """Every receiver URL of *kind* in the directory page, de-duplicated."""
    seen = {}
    for site in parse_directory(html):
        for receiver in site.get("receivers") or []:
            if receiver.get("type") != kind:
                continue
            url = (receiver.get("url") or "").strip()
            if url:
                seen[url] = None
    return list(seen)


def bands_from_status(status: Dict[str, Any]) -> List[Band]:
    """Turn an OpenWebRX status document into the bands it can tune.

    Every profile is a window of ``sample_rate`` centred on ``center_freq``,
    which is the same shape the catalogue uses for WebSDR bands.
    """
    bands = []
    for sdr in status.get("sdrs") or []:
        for profile in sdr.get("profiles") or []:
            center = profile.get("center_freq")
            rate = profile.get("sample_rate")
            if not center or not rate:
                continue
            bands.append(Band(name=profile.get("name") or sdr.get("name") or "band",
                              center_khz=center / 1000.0,
                              samplerate_khz=rate / 1000.0))
    return bands


def satellite_bands(bands: Iterable[Band]) -> List[str]:
    """Which amateur satellite bands a set of bands reaches."""
    bands = list(bands)
    hits = []
    for name, (low_mhz, high_mhz) in SAT_BANDS.items():
        low_hz, high_hz = low_mhz * 1e6, high_mhz * 1e6
        if any(band.low_hz <= high_hz and band.high_hz >= low_hz for band in bands):
            hits.append(name)
    return sorted(hits)


def receiver_from_status(url: str, status: Dict[str, Any]) -> Optional[Receiver]:
    """Build a :class:`~sdrstream.catalogue.Receiver` from a status document.

    Returns ``None`` when the server publishes no coordinates, because a
    receiver with no position cannot be used for pass planning.
    """
    info = status.get("receiver") or {}
    gps = info.get("gps") or {}
    latitude, longitude = gps.get("lat"), gps.get("lon")
    if latitude is None or longitude is None:
        return None

    bands = bands_from_status(status)
    return Receiver(url=url, kind="openwebrx", latitude=float(latitude),
                    longitude=float(longitude), bands=tuple(bands),
                    satellite_bands=tuple(satellite_bands(bands)))


def probe_one(url: str, timeout: float = 15.0) -> Optional[Receiver]:
    """Ask one server for its status. Failures return ``None``, never raise."""
    base = url if url.endswith("/") else url + "/"
    try:
        text = fetch(base + "status.json", timeout=timeout)
        status = json.loads(text)
    except (urllib.error.URLError, OSError, ValueError, TimeoutError):
        return None
    if not isinstance(status, dict):
        return None
    return receiver_from_status(base.rstrip("/"), status)


def probe(urls: Iterable[str], timeout: float = 15.0,
          workers: int = 16) -> List[Receiver]:
    """Probe many servers at once, keeping the ones that answered."""
    urls = list(urls)
    if not urls:
        return []
    with ThreadPoolExecutor(max_workers=workers) as pool:
        results = pool.map(lambda url: probe_one(url, timeout=timeout), urls)
    return [receiver for receiver in results if receiver is not None]


def discover(kind: str = "OpenWebRX", *, timeout: float = 30.0,
             workers: int = 16, directory_url: str = DIRECTORY_URL,
             limit: Optional[int] = None) -> List[Receiver]:
    """Read the public directory, then probe every listed server.

    This touches a few hundred hosts, so it is a deliberate, occasional
    operation rather than something to run in a loop.
    """
    urls = directory_urls(fetch(directory_url, timeout=timeout), kind=kind)
    if limit is not None:
        urls = urls[:limit]
    return probe(urls, workers=workers)


def for_frequency(frequency_hz: float, receivers: Iterable[Receiver],
                  band: Optional[str] = None) -> List[Receiver]:
    """The receivers from *receivers* that can hear *frequency_hz*."""
    found = []
    for receiver in receivers:
        if not receiver.can_hear(frequency_hz):
            continue
        if band is not None and band not in receiver.satellite_bands:
            continue
        found.append(receiver)
    return found
