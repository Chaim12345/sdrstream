"""Async client for WebSDR servers (``websdr.org`` family).

Protocol summary
----------------
WebSDR serves a raw WebSocket endpoint at ``/~~stream``. There is no
handshake: the server starts pushing binary frames immediately. Retuning is
requested by sending a plain HTTP-style text frame::

    GET /~~param?f=<kHz>&band=0&lo=<kHz>&hi=<kHz>&mode=<code>&name=

Mode codes are ``USB=0``, ``LSB=0``, ``CW=0``, ``AM=1``, ``AMSYNC=2`` and
``FM=4``; SSB and CW differ only in the passband offsets ``lo``/``hi``.

Binary frames carry the mixed table/LPC stream decoded by
:class:`~sdrstream.vocoder.WebSdrVocoder`. Control bytes observed in the
stream are ``0x81`` (sample rate), ``0x82`` (AGC gain), ``0x83`` (audio
flags), ``0x85`` (true frequency), ``0x86`` (filter switch) and ``0x87``
(timestamp).
"""

from __future__ import annotations

import asyncio
import random
import urllib.parse
from dataclasses import dataclass, field
from typing import Any, AsyncIterator, List, Optional

from .common import websdr_tuning_params, websocket_url
from .vocoder import WebSdrVocoder

try:  # pragma: no cover - import guard
    import websockets
except ImportError as exc:  # pragma: no cover
    raise ImportError(
        "the WebSDR client requires the 'websockets' package: pip install websockets"
    ) from exc

STREAM_PATH = "/~~stream"


@dataclass
class WebSdrChunk:
    """One decoded block of WebSDR audio."""

    samples: List[int]
    samplerate: int
    sequence: int = 0


@dataclass
class WebSdrStatus:
    """State reported by the server inside the audio stream."""

    samplerate: Optional[int] = None
    agc: Optional[int] = None
    audio_flags: Optional[int] = None
    true_frequency: Optional[float] = None
    timestamp: Optional[int] = None
    filter_switch: Optional[int] = None
    smeter: Optional[int] = None
    extra: List[Any] = field(default_factory=list)


