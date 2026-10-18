"""Shared helpers: stream URLs, tuning parameters and WAV output."""

from __future__ import annotations

import io
import struct
import urllib.parse
import wave
from typing import BinaryIO, Iterable, Optional, Union

#: Modulation names accepted by both back-ends, mapped to WebSDR mode codes.
WEBSDR_MODE_CODES = {
    "USB": 0,
    "LSB": 0,
    "CW": 0,
    "AM": 1,
    "AMSYNC": 2,
    "FM": 4,
}

#: Modulation names accepted by both back-ends, mapped to OpenWebRX names.
OPENWEBRX_MODES = {
    "USB": "usb",
    "LSB": "lsb",
    "CW": "cw",
    "AM": "am",
    "AMSYNC": "sam",
    "FM": "nfm",
    "WFM": "wfm",
    "NFM": "nfm",
}


def normalize_http_url(url: str) -> str:
    """Return *url* with an explicit scheme, defaulting to plain HTTP."""
    url = url.strip()
    if not url:
        raise ValueError("server URL must not be empty")
    if "://" not in url:
        url = "http://" + url
    return url


def websocket_url(url: str, path: str, secure: Optional[bool] = None) -> str:
    """Build a WebSocket URL for *url* pointing at *path*.

    ``http``/``ws`` map to ``ws``, ``https``/``wss`` map to ``wss``. An
    explicit *secure* argument overrides the scheme.
    """
    parsed = urllib.parse.urlsplit(normalize_http_url(url))
    if secure is None:
        secure = parsed.scheme in ("https", "wss")
    scheme = "wss" if secure else "ws"
    base = parsed.path.rstrip("/")
    return urllib.parse.urlunsplit((scheme, parsed.netloc, base + path, "", ""))


def websdr_tuning_params(
    frequency_khz: float,
    mode: str = "USB",
    bandwidth_hz: Optional[float] = None,
    low_khz: Optional[float] = None,
    high_khz: Optional[float] = None,
    band: int = 0,
    name: str = "",
) -> str:
    """Build the WebSDR ``/~~param`` query string that retunes the receiver.

    *frequency_khz* is the suppressed-carrier frequency. For SSB/CW modes the
    passband is expressed as offsets from that carrier via *low_khz* and
    *high_khz*; when only *bandwidth_hz* is given the passband is centred on
    the carrier.
    """
    upper = mode.upper()
    if upper not in WEBSDR_MODE_CODES:
        raise ValueError(
            "unsupported WebSDR mode %r; expected one of %s"
            % (mode, ", ".join(sorted(WEBSDR_MODE_CODES)))
        )

    if low_khz is None or high_khz is None:
        width = (bandwidth_hz or 2700.0) / 1000.0
        if upper == "USB":
            low_khz, high_khz = 0.0, width
        elif upper == "LSB":
            low_khz, high_khz = -width, 0.0
        elif upper in ("CW",):
            low_khz, high_khz = -width / 2, width / 2
        else:
            low_khz, high_khz = -width / 2, width / 2

    params = {
        "f": "%.3f" % frequency_khz,
        "band": str(band),
        "lo": "%.3f" % low_khz,
        "hi": "%.3f" % high_khz,
        "mode": str(WEBSDR_MODE_CODES[upper]),
        "name": name,
    }
    return urllib.parse.urlencode(params)


def write_wav(
    path_or_file: Union[str, BinaryIO],
    samples: Iterable[int],
    samplerate: int,
    channels: int = 1,
) -> int:
    """Write 16-bit PCM samples as a RIFF/WAVE file.

    Returns the number of frames written. Accepts a path or a binary file
    object. Requires the standard-library :mod:`wave` module only.
    """
    samples = list(samples)
    if len(samples) % channels:
        samples = samples[: len(samples) - (len(samples) % channels)]
    payload = struct.pack("<%dh" % len(samples), *samples)
    with wave.open(path_or_file, "wb") as handle:
        handle.setnchannels(channels)
        handle.setsampwidth(2)
        handle.setframerate(samplerate)
        handle.writeframes(payload)
    return len(samples) // channels


def wav_bytes(samples: Iterable[int], samplerate: int, channels: int = 1) -> bytes:
    """Return 16-bit PCM samples encoded as an in-memory WAV file."""
    buffer = io.BytesIO()
    write_wav(buffer, samples, samplerate, channels)
    return buffer.getvalue()


def pcm_bytes(samples: Iterable[int]) -> bytes:
    """Return 16-bit little-endian PCM bytes for *samples*."""
    samples = list(samples)
    return struct.pack("<%dh" % len(samples), *samples)
