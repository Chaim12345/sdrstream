"""Tests for the OpenWebRX IMA ADPCM decoder.

The expected outputs were generated with the reference ``AdpcmDecoder`` from
the openwebrx-js client, so these tests pin bit-exact compatibility.
"""

import struct
import unittest

from sdrstream.adpcm import AdpcmDecoder, IMA_STEP_TABLE


def _stream():
    parts = [((i * 37 + 11) & 0xFF) for i in range(16)]
    parts += list(b"SYNC")
    state = struct.pack("<hh", 40, -1234)
    parts += list(state)
    parts += [((i * 91 + 7) & 0xFF) for i in range(40)]
    return bytes(parts)


# Produced by openwebrx-js AdpcmDecoder.decodeWithSync on the stream above.
EXPECTED_SYNC = [
    -1234, -1144, -733, 238, -1219, -2577, -2753, -2273,
    -1254, 733, -2959, -7488, -9313, -6546, -2017, -2625,
    -10927, -23979, -32665, -21611, -5818, -12124, -10213, -32768,
    -32768, -2297, 32767, 12289, 23461, 26846, -854, 32767,
    32767, 4098, 22719, 32767, -1088, 32767, 28672, -4846,
    23823, 32767, -11247, 32767, 20481, -20485, 16377, 32767,
    -23096, -27191, -32768, -32768, 12285, 32767, 32767, 14146,
    -9553, -32768, 20477, 32767, 32767, 6698, -23773, -19678,
    32767, 32767, 32767, -751, -32768, -20482, -24206, 26579,
    32767, -8199, -32768, -12290, -23462, -26847, 853, -32768,
]


class TestAdpcmDecoder(unittest.TestCase):
    def test_expected_output_matches_reference(self):
        decoder = AdpcmDecoder()
        out = decoder.decode_with_sync(_stream())
        self.assertEqual(len(out), 80)
        self.assertEqual(out, EXPECTED_SYNC)

    def test_chunked_delivery_matches_single_call(self):
        """Splitting the payload must not change the output."""
        stream = _stream()
        whole = AdpcmDecoder().decode_with_sync(stream)
        decoder = AdpcmDecoder()
        merged = []
        for cut in range(0, len(stream), 7):
            merged.extend(decoder.decode_with_sync(stream[cut:cut + 7]))
        self.assertEqual(merged, whole)

    def test_split_sync_marker_resynchronises(self):
        """A SYNC marker straddling two payloads is still detected."""
        stream = _stream()
        decoder = AdpcmDecoder()
        first = decoder.decode_with_sync(stream[:18])   # cuts inside "SYNC"
        second = decoder.decode_with_sync(stream[18:])
        self.assertEqual(first + second, EXPECTED_SYNC)

    def test_raw_decode_ignores_markers(self):
        stream = _stream()
        out = AdpcmDecoder().decode(stream)
        self.assertEqual(len(out), 2 * len(stream))

    def test_predictor_is_clamped(self):
        decoder = AdpcmDecoder()
        decoder.predictor = 32000
        decoder.step = IMA_STEP_TABLE[88]
        for _ in range(200):
            value = decoder.decode_nibble(7)
            self.assertLessEqual(value, 32767)
            self.assertGreaterEqual(value, -32768)

    def test_step_index_is_clamped(self):
        decoder = AdpcmDecoder()
        decoder.step_index = 0
        decoder.decode_nibble(0)
        self.assertEqual(decoder.step_index, 0)
        decoder.step_index = 88
        decoder.decode_nibble(7)
        self.assertEqual(decoder.step_index, 88)

    def test_reset_restores_initial_state(self):
        decoder = AdpcmDecoder()
        decoder.decode_with_sync(_stream())
        decoder.reset()
        self.assertEqual(decoder.step_index, 0)
        self.assertEqual(decoder.predictor, 0)
        self.assertEqual(decoder.step, 0)
        self.assertEqual(decoder.decode_with_sync(_stream()), EXPECTED_SYNC)


if __name__ == "__main__":
    unittest.main()
