"""KiwiSDR client.

KiwiSDR is the other large public receiver network (several hundred public
servers, listed alongside OpenWebRX on receiverbook.de). Its protocol is
documented by the server source: a WebSocket per connection at
``/``+epoch``/SND`` carrying short ``SET`` commands and binary frames tagged
with a three byte marker.

The one step that is easy to miss is the rate agreement: the server will not
start the audio stream until the client has sent ``SET AR OK in=<rate>
out=<rate>``. Every frame of silence and no error shows up instead, which took
a live trace to work out.

Reference: jks-prv/KiwiSDR ``net/net.cpp`` (internal connect sequence) and
jks-prv/kiwiclient ``kiwi/client.py`` (frame formats).
"""

from __future__ import annotations

import asyncio
import random
import struct
import time
import urllib.parse
from dataclasses import dataclass
from typing import Any, AsyncIterator, Dict, List, Optional

import websockets

from .common import normalize_http_url, websocket_url

#: Server frame markers. MSG frames carry a space separated name=value body.
TAG_MSG = b"MSG"
TAG_SND = b"SND"

SND_FLAG_ADC_OVFL = 0x02
SND_FLAG_STEREO = 0x08
SND_FLAG_COMPRESSED = 0x10
SND_FLAG_LITTLE_ENDIAN = 0x80

#: KiwiSDR names the audio modes its own way, and the passband the browser
#: client uses for each. Frequencies the server is given are in kHz.
DEFAULT_MODES: Dict[str, List[int]] = {
    "am": [-4900, 4900],
    "amn": [-2500, 2500],
    "amw": [-6000, 6000],
    "lsb": [-2700, -300],
    "usb": [300, 2700],
    "cw": [300, 700],
    "cwn": [470, 530],
    "fm": [-6000, 6000],
    "nbfm": [-6000, 6000],
    "iq": [-5000, 5000],
}


@dataclass
class KiwiChunk:
    """One block of demodulated audio samples (signed 16 bit)."""

    samples: List[int]
    samplerate: int
    sequence: int = 0
    compressed: bool = False
    flags: int = 0
    rssi: float = 0.0


class _ImaAdpcm:
    """Four bit IMA ADPCM, low nibble first, as KiwiSDR sends it.

    Same codec idea as the OpenWebRX audio decoder but without the SYNC
    marker stream, so it is a separate implementation.
    """

    _STEPS = (7, 8, 9, 10, 11, 12, 13, 14, 16, 17, 19, 21, 23, 25, 28, 31,
              34, 37, 41, 45, 50, 55, 60, 66, 73, 80, 88, 97, 107, 118, 130,
              143, 157, 173, 190, 209, 230, 253, 279, 307, 337, 371, 408,
              449, 494, 544, 598, 658, 724, 796, 876, 963, 1060, 1166, 1282,
              1411, 1552, 1707, 1878, 2066, 2272, 2499, 2749, 3024)

    _INDEX = (-1, -1, -1, -1, 2, 4, 6, 8)

    def __init__(self) -> None:
        self.index = 0
        self.prev = 0

    def reset(self, index: int = 0, prev: int = 0) -> None:
        self.index = index
        self.prev = prev

    def decode(self, data: bytes) -> List[int]:
        out: List[int] = []
        steps = self._STEPS
        adjust = self._INDEX
        for byte in data:
            for code in (byte & 0x0F, (byte >> 4) & 0x0F):
                step = steps[self.index]
                difference = step >> 3
                if code & 1:
                    difference += step >> 2
                if code & 2:
                    difference += step >> 1
                if code & 4:
                    difference += step
                if code & 8:
                    difference = -difference
                sample = self.prev + difference
                sample = -32768 if sample < -32768 else (32767 if sample > 32767 else sample)
                self.prev = sample
                new_index = self.index + adjust[code & 7]
                self.index = 0 if new_index < 0 else (len(steps) - 1 if new_index > len(steps) - 1 else new_index)
                out.append(sample)
        return out


def kiwi_stream_path() -> str:
    """The stream URI the server expects, epoch stamped."""
    epoch = int(time.time())
    return "/%d/SND" % epoch


def split_server_url(url: str) -> str:
    """host[:port] from any scheme the directory may hand us."""
    url = normalize_http_url(url)
    parsed = urllib.parse.urlsplit(url)
    return parsed.netloc


