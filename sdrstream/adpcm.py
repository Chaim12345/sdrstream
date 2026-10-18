"""IMA ADPCM decoder used by OpenWebRX audio and FFT streams.

OpenWebRX transmits 4-bit IMA ADPCM. Audio payloads are wrapped in ``SYNC``
markers that periodically carry the decoder state, so a stream can be joined
mid-flight and resynchronise. FFT payloads are raw ADPCM with no markers.

The implementation mirrors ``AdpcmDecoder`` from the openwebrx-js client so
that output is bit-identical to the reference. In particular the step size used
for a nibble is the one *after* the index update, and the index update is
applied before clamping.
"""

from __future__ import annotations

from typing import List, Union

IMA_INDEX_TABLE = (-1, -1, -1, -1, 2, 4, 6, 8, -1, -1, -1, -1, 2, 4, 6, 8)

IMA_STEP_TABLE = (
    7, 8, 9, 10, 11, 12, 13, 14, 16, 17, 19, 21, 23, 25, 28, 31, 34, 37, 41, 45,
    50, 55, 60, 66, 73, 80, 88, 97, 107, 118, 130, 143, 157, 173, 190, 209, 230,
    253, 279, 307, 337, 371, 408, 449, 494, 544, 598, 658, 724, 796, 876, 963,
    1060, 1166, 1282, 1411, 1552, 1707, 1878, 2066, 2272, 2499, 2749, 3024, 3327,
    3660, 4026, 4428, 4871, 5358, 5894, 6484, 7132, 7845, 8630, 9493, 10442,
    11487, 12635, 13899, 15289, 16818, 18500, 20350, 22385, 24623, 27086, 29794,
    32767,
)

#: Byte sequence that introduces a decoder-state header in the audio stream.
SYNC_MARKER = b"SYNC"

#: Number of ADPCM bytes decoded between two state headers.
SYNC_INTERVAL = 1000


class AdpcmDecoder:
    """Streaming IMA ADPCM decoder.

    State is retained between calls, so payloads may be fed in arbitrary
    chunks. Use :meth:`decode` for raw data (FFT) and :meth:`decode_with_sync`
    for audio payloads that embed ``SYNC`` headers.
    """

    def __init__(self) -> None:
        self.step_index = 0
        self.predictor = 0
        self.step = 0
        self._synchronized = 0
        self._phase = 0
        self._sync_buffer = bytearray(4)
        self._sync_buffer_index = 0
        self._sync_counter = 0

    def reset(self) -> None:
        """Return the decoder to its initial state."""
        self.__init__()

    def decode_nibble(self, nibble: int) -> int:
        """Decode a single 4-bit sample and return the new predictor."""
        self.step_index += IMA_INDEX_TABLE[nibble]
        self.step_index = min(max(self.step_index, 0), 88)

        diff = self.step >> 3
        if nibble & 1:
            diff += self.step >> 2
        if nibble & 2:
            diff += self.step >> 1
        if nibble & 4:
            diff += self.step
        if nibble & 8:
            diff = -diff

        self.predictor += diff
        self.predictor = min(max(self.predictor, -32768), 32767)

        self.step = IMA_STEP_TABLE[self.step_index]
        return self.predictor

    def decode(self, data: Union[bytes, bytearray, memoryview]) -> List[int]:
        """Decode raw ADPCM, two samples per byte, ignoring sync markers."""
        out: List[int] = []
        append = out.append
        for byte in data:
            append(self.decode_nibble(byte & 0x0F))
            append(self.decode_nibble((byte >> 4) & 0x0F))
        return out

    def decode_with_sync(self, data: Union[bytes, bytearray, memoryview]) -> List[int]:
        """Decode an audio payload that may contain ``SYNC`` state headers."""
        out: List[int] = []
        append = out.append
        phase = self._phase
        for byte in data:
            if phase == 0:
                if byte != SYNC_MARKER[self._synchronized]:
                    self._synchronized = 0
                if byte == SYNC_MARKER[self._synchronized]:
                    self._synchronized += 1
                if self._synchronized == len(SYNC_MARKER):
                    self._sync_buffer_index = 0
                    phase = 1
            elif phase == 1:
                self._sync_buffer[self._sync_buffer_index] = byte
                self._sync_buffer_index += 1
                if self._sync_buffer_index == 4:
                    buf = self._sync_buffer
                    self.step_index = int.from_bytes(buf[0:2], "little", signed=True)
                    self.predictor = int.from_bytes(buf[2:4], "little", signed=True)
                    self._sync_counter = SYNC_INTERVAL
                    phase = 2
            else:
                append(self.decode_nibble(byte & 0x0F))
                append(self.decode_nibble((byte >> 4) & 0x0F))
                self._sync_counter -= 1
                if self._sync_counter <= 0:
                    self._synchronized = 0
                    phase = 0
        self._phase = phase
        return out
