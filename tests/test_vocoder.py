"""Tests for the WebSDR vocoder.

Fixtures are real WebSocket messages captured from a live WebSDR server
(``websdr.ewi.utwente.nl:8901``). The ``*_ref.bin`` files hold the PCM that the
official WebSDR HTML5 client produces for the matching input, so these tests
pin bit-exact compatibility with the reference decoder.
"""

import os
import struct
import unittest

from sdrstream.vocoder import WebSdrVocoder

DATA = os.path.join(os.path.dirname(__file__), "data")


def _read(name):
    with open(os.path.join(DATA, name), "rb") as handle:
        return handle.read()


def _samples(name):
    raw = _read(name)
    return list(struct.unpack("<%dh" % (len(raw) // 2), raw))


class TestWebSdrVocoder(unittest.TestCase):
    def _check(self, stem):
        decoded = WebSdrVocoder().decode(_read("websdr_%s.bin" % stem))
        self.assertEqual(decoded, _samples("websdr_%s_ref.bin" % stem))

    def test_table_frame_matches_reference(self):
        """Frame 0000 is a 0x80 table frame followed by metadata."""
        self._check("0000")

    def test_lpc_frame_matches_reference(self):
        """Frame 0020 is a pure LPC stream."""
        self._check("0020")

    def test_mixed_frame_matches_reference(self):
        """Frame 0004 mixes LPC blocks with a table frame."""
        self._check("0004")

    def test_stream_state_is_carried_across_messages(self):
        """Concatenating message decodes equals decoding them separately."""
        frames = ["0000", "0020", "0004"]
        combined = WebSdrVocoder().decode(b"".join(_read("websdr_%s.bin" % f) for f in frames))
        streamed = []
        decoder = WebSdrVocoder()
        for frame in frames:
            streamed.extend(decoder.decode(_read("websdr_%s.bin" % frame)))
        self.assertEqual(combined, streamed)

    def test_samplerate_is_reported(self):
        decoder = WebSdrVocoder()
        decoder.decode(_read("websdr_0000.bin"))
        self.assertEqual(decoder.samplerate, 7119)

    def test_truncated_table_frame_pads_with_silence(self):
        """A table frame cut short contributes silence for the missing bytes."""
        decoder = WebSdrVocoder()
        short = bytes([0x80]) + bytes(range(10))
        out = decoder.decode(short)
        self.assertEqual(len(out), 128)
        self.assertEqual(out[10:], [0] * 118)

    def test_silence_frame_emits_zeros(self):
        out = WebSdrVocoder().decode(bytes([0x84]))
        self.assertEqual(out, [0] * 128)

    def test_metadata_only_message_yields_no_audio(self):
        out = WebSdrVocoder().decode(bytes([0x81, 0x1F, 0x40, 0x82, 0x00, 0x28]))
        self.assertEqual(out, [])

    def test_reset_restores_initial_state(self):
        decoder = WebSdrVocoder()
        decoder.decode(_read("websdr_0020.bin"))
        decoder.reset()
        self.assertEqual(
            decoder.decode(_read("websdr_0020.bin")),
            _samples("websdr_0020_ref.bin"),
        )


if __name__ == "__main__":
    unittest.main()
