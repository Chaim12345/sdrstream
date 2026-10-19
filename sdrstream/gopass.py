"""Fast pass prediction backed by the Go ``sdrpass`` helper.

The pure-Python pass finder in :mod:`sdrstream.tracking` is correct but slow
enough that a sweep over hundreds of public receivers takes tens of seconds.
``gotools/sdrpass`` does the same scan in Go, propagating the orbit once per
time step and evaluating every observer from that position, which turns the
sweep into a fraction of a second.

The Go helper is optional: if the binary is not built the caller falls back to
the Python implementation. Build it with ``tools/build_sdrpass.sh``.
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess
from pathlib import Path
from typing import Dict, Optional, Sequence

#: Repository root, used to find the prebuilt binary.
_ROOT = Path(__file__).resolve().parent.parent

#: Where ``tools/build_sdrpass.sh`` leaves the binary.
DEFAULT_BINARY = _ROOT / "gotools" / "sdrpass" / "sdrpass"


def binary_path() -> Optional[str]:
    """Path to the ``sdrpass`` binary, or None if it is not available.

    ``SDRPASS_BIN`` overrides the search, which is how a system-wide install
    or a differently placed build is picked up.
    """
    override = os.environ.get("SDRPASS_BIN")
    if override:
        return override if Path(override).is_file() else None
    if DEFAULT_BINARY.is_file():
        return str(DEFAULT_BINARY)
    found = shutil.which("sdrpass")
    return found


def available() -> bool:
    """True when the Go helper can be used."""
    return binary_path() is not None


def predict(job: Dict, binary: Optional[str] = None) -> Dict:
    """Run one prediction job through the Go helper.

    *job* has the keys ``start`` (RFC3339 UTC), ``hours``, ``min_elevation``,
    ``step_seconds``, ``satellites`` (name, line1, line2) and ``observers``
    (lat, lon). The returned report holds one pass list per satellite per
    observer, in the input order.
    """
    binary = binary or binary_path()
    if binary is None:
        raise RuntimeError("sdrpass binary not found; run tools/build_sdrpass.sh")
    proc = subprocess.run(
        [binary],
        input=json.dumps(job).encode(),
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    if proc.returncode != 0:
        raise RuntimeError("sdrpass failed: %s"
                           % proc.stderr.decode(errors="replace").strip())
    return json.loads(proc.stdout.decode())


def observers_for(receivers: Sequence):
    """Distinct observer positions plus the index of each receiver's observer.

    Several receivers can share a position (the same site offering two
    protocols, say), and predicting the same geometry twice is wasted work, so
    the positions are de-duplicated and the caller is handed the mapping back.
    """
    seen = {}
    observers = []
    indices = []
    for receiver in receivers:
        key = (round(receiver.latitude, 4), round(receiver.longitude, 4))
        index = seen.get(key)
        if index is None:
            index = len(observers)
            seen[key] = index
            observers.append({"lat": receiver.latitude,
                              "lon": receiver.longitude})
        indices.append(index)
    return observers, indices
