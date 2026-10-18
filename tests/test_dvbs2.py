"""Tests for carving non-text (binary) protocols out of a capture.

The layouts are the two Don't Look Up documents, which are TFTP DATA packets
with fixed block sizes. Captures are built in the tests, so nothing here needs
a receiver or a network.
"""

import struct
import unittest

from sdrstream import dvbs2


def packet(block, payload):
    """One TFTP DATA packet: opcode 3, block number, payload."""
    return struct.pack(">HH", 3, block) + payload


def stream(*blocks, block_size=512, filler=None):
    return b"".join(
        packet(block, filler if filler is not None else bytes([block & 0xFF])
               * block_size)
        for block in blocks)


class TftpDataTest(unittest.TestCase):
    def test_a_packet_round_trips(self):
        original = dvbs2.TftpData(block=7, data=b"hello")
        parsed = dvbs2.TftpData.parse(original.to_bytes())
        self.assertEqual(parsed.block, 7)
        self.assertEqual(parsed.data, b"hello")
        self.assertEqual(parsed.opcode, 3)

    def test_a_short_frame_is_rejected(self):
        with self.assertRaises(ValueError):
            dvbs2.TftpData.parse(b"\x00\x03\x00")

    def test_a_wrong_opcode_is_rejected(self):
        # Opcode 1 is a read request, not data.
        with self.assertRaises(ValueError):
            dvbs2.TftpData.parse(struct.pack(">HH", 1, 1) + b"x" * 512)

    def test_the_block_size_can_be_enforced(self):
        frame = packet(1, b"x" * 100)
        self.assertEqual(len(dvbs2.TftpData.parse(frame).data), 100)
        with self.assertRaises(ValueError):
            dvbs2.TftpData.parse(frame, payload_size=512)

    def test_length_includes_the_header(self):
        self.assertEqual(len(dvbs2.TftpData(block=1, data=b"x" * 512)), 516)


class CarveTest(unittest.TestCase):
    def test_a_clean_516_stream_is_fully_recovered(self):
        found = dvbs2.carve_516(stream(1, 2, 3, 4))
        self.assertEqual([f.block for f in found.frames], [1, 2, 3, 4])
        self.assertEqual(len(found.payload()), 2048)
        self.assertEqual(found.gaps, [])

    def test_a_clean_1028_stream_is_fully_recovered(self):
        found = dvbs2.carve_1028(stream(1, 2, block_size=1024))
        self.assertEqual([f.block for f in found.frames], [1, 2])
        self.assertEqual(len(found.payload()), 2048)

    def test_bytes_before_and_after_frames_become_gaps(self):
        blob = b"\xde\xad\xbe\xef" + stream(1) + b"\xff" * 10
        found = dvbs2.carve_516(blob)
        self.assertEqual(len(found.frames), 1)
        self.assertEqual(found.gaps, [(0, 4), (520, 530)])

    def test_a_truncated_trailing_frame_is_ignored(self):
        blob = stream(1) + packet(2, b"x" * 512)[:100]
        found = dvbs2.carve_516(blob)
        self.assertEqual([f.block for f in found.frames], [1])

    def test_a_516_stream_yields_nothing_when_carved_as_1028(self):
        # The header is identical in both layouts, so the payload size is what
        # decides. One 516 byte frame is not long enough to hold a 1028 byte
        # one, so nothing is found.
        found = dvbs2.carve_1028(stream(1))
        self.assertEqual(found.frames, [])

    def test_a_1028_stream_can_be_mis_split_as_516(self):
        # The reverse is not safe: a 1028-byte frame is long enough to satisfy
        # a 516-byte read, so a 516 carve of a 1028 stream produces shifted
        # frames. This is exactly why best_carve() compares payload sizes
        # instead of trusting one layout.
        found = dvbs2.carve_516(stream(1, block_size=1024))
        self.assertEqual(len(found.frames), 1)
        self.assertNotEqual(found.frames[0].data,
                            stream(1, block_size=1024)[4:])

    def test_a_magic_inside_a_payload_does_not_split_a_frame(self):
        payload = b"\x00\x03" + b"A" * 510
        found = dvbs2.carve_516(packet(1, payload))
        self.assertEqual(len(found.frames), 1)
        self.assertEqual(found.frames[0].data, payload)

    def test_the_frame_keeps_its_offset_and_raw_bytes(self):
        blob = b"\x00" * 7 + stream(9)
        found = dvbs2.carve_516(blob)
        self.assertEqual(found.frames[0].offset, 7)
        self.assertEqual(found.frames[0].raw, stream(9))
        self.assertEqual(found.frames[0].end, 7 + 516)

    def test_an_empty_capture_yields_nothing(self):
        found = dvbs2.carve_516(b"")
        self.assertEqual(found.frames, [])
        self.assertEqual(found.payload(), b"")


