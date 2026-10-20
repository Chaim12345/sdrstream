"""Fetching and caching two-line element sets (TLEs).

Celestrak publishes the amateur-radio catalogue as plain text. TLEs go stale:
SGP4 error grows by roughly a kilometre a day for a low Earth orbit, which is
tolerable for a day or two and useless after a few weeks. The cache here keeps
the fetch time so callers can decide whether to refetch.
"""

from __future__ import annotations

import os
import time
import urllib.request
from pathlib import Path
from typing import Dict, Optional

#: TLE groups useful for listening. "amateur" holds the FM and linear
#: satellites; "weather" and "noaa" hold the 137 MHz APT birds. Each group
#: lists its download URLs in fallback order: AMSAT publishes the same
#: amateur set as Celestrak and covers Celestrak outages.
GROUPS: Dict[str, tuple] = {
    "amateur": (
        "https://celestrak.org/NORAD/elements/gp.php?GROUP=amateur&FORMAT=tle",
        "https://www.amsat.org/tle/current/nasa.all",
    ),
    "weather": (
        "https://celestrak.org/NORAD/elements/gp.php?GROUP=weather&FORMAT=tle",
    ),
    "stations": (
        "https://celestrak.org/NORAD/elements/gp.php?GROUP=stations&FORMAT=tle",
    ),
    "cubesat": (
        "https://celestrak.org/NORAD/elements/gp.php?GROUP=cubesat&FORMAT=tle",
    ),
}

DEFAULT_CACHE = Path(
    os.environ.get("SDRSTREAM_CACHE", Path.home() / ".cache" / "sdrstream")
)

#: Celestrak can return a single object by catalogue number. Used for the few
#: birds that are not in any group we download, so the table still tracks them.
CATNR_URL = ("https://celestrak.org/NORAD/elements/gp.php?CATNR=%d&FORMAT=tle")


def cache_path(group: str, cache_dir: Optional[Path] = None) -> Path:
    return (cache_dir or DEFAULT_CACHE) / ("tle_%s.txt" % group)


def age_seconds(group: str, cache_dir: Optional[Path] = None) -> Optional[float]:
    """Seconds since the cached TLE set was fetched, or ``None`` if absent."""
    path = cache_path(group, cache_dir)
    if not path.exists():
        return None
    return time.time() - path.stat().st_mtime


def _looks_like_tle(text: str) -> bool:
    """AMSAT's nasa.all carries a bulletin header before the TLEs, so the
    test is 'contains a line starting with 1 ' rather than a magic prefix."""
    return any(line.startswith("1 ") for line in text.splitlines())


def fetch(group: str = "amateur", timeout: float = 30.0,
          cache_dir: Optional[Path] = None) -> str:
    """Download a TLE set and cache it. Returns the raw text.

    Each source URL is tried in turn until one returns something that
    plausibly contains element sets."""
    if group not in GROUPS:
        raise KeyError("unknown group %r; known: %s"
                       % (group, ", ".join(sorted(GROUPS))))
    last_error: Optional[Exception] = None
    for url in GROUPS[group]:
        request = urllib.request.Request(
            url, headers={"User-Agent": "sdrstream/0.1 (+satellite)"}
        )
        try:
            with urllib.request.urlopen(request, timeout=timeout) as response:
                text = response.read().decode("utf-8", "replace")
        except Exception as error:  # noqa: BLE001 - try the next source
            last_error = error
            continue
        if not _looks_like_tle(text):
            last_error = ValueError("no element lines found at %s" % url)
            continue
        path = cache_path(group, cache_dir)
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)
        return text
    if last_error is not None:
        raise last_error
    raise KeyError("group %r has no sources" % group)


def load(group: str = "amateur", max_age_hours: float = 24.0,
         cache_dir: Optional[Path] = None, offline: bool = False) -> str:
    """Return a TLE set, downloading it only when the cache is stale.

    With *offline* set, the cached copy is used no matter how old it is; the
    function raises if there is no cache at all.
    """
    age = age_seconds(group, cache_dir)
    if age is not None and (offline or age < max_age_hours * 3600.0):
        return cache_path(group, cache_dir).read_text()
    if offline:
        raise FileNotFoundError(
            "no cached TLE set for %r; run without --offline once" % group
        )
    try:
        return fetch(group, cache_dir=cache_dir)
    except Exception:
        if age is not None:  # network down but a stale cache exists
            return cache_path(group, cache_dir).read_text()
        raise


def catnr(line1: str) -> int:
    """The catalogue number from TLE line 1, as an integer.

    Celestrak zero-pads it to five columns, so "07530" and "7530" are the same
    object; comparing the raw text would treat them as different.
    """
    return int(line1[2:7])


def parse(text: str) -> Dict[str, Dict[str, str]]:
    """Parse a TLE file into ``{name: {"line1": ..., "line2": ...}}``.

    Celestrak's format is a name line followed by the two element lines. Some
    files omit names, in which case the satellite number from line 1 is used.
    """
    lines = [line.rstrip() for line in text.splitlines()]
    out: Dict[str, Dict[str, str]] = {}
    i = 0
    while i < len(lines):
        line = lines[i]
        if line.startswith("1 ") and i + 1 < len(lines) and lines[i + 1].startswith("2 "):
            name = line[2:7].strip()
            out[name] = {"line1": line, "line2": lines[i + 1]}
            i += 2
            continue
        if line.strip() and i + 2 < len(lines) and lines[i + 1].startswith("1 "):
            out[line.strip()] = {"line1": lines[i + 1], "line2": lines[i + 2]}
            i += 3
            continue
        i += 1
    return out


def lookup(name: str, group: str = "amateur", **kwargs) -> Dict[str, str]:
    """Find one satellite's TLE by name, allowing partial matches."""
    elements = parse(load(group, **kwargs))
    want = name.strip().upper()
    if want in elements:
        return elements[want]
    for key, value in elements.items():
        if want in key.upper():
            return value
    raise KeyError("no TLE for %r in group %r (%d satellites)"
                   % (name, group, len(elements)))


def fetch_catnr(norad_id: int, timeout: float = 30.0,
                cache_dir: Optional[Path] = None) -> str:
    """Download one object's TLE by catalogue number, and cache it.

    Celestrak answers a miss with a short ``No GP data found`` body rather than
    an error, so a real TLE is recognised by its ``1 `` element line.
    """
    url = CATNR_URL % norad_id
    request = urllib.request.Request(
        url, headers={"User-Agent": "sdrstream/0.1 (+satellite)"}
    )
    with urllib.request.urlopen(request, timeout=timeout) as response:
        text = response.read().decode("utf-8", "replace")
    path = (cache_dir or DEFAULT_CACHE) / ("tle_catnr_%d.txt" % norad_id)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text)
    return text


def load_catnr(norad_id: int, max_age_hours: float = 24.0,
               cache_dir: Optional[Path] = None,
               offline: bool = False) -> Optional[str]:
    """A single object's TLE text, cached, or ``None`` if Celestrak has none.

    This is the fallback for a satellite in our table that no downloaded group
    carries, such as the older AO-7 and CAS-4 birds.
    """
    path = (cache_dir or DEFAULT_CACHE) / ("tle_catnr_%d.txt" % norad_id)
    age = None
    if path.exists():
        age = time.time() - path.stat().st_mtime
    if age is not None and (offline or age < max_age_hours * 3600.0):
        text = path.read_text()
    elif offline:
        return None
    else:
        try:
            text = fetch_catnr(norad_id, cache_dir=cache_dir)
        except Exception:
            return path.read_text() if path.exists() else None
    return text if "1 " in text else None
