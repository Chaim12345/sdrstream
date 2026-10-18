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

#: Celestrak groups useful for listening. "amateur" holds the FM and linear
#: satellites; "weather" and "noaa" hold the 137 MHz APT birds.
GROUPS: Dict[str, str] = {
    "amateur": "https://celestrak.org/NORAD/elements/gp.php?GROUP=amateur&FORMAT=tle",
    "weather": "https://celestrak.org/NORAD/elements/gp.php?GROUP=weather&FORMAT=tle",
    "stations": "https://celestrak.org/NORAD/elements/gp.php?GROUP=stations&FORMAT=tle",
    "cubesat": "https://celestrak.org/NORAD/elements/gp.php?GROUP=cubesat&FORMAT=tle",
}

DEFAULT_CACHE = Path(
    os.environ.get("SDRSTREAM_CACHE", Path.home() / ".cache" / "sdrstream")
)


def cache_path(group: str, cache_dir: Optional[Path] = None) -> Path:
    return (cache_dir or DEFAULT_CACHE) / ("tle_%s.txt" % group)


def age_seconds(group: str, cache_dir: Optional[Path] = None) -> Optional[float]:
    """Seconds since the cached TLE set was fetched, or ``None`` if absent."""
    path = cache_path(group, cache_dir)
    if not path.exists():
        return None
    return time.time() - path.stat().st_mtime


def fetch(group: str = "amateur", timeout: float = 30.0,
          cache_dir: Optional[Path] = None) -> str:
    """Download a TLE set and cache it. Returns the raw text."""
    if group not in GROUPS:
        raise KeyError("unknown group %r; known: %s"
                       % (group, ", ".join(sorted(GROUPS))))
    request = urllib.request.Request(
        GROUPS[group], headers={"User-Agent": "sdrstream/0.1 (+satellite)"}
    )
    with urllib.request.urlopen(request, timeout=timeout) as response:
        text = response.read().decode("utf-8", "replace")
    path = cache_path(group, cache_dir)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text)
    return text


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