class KiwiSDRClient:
    """Connect, tune and record from one KiwiSDR server.

    Audio arrives already demodulated at roughly 12 kHz, so it drops straight
    into the WAV writer the other clients share.
    """

    def __init__(
        self,
        url: str,
        *,
        password: str = "",
        ident_user: str = "sdrstream",
        reconnect: bool = True,
        reconnect_initial_delay: float = 1.0,
        reconnect_max_delay: float = 30.0,
        open_timeout: float = 15.0,
        idle_timeout: float = 30.0,
    ) -> None:
        self.url = url
        self.base = split_server_url(url) if "://" in url else normalize_http_url(url).replace("http://", "").replace("https://", "")
        self.password = password
        self.ident_user = ident_user
        self.reconnect = reconnect
        self.reconnect_initial_delay = reconnect_initial_delay
        self.reconnect_max_delay = reconnect_max_delay
        self.open_timeout = open_timeout
        self.idle_timeout = idle_timeout
        self._ws = None
        self._events: asyncio.Queue = asyncio.Queue()
        self._reader: Optional[asyncio.Task] = None
        self._adpcm = _ImaAdpcm()
        self._tuning: Optional[str] = None
        self._frequency_khz: Optional[float] = None
        self._modulation = "usb"
        self._low: Optional[int] = None
        self._high: Optional[int] = None
        self.samplerate = 12000
        self.bandwidth_khz: Optional[float] = None
        self.version: Optional[str] = None
        self.rx_chans: Optional[int] = None
        self.gps: Optional[tuple] = None
        self.status: Dict[str, str] = {}
        self._last_keepalive = 0.0
        self._closed = False

    async def __aenter__(self) -> "KiwiSDRClient":
        await self.connect()
        return self

    async def __aexit__(self, *exc_info: Any) -> None:
        await self.close()

    async def connect(self) -> None:
        """Open the stream and start the handshake the server requires."""
        target = self.base if "://" not in self.base else self.base
        if "://" in self.url:
            url = websocket_url(normalize_http_url(self.url), kiwi_stream_path())
        else:
            url = "ws://%s%s" % (self.base, kiwi_stream_path())
        self._ws = await websockets.connect(
            url, max_size=None, open_timeout=self.open_timeout, ping_interval=None
        )
        self._adpcm.reset()
        self._events = asyncio.Queue()
        self._closed = False
        await self._send("SET auth t=kiwi p=%s" % (self.password or ""))
        await self._send("SET AR OK in=12000 out=44100")
        await self._send("SET agc=1 hang=0 thresh=-100 slope=6 decay=1000 manGain=50")
        if self._tuning:
            await self._send(self._tuning)
        await self._send("SET ident_user=%s" % (self.ident_user or "sdrstream"))
        self._reader = asyncio.create_task(self._read_loop())

    async def _send(self, message: str) -> None:
        if self._ws is None:
            return
        await self._ws.send(message)

    async def close(self) -> None:
        self._closed = True
        if self._reader is not None:
            self._reader.cancel()
            try:
                await self._reader
            except (asyncio.CancelledError, Exception):
                pass
            self._reader = None
        if self._ws is not None and self._ws.state.name != "CLOSED":
            try:
                await self._ws.close()
            except Exception:
                pass
        self._ws = None
        await self._events.put(None)

    async def tune(
        self,
        frequency_khz: Optional[float] = None,
        *,
        mode: str = "usb",
        low: Optional[int] = None,
        high: Optional[int] = None,
    ) -> None:
        """Tune. *frequency_khz* is in kHz; *low*/*high* are Hz passband edges."""
        self._modulation = mode.lower()
        self._frequency_khz = frequency_khz if frequency_khz is not None else self._frequency_khz
        if low is None or high is None:
            default = DEFAULT_MODES.get(self._modulation)
            if default is None:
                raise ValueError("unknown kiwi mode %r; known: %s"
                                 % (self._modulation, ", ".join(sorted(DEFAULT_MODES))))
            low = default[0] if low is None else low
            high = default[1] if high is None else high
        self._low, self._high = low, high
        self._tuning = "SET mod=%s low_cut=%d high_cut=%d freq=%.3f" % (
            self._modulation, low, high, float(self._frequency_khz))
        if self._ws is not None:
            await self._send(self._tuning)

    async def _read_loop(self) -> None:
        delay = self.reconnect_initial_delay
        try:
            while not self._closed:
                try:
                    message = await asyncio.wait_for(self._ws.recv(), timeout=self.idle_timeout)
                except asyncio.TimeoutError:
                    await self._closed_check()
                    continue
                except websockets.ConnectionClosed:
                    if not self.reconnect:
                        break
                    await self._events.put({"type": "reconnecting", "value": delay})
                    await asyncio.sleep(delay + random.random() * delay * 0.1)
                    delay = min(delay * 2, self.reconnect_max_delay)
                    await self._reconnect()
                    delay = self.reconnect_initial_delay
                    continue
                if isinstance(message, str):
                    continue
                self._handle_binary(message)
        except asyncio.CancelledError:
            pass

    async def _closed_check(self) -> None:
        now = time.monotonic()
        if now - self._last_keepalive > 1.0:
            self._last_keepalive = now
            await self._send("SET keepalive")

    async def _reconnect(self) -> None:
        if self._ws is not None and self._ws.state.name != "CLOSED":
            try:
                await self._ws.close()
            except Exception:
                pass
        await self.connect()

    def _handle_binary(self, message: bytes) -> None:
        tag, body = message[:3], message[3:]
        if tag == TAG_MSG:
            self._handle_msg(body)
        elif tag == TAG_SND:
            self._handle_snd(body)
        # "W/F" waterfall frames arrive on their own connection, so they are
        # not handled here.

    def _handle_msg(self, body: bytes) -> None:
        text = body[1:].decode("utf-8", "replace")
        for pair in text.split(" "):
            if "=" not in pair:
                name, value = pair, None
            else:
                name, value = pair.split("=", 1)
            self.status[name] = value
            if name == "sample_rate" and value:
                try:
                    self.samplerate = float(value)
                except ValueError:
                    pass
            elif name == "bandwidth" and value:
                try:
                    self.bandwidth_khz = float(value)
                except ValueError:
                    pass
            elif name == "version_maj":
                self._version_maj = value
            elif name == "version_min":
                self._version_min = value
                if getattr(self, "_version_maj", None) is not None:
                    self.version = "%s.%s" % (self._version_maj, self._version_min)
            elif name == "rx_chans":
                with_context = self.status.get("firmware_sel", "")
                try:
                    self.rx_chans = int(value)
                except ValueError:
                    pass
                    self.rx_chans = None
            elif name == "load_cfg" and value:
                self._handle_load_cfg(value)
            self._events.put_nowait({"type": "msg", "name": name, "value": value})

    def _handle_load_cfg(self, value: str) -> None:
        import base64
        import json as _json
        try:
            decoded = urllib.parse.unquote(value)
            config = _json.loads(decoded)
            gps = config.get("rx_gps") or ""
            parts = str(gps).strip("[] ()").split(",")
            if len(parts) >= 2:
                self.gps = (float(parts[0]), float(parts[1]))
        except Exception:
            pass

    def _handle_snd(self, body: bytes) -> None:
        if len(body) < 7:
            return
        flags = body[0]
        (seq,) = struct.unpack("<I", body[1:5])
        (smeter,) = struct.unpack(">H", body[5:7])
        data = body[7:]
        rssi = 0.1 * smeter - 127
        if flags & SND_FLAG_COMPRESSED:
            samples = self._adpcm.decode(data)
            compressed = True
        elif flags & SND_FLAG_LITTLE_ENDIAN:
            count = len(data) // 2
            samples = list(struct.unpack("<%dh" % count, data[: count * 2]))
            compressed = False
        else:
            count = len(data) // 2
            samples = list(struct.unpack(">%dh" % count, data[: count * 2]))
            compressed = False
        if not samples:
            return
        self._events.put_nowait({
            "type": "audio",
            "value": KiwiChunk(samples=samples, samplerate=round(self.samplerate),
                               sequence=seq, compressed=compressed,
                               flags=flags, rssi=rssi),
        })
        self._ades = 0

    async def audio(self) -> AsyncIterator[KiwiChunk]:
        """Yield audio chunks as they arrive."""
        while True:
            event = await self._events.get()
            if event is None:
                break
            if event.get("type") == "audio":
                yield event["value"]

    async def events(self) -> AsyncIterator[Dict[str, Any]]:
        """Yield everything: audio, server messages, reconnect notices."""
        while True:
            event = await self._events.get()
            if event is None:
                break
            yield event

    async def record(self, duration: float) -> List[KiwiChunk]:
        """Collect audio for *duration* seconds."""
        collected: List[KiwiChunk] = []
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
    def concat(chunks: List[KiwiChunk]) -> List[int]:
        samples: List[int] = []
        for chunk in chunks:
            samples.extend(chunk.samples)
        return samples
