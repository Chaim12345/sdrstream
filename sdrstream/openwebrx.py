"""Async client for OpenWebRX and OpenWebRX+ servers.

Protocol summary
----------------
Connect to ``ws(s)://<host>/ws/`` and send a text handshake::

    SERVER DE CLIENT client=<name> type=receiver

The server replies with ``CLIENT DE SERVER ...`` followed by JSON text frames
(``config``, ``profiles``, ``modes``, ``smeter``, ...). After the client sends
``{"type": "dspcontrol", "action": "start"}`` the server begins streaming
binary frames whose first byte is an opcode:

===== ============ =================================================
Byte  Payload      Meaning
===== ============ =================================================
0x01  FFT          Waterfall spectrum, IMA ADPCM, no sync markers
0x02  AUDIO        12 kHz demodulated audio, IMA ADPCM with SYNC markers
0x03  SECONDAY FFT Reserved
0x04  HD AUDIO     High-definition audio (e.g. 44.1 kHz)
===== ============ =================================================

Audio payloads may be ``adpcm`` compressed or raw 16-bit PCM, depending on the
``audio_compression`` value advertised in the ``config`` message.
"""

from __future__ import annotations

import asyncio
import json
import random
import struct
from dataclasses import dataclass, field
from typing import Any, AsyncIterator, Callable, Dict, List, Optional

from .adpcm import AdpcmDecoder
from .common import OPENWEBRX_MODES, websocket_url

try:  # pragma: no cover - import guard
    import websockets
except ImportError as exc:  # pragma: no cover
    raise ImportError(
        "the OpenWebRX client requires the 'websockets' package: pip install websockets"
    ) from exc

HANDSHAKE_CLIENT = "SERVER DE CLIENT client={client} type=receiver"
HANDSHAKE_SERVER_PREFIX = "CLIENT DE SERVER"

OPCODE_FFT = 0x01
OPCODE_AUDIO = 0x02
OPCODE_SECONDARY_FFT = 0x03
OPCODE_HD_AUDIO = 0x04

DEFAULT_OUTPUT_RATE = 12000
DEFAULT_HD_OUTPUT_RATE = 44100


@dataclass
class AudioChunk:
    """One decoded block of audio."""

    samples: List[int]
    samplerate: int
    sequence: int = 0
    #: ``True`` when the payload was ADPCM compressed.
    compressed: bool = False
    #: ``"audio"`` or ``"hd_audio"``.
    kind: str = "audio"


@dataclass
class OpenWebRXConfig:
    """Server configuration advertised in the ``config`` message."""

    center_freq: Optional[int] = None
    samp_rate: Optional[float] = None
    start_mod: Optional[str] = None
    start_freq: Optional[int] = None
    audio_compression: Optional[str] = None
    fft_compression: Optional[str] = None
    audio_rates: List[int] = field(default_factory=list)
    hd_audio_rates: List[int] = field(default_factory=list)
    hd_audio_compression: Optional[str] = None
    max_clients: Optional[int] = None
    raw: Dict[str, Any] = field(default_factory=dict)

    @classmethod
    def from_dict(cls, value: Dict[str, Any]) -> "OpenWebRXConfig":
        return cls(
            center_freq=value.get("center_freq"),
            samp_rate=value.get("samp_rate"),
            start_mod=value.get("start_mod"),
            start_freq=value.get("start_freq"),
            audio_compression=value.get("audio_compression"),
            fft_compression=value.get("fft_compression"),
            audio_rates=value.get("audio_rates") or [],
            hd_audio_rates=value.get("hd_audio_rates") or [],
            hd_audio_compression=value.get("hd_audio_compression"),
            max_clients=value.get("max_clients"),
            raw=dict(value),
        )