class WebSDRClient:
    """Streams audio from a WebSDR server.

    Example
    -------
    >>> async with WebSDRClient("http://websdr.example.org:8901") as client:
    ...     await client.tune(frequency_khz=10000.0, mode="AM")
    ...     async for chunk in client.audio():
    ...         ...
    """

    def __init__(
        self,
        url: str,
        *,
        reconnect: bool = True,
        reconnect_initial_delay: float = 1.0,
        reconnect_max_delay: float = 30.0,
        open_timeout: float = 15.0,
        idle_timeout: float = 30.0,
    ) -> None:
        self.url = websocket_url(url, STREAM_PATH)
        # WebSDR validates the Origin header and drops the connection without it.
        self.origin = self._origin_for(url)
        self.reconnect = reconnect
        self.reconnect_initial_delay = reconnect_initial_delay
        self.reconnect_max_delay = reconnect_max_delay
        self.open_timeout = open_timeout
        self.idle_timeout = idle_timeout

        self.status = WebSdrStatus()
        self.samplerate: Optional[int] = None
        self.last_error: Optional[BaseException] = None

        self._ws: Any = None
        self._vocoder = WebSdrVocoder()
        self._sequence = 0
        self._events: "asyncio.Queue[Optional[Any]]" = asyncio.Queue()
        self._reader: Optional[asyncio.Task] = None
        self._tuning: Optional[str] = None

    # -- connection lifecycle ------------------------------------------------

    async def __aenter__(self) -> "WebSDRClient":
        await self.connect()
        return self

    async def __aexit__(self, *exc_info: Any) -> None:
        await self.close()

    async def connect(self) -> None:
        """Open the stream and send the pending tuning request, if any."""
        self._ws = await websockets.connect(
            self.url,
            max_size=None,
            open_timeout=self.open_timeout,
            ping_interval=None,
            origin=self.origin,
        )
        self._vocoder.reset()
        self._events = asyncio.Queue()
        self._sequence = 0
        if self._tuning:
            await self._ws.send("GET /~~param?" + self._tuning)
        self._reader = asyncio.create_task(self._read_loop())

    @staticmethod
    def _origin_for(url: str) -> str:
        """Derive the Origin header the server expects from *url*."""
        if "://" not in url:
            url = "http://" + url
        parsed = urllib.parse.urlsplit(url)
        scheme = "https" if parsed.scheme in ("https", "wss") else "http"
        return "%s://%s" % (scheme, parsed.netloc)

    async def close(self) -> None:
        """Close the stream and cancel the background reader."""
        if self._reader is not None:
            self._reader.cancel()
            try:
                await self._reader
            except (asyncio.CancelledError, Exception):
                pass
            self._reader = None
        if self._ws is not None:
            try:
                await self._ws.close()
            except Exception:
                pass
            self._ws = None

    # -- tuning --------------------------------------------------------------

    async def tune(
        self,
        frequency_khz: Optional[float] = None,
        *,
        mode: str = "USB",
        bandwidth_hz: Optional[float] = None,
        low_khz: Optional[float] = None,
        high_khz: Optional[float] = None,
        band: int = 0,
        name: str = "",
    ) -> None:
        """Retune the receiver. *frequency_khz* is the carrier in kHz."""
        if frequency_khz is not None:
            self._tuning = websdr_tuning_params(
                frequency_khz,
                mode=mode,
                bandwidth_hz=bandwidth_hz,
                low_khz=low_khz,
                high_khz=high_khz,
                band=band,
                name=name,
            )
        if self._ws is not None and self._tuning:
            await self._ws.send("GET /~~param?" + self._tuning)

    async def send_param(self, param: str) -> None:
        """Send a raw ``/~~param`` query string (e.g. ``"squelch=3"``)."""
        if self._ws is not None:
            await self._ws.send("GET /~~param?" + param)

    async def set_squelch(self, value: float) -> None:
        """Set the squelch level; ``0`` disables it."""
        await self.send_param("squelch=%s" % value)

    async def set_noise_reduction(self, level: float) -> None:
        """Set the noise-reduction level; ``-999`` turns it off."""
        await self.send_param("noisered=%s" % level)

    async def set_auto_notch(self, value: float) -> None:
        """Enable or disable the automatic notch filter."""
        await self.send_param("autonotch=%s" % value)

    # -- message handling ----------------------------------------------------

    async def _read_loop(self) -> None:
        delay = self.reconnect_initial_delay
        while True:
            try:
                while True:
                    message = await asyncio.wait_for(
                        self._ws.recv(), timeout=self.idle_timeout
                    )
                    delay = self.reconnect_initial_delay
                    if isinstance(message, (bytes, bytearray, memoryview)):
                        self._handle_binary(bytes(message))
                    else:
                        self._push({"type": "text", "value": message})
            except asyncio.CancelledError:
                raise
            except Exception as exc:
                self.last_error = exc
                self._push({"type": "error", "value": exc})
                if not self.reconnect:
                    self._push(None)
                    return
                await self._events.put({"type": "reconnecting", "value": delay})
                await asyncio.sleep(delay + random.random() * delay * 0.1)
                delay = min(delay * 2, self.reconnect_max_delay)
                try:
                    await self.connect()
                except Exception as inner:
                    self.last_error = inner

    def _handle_binary(self, message: bytes) -> None:
        samples = self._vocoder.decode(message)
        samplerate = self._vocoder.samplerate
        if samplerate != self.samplerate:
            self.samplerate = samplerate
            self.status.samplerate = samplerate
        self.status.agc = self._vocoder.agc
        self.status.audio_flags = self._vocoder.audio_flags
        if samples:
            chunk = WebSdrChunk(
                samples=samples,
                samplerate=samplerate,
                sequence=self._sequence,
            )
            self._sequence += 1
            self._push({"type": "audio", "value": chunk})

    def _push(self, event: Any) -> None:
        self._events.put_nowait(event)

    # -- public streaming API ------------------------------------------------

    async def audio(self) -> AsyncIterator[WebSdrChunk]:
        """Yield :class:`WebSdrChunk` objects until the stream ends."""
        while True:
            event = await self._events.get()
            if event is None:
                return
            if event.get("type") == "audio":
                yield event["value"]

    async def events(self) -> AsyncIterator[dict]:
        """Yield every event (audio, text, status, errors)."""
        while True:
            event = await self._events.get()
            if event is None:
                return
            yield event

    async def record(self, duration: float) -> List[WebSdrChunk]:
        """Collect audio for *duration* seconds and return the chunks."""
        collected: List[WebSdrChunk] = []
        deadline = asyncio.get_running_loop().time() + duration
        while True:
            remaining = deadline - asyncio.get_running_loop().time()
            if remaining <= 0:
                break
            try:
                event = await asyncio.wait_for(self._events.get(), timeout=remaining)
            except asyncio.TimeoutError:
                break
            if event is None:
                break
            if event.get("type") == "audio":
                collected.append(event["value"])
        return collected

    @staticmethod
    def concat(chunks: List[WebSdrChunk]) -> List[int]:
        """Flatten a list of chunks into a single sample list."""
        out: List[int] = []
        for chunk in chunks:
            out.extend(chunk.samples)
        return out