class OrderingTest(unittest.TestCase):
    def test_payload_keeps_capture_order(self):
        found = dvbs2.carve_516(stream(3, 1, 2))
        self.assertEqual([f.block for f in found.frames], [3, 1, 2])

    def test_ordered_sorts_by_block_number(self):
        found = dvbs2.carve_516(stream(3, 1, 2))
        self.assertEqual([f.block for f in found.ordered()], [1, 2, 3])

    def test_ordered_payload_follows_block_order(self):
        blob = packet(2, b"B" * 512) + packet(1, b"A" * 512)
        found = dvbs2.carve_516(blob)
        self.assertEqual(found.ordered_payload()[:1], b"A")
        self.assertEqual(found.payload()[:1], b"B")

    def test_a_wrapped_counter_still_orders_correctly(self):
        # A long transfer wraps 65535 -> 0; a plain numeric sort would put the
        # tail of the stream first.
        found = dvbs2.carve_516(stream(0, 1, 65534, 65535))
        self.assertEqual([f.block for f in found.ordered()],
                         [65534, 65535, 0, 1])

    def test_a_single_frame_orders_trivially(self):
        self.assertEqual([f.block for f in dvbs2.carve_516(stream(7)).ordered()],
                         [7])

    def test_out_of_order_is_detected(self):
        self.assertTrue(dvbs2.carve_516(stream(2, 1)).out_of_order())
        self.assertFalse(dvbs2.carve_516(stream(1, 2)).out_of_order())


class HolesTest(unittest.TestCase):
    def test_a_contiguous_stream_has_no_holes(self):
        found = dvbs2.carve_516(stream(1, 2, 3, 4))
        self.assertEqual(found.missing(), [])

    def test_a_missing_block_is_reported(self):
        found = dvbs2.carve_516(stream(1, 2, 5, 6))
        self.assertEqual(found.missing(), [3, 4])

    def test_a_hole_is_found_even_when_frames_arrive_out_of_order(self):
        found = dvbs2.carve_516(stream(3, 1, 5))
        self.assertEqual(found.missing(), [2, 4])

    def test_a_wrap_is_not_reported_as_a_hole(self):
        found = dvbs2.carve_516(stream(65535, 0, 1))
        self.assertEqual(found.missing(), [])

    def test_duplicate_blocks_are_reported(self):
        found = dvbs2.carve_516(stream(1, 1, 2))
        self.assertEqual(found.duplicates(), [1])

    def test_no_duplicates_in_a_clean_stream(self):
        self.assertEqual(dvbs2.carve_516(stream(1, 2, 3)).duplicates(), [])

    def test_the_summary_mentions_holes_and_duplicates(self):
        text = dvbs2.carve_516(stream(1, 1, 4)).summary()
        self.assertIn("missing blocks", text)
        self.assertIn("duplicate blocks", text)


class BestCarveTest(unittest.TestCase):
    def test_the_layout_with_more_payload_wins(self):
        blob = stream(1, 2, 3, block_size=1024)
        self.assertEqual(dvbs2.best_carve(blob).payload_size, dvbs2.BLOCK_1028)

    def test_a_516_capture_prefers_516(self):
        blob = stream(1, 2, 3, 4)
        self.assertEqual(dvbs2.best_carve(blob).payload_size, dvbs2.BLOCK_516)

    def test_an_empty_capture_falls_back_to_516(self):
        self.assertEqual(dvbs2.best_carve(b"").payload_size, dvbs2.BLOCK_516)

    def test_a_single_payload_size_can_be_requested(self):
        # Forced to the 516 layout, a 1028 stream still carves as 516 frames,
        # which is exactly the ambiguity best_carve() resolves by comparison.
        blob = stream(1, 2, block_size=1024)
        found = dvbs2.best_carve(blob, (dvbs2.BLOCK_516,))
        self.assertEqual(found.payload_size, dvbs2.BLOCK_516)
        self.assertGreater(len(found.payload()), 0)


class ReassembleTest(unittest.TestCase):
    def test_ordered_reassembly_is_the_default(self):
        blob = packet(2, b"B" * 512) + packet(1, b"A" * 512)
        self.assertEqual(dvbs2.reassemble(blob)[:1], b"A")

    def test_capture_order_can_be_requested(self):
        blob = packet(2, b"B" * 512) + packet(1, b"A" * 512)
        self.assertEqual(dvbs2.reassemble(blob, ordered=False)[:1], b"B")

    def test_the_1028_layout_can_be_reassembled(self):
        blob = stream(1, 2, block_size=1024, filler=b"Z" * 1024)
        self.assertEqual(dvbs2.reassemble(blob, dvbs2.BLOCK_1028), b"Z" * 2048)


if __name__ == "__main__":
    unittest.main()
