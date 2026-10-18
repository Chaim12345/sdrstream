"""Carve binary IP-encapsulation protocols out of a capture.

The other modules in this package speak the *text* control protocols of public
SDR servers. This one handles the non-text contract found underneath them: the
raw byte framing used when IP traffic is carried over a satellite link.

The formats and the "scan every offset for a magic" tactic come from the Don't
Look Up project (https://github.com/ucsdsysnet/dontlookup), which recovers IP
from DVB-S2(X) captures. That project carries two custom layouts, both of which
turn out to be plain TFTP DATA packets (RFC 1350) with a fixed block size:

    00 03            opcode 3, "DATA", big-endian
    <block>          2-byte block number, big-endian
    <data>           512 or 1024 bytes

One capture quirk is preserved deliberately. Don't Look Up writes the 516-byte
layout as the data alone, but the 1028-byte layout as block number plus data,
so the two parsers disagree about whether the block number is part of the
payload. :meth:`Carved.payload` always returns data only; use
:attr:`Frame.raw` when byte-for-byte agreement with that tool matters.

Nothing here decodes audio or needs a network connection; it is pure byte
carving, which is what makes it testable without a receiver.

    >>> stream = carve(b"\\x00\\x03\\x00\\x01" + b"A" * 512)
    >>> stream.frames[0].block
    1
    >>> stream.payload()
    b'AAAA...'
"""

from __future__ import annotations

import struct
from dataclasses import dataclass, field
from typing import Iterator, List, Optional, Sequence, Tuple

#: TFTP DATA opcode, which is the only thing either layout has in common.
MAGIC = b"\x00\x03"

#: Payload sizes of the two documented layouts. The constants are named after
#: the total frame sizes, which is how Don't Look Up labels them, so note the
#: four byte header: BLOCK_516 frames are 512 bytes of data inside 516 bytes.
BLOCK_516 = 512
BLOCK_1028 = 1024

#: Total frame sizes, for when the distinction matters.
FRAME_516 = BLOCK_516 + 4
FRAME_1028 = BLOCK_1028 + 4

#: Sequence numbers are unsigned 16-bit, so they wrap.
MODULUS = 1 << 16

HEADER_SIZE = 4


@dataclass(frozen=True)
class TftpData:
    """One TFTP DATA packet: opcode, block number and payload."""

    block: int
    data: bytes
    opcode: int = 3

    @classmethod
    def parse(cls, frame: bytes, payload_size: Optional[int] = None) -> "TftpData":
        """Read a DATA packet, optionally insisting on an exact data length."""
        if len(frame) < HEADER_SIZE:
            raise ValueError("frame shorter than a TFTP header")
        opcode, block = struct.unpack(">HH", frame[:HEADER_SIZE])
        if opcode != 3:
            raise ValueError("not a TFTP DATA packet (opcode %d)" % opcode)
        data = frame[HEADER_SIZE:]
        if payload_size is not None and len(data) != payload_size:
            raise ValueError("expected %d data bytes, found %d"
                             % (payload_size, len(data)))
        return cls(block=block, data=data, opcode=opcode)

    def to_bytes(self) -> bytes:
        return struct.pack(">HH", self.opcode, self.block) + self.data

    def __len__(self) -> int:
        return HEADER_SIZE + len(self.data)


@dataclass
class Frame:
    """A DATA packet found at a byte offset in the capture."""

    offset: int
    block: int
    data: bytes
    payload_size: int

    @property
    def raw(self) -> bytes:
        """The frame as it sits in the capture, header included."""
        return struct.pack(">HH", 3, self.block) + self.data

    @property
    def end(self) -> int:
        return self.offset + len(self.raw)

    def summary(self) -> str:
        return "offset %-9d block %-6d %d bytes" % (self.offset, self.block,
                                                    len(self.data))


