"""Decoder for the WebSDR audio stream.

WebSDR does not send PCM. It sends a compact stream that mixes two sample
sources:

* ``0x80`` frames: 128 samples looked up from a 256-entry companding table.
* LPC frames: 128 samples reconstructed by a 20th-order LPC synthesis filter
  driven by an adaptive quantiser.

Everything else in the stream is metadata: sample rate, filter coefficients,
AGC state, tuned frequency, S-meter, timestamps.

This module reimplements the decoder from the public WebSDR HTML5 client
(``websdr-sound.js``, copyright 2013-2025 PA3FWM). JavaScript's ``<<``, ``>>``
and ``|`` operators coerce to 32-bit signed integers, and the reference code
depends on that behaviour for quantiser overflow, so those semantics are
reproduced explicitly here.
"""

from __future__ import annotations

from typing import List, Union

from ._websdr_table import SAMPLE_TABLE

#: Quantiser magnitude thresholds, indexed by ``G``.
_QUANTIZER_Z = (999, 999, 8, 4, 2, 1, 99, 99)

_INT32 = 0xFFFFFFFF


def _i32(value: int) -> int:
    """Coerce to a signed 32-bit integer, as JavaScript bitwise ops do."""
    value &= _INT32
    return value - 0x100000000 if value >= 0x80000000 else value


def _to_int32(value: float) -> int:
    """Coerce a float to int32 using JavaScript ``ToInt32`` semantics."""
    return _i32(int(value))


def _i16(value: float) -> int:
    """Coerce to a signed 16-bit integer, as ``Int16Array`` assignment does."""
    value = int(value) & 0xFFFF
    return value - 0x10000 if value >= 0x8000 else value


class WebSdrVocoder:
    """Stateful decoder for WebSDR stream messages.

    Feed each WebSocket binary message to :meth:`decode`; it returns the PCM
    samples produced by that message. Decoder state is carried across calls.
    """

    def __init__(self) -> None:
        self._n = [0] * 20
        self._o = [0] * 20
        self._fa = 0
        self._g = 1
        self._oa = 0
        self._aa = 0
        self.samplerate = 8000
        self._out: List[int] = []
        self._j = 14

    def reset(self) -> None:
        """Return the decoder to its initial state."""
        self.__init__()

    @property
    def agc(self) -> int:
        """Current AGC gain parameter reported by the server."""
        return self._oa

    @property
    def audio_flags(self) -> int:
        """Raw audio control byte; bit 4 selects the high-quality filter."""
        return self._aa

    def _emit(self, value: int) -> None:
        self._out.append(_i16(value))

    def _decode_lpc_block(self, data: bytes, a: int, m: int) -> int:
        """Reconstruct 128 samples and return the new byte offset."""
        n, o = self._n, self._o
        length = len(data)
        for _ in range(128):
            def byte(index: int) -> int:
                return data[index] if 0 <= index < length else 0

            f = _i32((byte(a + 3) & 255) | ((byte(a + 2) & 255) << 8)
                     | ((byte(a + 1) & 255) << 16) | ((byte(a) & 255) << 24))
            f = _i32(f << m)
            e = 0
            r = 15 - self._g
            w = self._oa
            if f != 0:
                while not (f & 0x80000000) and e < r:
                    f = _i32(f << 1)
                    e += 1
            if e < r:
                r = e
                e += 1
                f = _i32(f << 1)
            else:
                r = (f >> 24) & 255
                e += 8
                f = _i32(f << 8)

            s = 0
            if r >= _QUANTIZER_Z[self._g]:
                s += 1
            if r >= _QUANTIZER_Z[self._g - 1]:
                s += 1
            if s > self._g - 1:
                s = self._g - 1

            z = (((f >> 16) & 0xFFFF) >> (17 - self._g)) & (-1 << s)
            z += r << (self._g - 1)
            if f & (1 << (32 - self._g + s)):
                z |= (1 << s) - 1
                z = ~z

            m += e + self._g - s
            while m >= 8:
                a += 1
                m -= 8

            acc = 0
            for t in range(20):
                acc += n[t] * o[t]
            acc = _to_int32(acc)
            acc = (acc >> 12) if acc >= 0 else ((acc + 4095) >> 12)

            w = z * w + w / 2.0
            z = _to_int32(w) >> 4
            for t in range(19, -1, -1):
                n[t] = _i32(n[t] + (-(_i32(n[t]) >> 7) + (_to_int32(o[t] * z) >> self._j)))
                if t == 0:
                    break
                o[t] = o[t - 1]
            o[0] = acc + w

            sample = o[0] + (_i32(self._fa) >> 4)
            self._fa = 0 if (self._aa & 16) else _i32(self._fa + ((_to_int32(o[0]) << 4) >> 3))
            self._emit(sample)

        if m == 0:
            a -= 1
        return a

    def _fill_table(self, data: bytes, a: int) -> None:
        """Handle a ``0x80`` frame: 128 table-looked-up samples.

        The reference client indexes past the end of the message when a frame
        is truncated and relies on ``undefined`` coercing to zero, so missing
        bytes contribute silence rather than shortening the frame.
        """
        append = self._out.append
        length = len(data)
        for index in range(a + 1, a + 129):
            if index < length:
                append(_i16(SAMPLE_TABLE[data[index]]))
            else:
                append(0)
        self._reset_filter()

    def _fill_silence(self) -> None:
        """Handle a ``0x84`` frame: 128 zero samples."""
        self._out.extend([0] * 128)
        self._reset_filter()

    def _reset_filter(self) -> None:
        self._n = [0] * 20
        self._o = [0] * 20
        self._fa = 0

    def decode(self, data: Union[bytes, bytearray, memoryview]) -> List[int]:
        """Decode one WebSDR stream message into PCM samples."""
        data = bytes(data)
        self._out = []
        length = len(data)
        a = 0
        while a < length:
            c = data[a]
            if (c & 0xF0) == 0xF0:
                a += 2
            elif c == 0x80:
                self._fill_table(data, a)
                a += 129
            elif 0x90 <= c <= 0xDF:
                self._g = 14 - (c >> 4)
                self._j = 12 if (self._aa & 16) else 14
                a = self._decode_lpc_block(data, a, 4) + 1
            elif (c & 0x80) == 0:
                self._j = 12 if (self._aa & 16) else 14
                a = self._decode_lpc_block(data, a, 1) + 1
            elif c == 0x81:
                self.samplerate = 256 * data[a + 1] + data[a + 2]
                a += 3
            elif c == 0x82:
                self._oa = 256 * data[a + 1] + data[a + 2]
                a += 3
            elif c == 0x83:
                self._aa = data[a + 1]
                a += 2
            elif c == 0x84:
                self._fill_silence()
                a += 1
            elif c == 0x85:
                a += 7
            elif c == 0x86:
                a += 1
            elif c == 0x87:
                a += 7
            else:
                a += 1
        return self._out