class OpenWebRXClient:
    """Streams demodulated audio from an OpenWebRX server.

    Example
    -------
    >>> async with OpenWebRXClient("wss://sdr.example.org/ws/") as client:
    ...     await client.tune(frequency=7100000, modulation="lsb")
    ...     async for chunk in client.audio():
    ...         ...
    """

    def __init__(
        self,
        url: str,
        *,
        client_name: str = "sdrstream",
        output_rate: int = DEFAULT_OUTPUT_RATE,
        hd_output_rate: int = DEFAULT_HD_OUTPUT_RATE,
        reconnect: bool = True,
        reconnect_initial_delay: float = 1.0,
        reconnect_max_delay: float = 30.0,
        open_timeout: float = 15.0,
        idle_timeout: float = 30.0,
    ) -> None:
        self.url = url if "://" in url and url.split("://", 1)[0] in ("ws", "wss") \
            else websocket_url(url, "/ws/")
        self.client_name = client_name
        self.output_rate = output_rate
        self.hd_output_rate = hd_output_rate
        self.reconnect = reconnect
        self.reconnect_initial_delay = reconnect_initial_delay
        self.reconnect_max_delay = reconnect_max_delay
        self.open_timeout = open_timeout
        self.idle_timeout = idle_timeout

        self.config: Optional[OpenWebRXConfig] = None
        self._config_parts: Dict[str, Any] = {}
        self.profiles: List[Dict[str, Any]] = []
        self.modes: List[Dict[str, Any]] = []
        self.smeter: Optional[float] = None
        self.receiver_details: Dict[str, Any] = {}
        self.clients: int = 0
        self.last_error: Optional[BaseException] = None

        self._ws: Any = None
        self._tuning: Dict[str, Any] = {}
        self._audio_decoder = AdpcmDecoder()
        self._hd_decoder = AdpcmDecoder()
        self._fft_decoder = AdpcmDecoder()
        self._sequence = 0
        self._events: "asyncio.Queue[Optional[Any]]" = asyncio.Queue()
        self._reader: Optional[asyncio.Task] = None
        self._on_config: Optional[Callable[[OpenWebRXConfig], None]] = None

    # -- connection lifecycle ------------------------------------------------

    async def __aenter__(self) -> "OpenWebRXClient":
        await self.connect()
        return self

    async def __aexit__(self, *exc_info: Any) -> None:
        await self.close()

    async def connect(self) -> OpenWebRXConfig:
        """Open the WebSocket, perform the handshake and start the DSP.

        Returns the server's :class:`OpenWebRXConfig`.
        """
        self._events = asyncio.Queue()
        await self._establish()
        self._reader = asyncio.create_task(self._read_loop())
        return self.config  # type: ignore[return-value]

    async def _establish(self) -> None:
        """Open the socket, exchange the handshake and start the DSP.

        The handshake is completed before the background reader starts so that
        only one coroutine ever reads from the socket.
        """
        self._ws = await websockets.connect(
            self.url, max_size=None, open_timeout=self.open_timeout
        )
        await self._ws.send(HANDSHAKE_CLIENT.format(client=self.client_name))
        self._audio_decoder.reset()
        self._hd_decoder.reset()
        self._fft_decoder.reset()
        self._config_parts = {}
        self._sequence = 0
        await self._await_config()
        await self._send_tuning()
        await self._send_json({"type": "dspcontrol", "action": "start"})

    async def close(self) -> None:
        """Close the connection and cancel the background reader."""
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
        frequency: Optional[int] = None,
        *,
        modulation: Optional[str] = None,
        bandwidth: Optional[int] = None,
        profile: Optional[str] = None,
        secondary: Optional[Dict[str, Any]] = None,
        **params: Any,
    ) -> None:
        """Retune the receiver.

        *frequency* is in Hz. *modulation* accepts names such as ``"lsb"``,
        ``"usb"``, ``"am"`` or ``"nfm"`` (case-insensitive). Any additional
        keyword arguments are forwarded verbatim to the server's ``dspcontrol``
        message, which is how server-specific parameters are reached.
        """
        tuning: Dict[str, Any] = dict(self._tuning)
        if frequency is not None:
            tuning["frequency"] = int(frequency)
        if modulation is not None:
            key = modulation.upper()
            tuning["modulation"] = OPENWEBRX_MODES.get(key, modulation.lower())
        if bandwidth is not None:
            tuning["bandwidth"] = int(bandwidth)
        if profile is not None:
            tuning["profile"] = profile
        if secondary is not None:
            tuning["secondary_mod"] = secondary
        tuning.update(params)
        self._tuning = tuning
        if self._ws is not None:
            await self._send_tuning()

    async def _send_tuning(self) -> None:
        if not self._tuning or self._ws is None:
            return
        message = {"type": "dspcontrol"}
        message.update(self._tuning)
        await self._send_json(message)

    async def _send_json(self, payload: Dict[str, Any]) -> None:
        if self._ws is not None:
            await self._ws.send(json.dumps(payload))

    # -- message handling ----------------------------------------------------

    async def _await_config(self) -> None:
        """Read until the server has sent enough configuration to tune.

        The configuration arrives as several ``config`` messages and only the
        last one carries the centre frequency, so reading stops once that
        field is known. A short grace period covers servers that never report
        it.
        """
        self._config_parts = {}
        deadline: Optional[float] = None
        loop = asyncio.get_running_loop()
        while True:
            timeout = self.open_timeout
            if deadline is not None:
                timeout = max(0.0, deadline - loop.time())
                if timeout == 0.0:
                    return
            message = await asyncio.wait_for(self._ws.recv(), timeout=timeout)
            if isinstance(message, bytes):
                continue
            if message.startswith(HANDSHAKE_SERVER_PREFIX):
                await self._send_json(
                    {
                        "type": "connectionproperties",
                        "params": {
                            "output_rate": self.output_rate,
                            "hd_output_rate": self.hd_output_rate,
                        },
                    }
                )
                continue
            if self._handle_text(message):
                return
            if self._config_parts and deadline is None:
                deadline = loop.time() + 2.0

    def _handle_text(self, message: str) -> bool:
        """Handle a JSON control message; return ``True`` once config is known."""
        try:
            payload = json.loads(message)
        except ValueError:
            return False
        kind = payload.get("type")
        value = payload.get("value")
        if kind == "config":
            # OpenWebRX splits the configuration over several messages, so
            # they must be merged rather than replaced.
            self._config_parts.update(value or {})
            self.config = OpenWebRXConfig.from_dict(self._config_parts)
            if self._on_config is not None:
                self._on_config(self.config)
            self._push({"type": "config", "value": self.config})
            return "center_freq" in self._config_parts
        if kind == "profiles":
            self.profiles = value or []
            self._push({"type": "profiles", "value": self.profiles})
        elif kind == "modes":
            self.modes = value or []
            self._push({"type": "modes", "value": self.modes})
        elif kind == "smeter":
            self.smeter = value
            self._push({"type": "smeter", "value": value})
        elif kind == "receiver_details":
            self.receiver_details = value or {}
        elif kind == "clients":
            self.clients = value if isinstance(value, int) else 0
        elif kind == "secondary_config":
            self._push({"type": "secondary_config", "value": value})
        return False

    def _push(self, event: Any) -> None:
        self._events.put_nowait(event)

    async def _read_loop(self) -> None:
        """Read frames, reconnect on failure and publish events."""
        delay = self.reconnect_initial_delay
        while True:
            try:
                while True:
                    message = await asyncio.wait_for(
                        self._ws.recv(), timeout=self.idle_timeout
                    )
                    delay = self.reconnect_initial_delay
                    if isinstance(message, bytes):
                        self._handle_binary(message)
                    else:
                        self._handle_text(message)
            except asyncio.CancelledError:
                raise
            except Exception as exc:  # network failure, timeout, protocol error
                self.last_error = exc
                self._push({"type": "error", "value": exc})
                if not self.reconnect:
                    self._push(None)
                    return
                await self._events.put({"type": "reconnecting", "value": delay})
                await asyncio.sleep(delay + random.random() * delay * 0.1)
                delay = min(delay * 2, self.reconnect_max_delay)
                try:
                    await self._establish()
                except Exception as inner:
                    self.last_error = inner
                    continue

    def _handle_binary(self, message: bytes) -> None:
        if not message:
            return
        opcode = message[0]
        payload = message[1:]
        if opcode == OPCODE_AUDIO:
            compressed = (self.config.audio_compression or "none") == "adpcm"
            samples = self._decode(payload, compressed, self._audio_decoder, sync=True)
            if samples:
                self._publish_audio(samples, self.output_rate, compressed, "audio")
        elif opcode == OPCODE_HD_AUDIO:
            compressed = (self.config.hd_audio_compression or "none") == "adpcm"
            samples = self._decode(payload, compressed, self._hd_decoder, sync=True)
            if samples:
                self._publish_audio(samples, self.hd_output_rate, compressed, "hd_audio")
        elif opcode in (OPCODE_FFT, OPCODE_SECONDARY_FFT):
            compressed = (self.config.fft_compression or "none") == "adpcm"
            if compressed:
                samples = self._fft_decoder.decode(payload)
            else:
                samples = self._decode(payload, False, None, sync=False)
            if samples:
                self._push(
                    {
                        "type": "fft",
                        "value": samples,
                        "secondary": opcode == OPCODE_SECONDARY_FFT,
                    }
                )

    @staticmethod
    def _decode(
        payload: bytes,
        compressed: bool,
        decoder: Optional[AdpcmDecoder],
        *,
        sync: bool,
    ) -> List[int]:
        if compressed and decoder is not None:
            return decoder.decode_with_sync(payload) if sync else decoder.decode(payload)
        usable = len(payload) - (len(payload) % 2)
        return list(struct.unpack("<%dh" % (usable // 2), payload[:usable]))

    def _publish_audio(
        self, samples: List[int], samplerate: int, compressed: bool, kind: str
    ) -> None:
        chunk = AudioChunk(
            samples=samples,
            samplerate=samplerate,
            sequence=self._sequence,
            compressed=compressed,
            kind=kind,
        )
        self._sequence += 1
        self._push({"type": "audio", "value": chunk})

    # -- public streaming API ------------------------------------------------

    async def audio(self) -> AsyncIterator[AudioChunk]:
        """Yield :class:`AudioChunk` objects until the connection closes."""
        while True:
            event = await self._events.get()
            if event is None:
                return
            if event.get("type") == "audio":
                yield event["value"]

    async def events(self) -> AsyncIterator[Dict[str, Any]]:
        """Yield every event (audio, fft, smeter, config, errors)."""
        while True:
            event = await self._events.get()
            if event is None:
                return
            yield event

    async def await_metadata(self, timeout: float = 2.0) -> None:
        """Drain events until the mode and profile lists have arrived.

        The server pushes ``modes``, ``profiles`` and ``smeter`` after the
        handshake, so callers that want them must keep reading. Reading stops
        once both lists are known or *timeout* elapses, whichever comes first.
        """
        seen = set()
        loop = asyncio.get_running_loop()
        deadline = loop.time() + timeout
        while "modes" not in seen or "profiles" not in seen:
            remaining = deadline - loop.time()
            if remaining <= 0:
                return
            try:
                event = await asyncio.wait_for(self._events.get(), timeout=remaining)
            except asyncio.TimeoutError:
                return
            if event is None:
                return
            kind = event.get("type")
            if kind in ("modes", "profiles"):
                seen.add(kind)

    async def record(
        self,
        duration: float,
        *,
        include_fft: bool = False,
    ) -> List[AudioChunk]:
        """Collect audio for *duration* seconds and return the chunks."""
        collected: List[AudioChunk] = []
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
            elif include_fft and event.get("type") == "fft":
                pass
        return collected

    @staticmethod
    def concat(chunks: List[AudioChunk]) -> List[int]:
        """Flatten a list of chunks into a single sample list."""
        out: List[int] = []
        for chunk in chunks:
            out.extend(chunk.samples)
        return out