@dataclass
class Carved:
    """Every DATA packet found in one capture, plus what was skipped."""

    payload_size: int
    frames: List[Frame] = field(default_factory=list)
    #: Byte ranges that matched nothing, as ``(start, end)`` pairs.
    gaps: List[Tuple[int, int]] = field(default_factory=list)
    #: Total size of the capture the frames were carved from.
    size: int = 0

    def payload(self) -> bytes:
        """The payloads in the order they appear in the capture.

        This mirrors what Don't Look Up writes out, including its behaviour
        when frames arrive out of order.
        """
        return b"".join(frame.data for frame in self.frames)

    def ordered(self) -> List[Frame]:
        """Frames in block order, tolerating one wrap of the counter.

        The counter is 16 bits, so a long transfer wraps and a plain numeric
        sort would put the tail of the stream first. The wrap sits at the
        largest circular gap between consecutive block numbers, so ordering
        starts just after it. This works whatever order the frames arrived in.
        """
        if not self.frames:
            return []
        blocks = sorted({frame.block for frame in self.frames})
        if len(blocks) == 1:
            return sorted(self.frames, key=lambda frame: frame.block)
        gaps = [((blocks[(index + 1) % len(blocks)] - blocks[index]) % MODULUS,
                 index) for index in range(len(blocks))]
        _, index = max(gaps)
        origin = blocks[(index + 1) % len(blocks)]
        return sorted(self.frames, key=lambda frame: (frame.block - origin) % MODULUS)

    def ordered_payload(self) -> bytes:
        """The payloads reassembled in block order, not capture order."""
        return b"".join(frame.data for frame in self.ordered())

    def missing(self) -> List[int]:
        """Block numbers absent between the lowest and highest seen.

        A non-empty result means the reassembled stream has a hole in it, so
        the payload is not a complete file. The span is measured in block
        order, not capture order, or a frame that arrived early would look
        like a wrap of the counter and produce a near-endless gap.
        """
        ordered = self.ordered()
        if not ordered:
            return []
        first = ordered[0].block
        span = (ordered[-1].block - first) % MODULUS
        present = {(frame.block - first) % MODULUS for frame in ordered}
        return [(first + step) % MODULUS for step in range(span + 1)
                if step not in present]

    def duplicates(self) -> List[int]:
        """Block numbers that appear more than once."""
        seen, repeated = set(), []
        for frame in self.frames:
            if frame.block in seen and frame.block not in repeated:
                repeated.append(frame.block)
            seen.add(frame.block)
        return repeated

    def out_of_order(self) -> bool:
        """Whether the blocks did not arrive in order."""
        return [frame.block for frame in self.frames] != \
               [frame.block for frame in self.ordered()]

    def summary(self) -> str:
        lines = ["%d frame(s) of %d bytes, %d byte(s) of payload"
                 % (len(self.frames), self.payload_size, len(self.payload()))]
        missing = self.missing()
        if missing:
            lines.append("  missing blocks: %s%s"
                         % (", ".join(str(b) for b in missing[:8]),
                            " ..." if len(missing) > 8 else ""))
        if self.duplicates():
            lines.append("  duplicate blocks: %s"
                         % ", ".join(str(b) for b in self.duplicates()))
        if self.out_of_order():
            lines.append("  frames arrived out of order")
        if self.gaps:
            lines.append("  %d gap(s), %d byte(s) not part of a frame"
                         % (len(self.gaps), sum(e - s for s, e in self.gaps)))
        return "\n".join(lines)


def iter_frames(data: bytes, payload_size: int,
                start: int = 0) -> Iterator[Frame]:
    """Yield every DATA packet of exactly *payload_size* bytes in *data*.

    A match is only accepted when the frame fits, because a stray magic near
    the end of a capture would otherwise produce a truncated frame.
    """
    stride = HEADER_SIZE + payload_size
    position = start
    while position + stride <= len(data):
        if data[position:position + 2] != MAGIC:
            position += 1
            continue
        packet = TftpData.parse(data[position:position + stride],
                                payload_size=payload_size)
        yield Frame(offset=position, block=packet.block, data=packet.data,
                    payload_size=payload_size)
        position += stride


def carve(data: bytes, payload_size: int = BLOCK_516) -> Carved:
    """Find every DATA packet in *data* and record the bytes between them."""
    result = Carved(payload_size=payload_size, size=len(data))
    position = 0
    for frame in iter_frames(data, payload_size):
        if frame.offset > position:
            result.gaps.append((position, frame.offset))
        result.frames.append(frame)
        position = frame.end
    if position < len(data):
        result.gaps.append((position, len(data)))
    return result


def carve_516(data: bytes) -> Carved:
    """The 516-byte layout: ``00 03``, block number, 512 bytes of data."""
    return carve(data, BLOCK_516)


def carve_1028(data: bytes) -> Carved:
    """The 1028-byte layout: ``00 03``, block number, 1024 bytes of data."""
    return carve(data, BLOCK_1028)


def best_carve(data: bytes, payload_sizes: Sequence[int] = (BLOCK_516,
                                                          BLOCK_1028)) -> Carved:
    """Carve with each block size and keep whichever finds the most bytes.

    Don't Look Up runs every parser and compares the packet counts, because the
    capture does not say which layout it holds.
    """
    best: Optional[Carved] = None
    for payload_size in payload_sizes:
        found = carve(data, payload_size)
        if best is None or len(found.payload()) > len(best.payload()):
            best = found
    return best if best is not None else Carved(payload_size=BLOCK_516)


def reassemble(data: bytes, payload_size: int = BLOCK_516,
               *, ordered: bool = True) -> bytes:
    """Carve *data* and return just the reassembled payload."""
    found = carve(data, payload_size)
    return found.ordered_payload() if ordered else found.payload()
