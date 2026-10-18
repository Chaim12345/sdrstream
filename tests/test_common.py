"""Tests for URL handling, tuning parameters and WAV output."""

import io
import os
import struct
import tempfile
import unittest
import wave

from sdrstream.common import (
    pcm_bytes,
    wav_bytes,
    websdr_tuning_params,
    websocket_url,
    write_wav,
)


class TestWebsocketUrl(unittest.TestCase):
    def test_http_becomes_ws(self):
        self.assertEqual(
            websocket_url("http://sdr.example.org:8901", "/~~stream"),
            "ws://sdr.example.org:8901/~~stream",
        )

    def test_https_becomes_wss(self):
        self.assertEqual(
            websocket_url("https://sdr.example.org", "/ws/"),
            "wss://sdr.example.org/ws/",
        )

    def test_missing_scheme_defaults_to_plain(self):
        self.assertEqual(
            websocket_url("sdr.example.org:8901", "/~~stream"),
            "ws://sdr.example.org:8901/~~stream",
        )

    def test_base_path_is_preserved(self):
        self.assertEqual(
            websocket_url("https://example.org/kiwi", "/ws/"),
            "wss://example.org/kiwi/ws/",
        )

    def test_explicit_secure_overrides_scheme(self):
        self.assertEqual(
            websocket_url("http://example.org", "/ws/", secure=True),
            "wss://example.org/ws/",
        )

    def test_empty_url_is_rejected(self):
        with self.assertRaises(ValueError):
            websocket_url("   ", "/ws/")


class TestTuningParams(unittest.TestCase):
    def _parse(self, query):
        import urllib.parse
        return dict(urllib.parse.parse_qsl(query))

    def test_usb_passband_starts_at_carrier(self):
        params = self._parse(websdr_tuning_params(7100.0, mode="USB", bandwidth_hz=2700))
        self.assertEqual(params["f"], "7100.000")
        self.assertEqual(params["lo"], "0.000")
        self.assertEqual(params["hi"], "2.700")
        self.assertEqual(params["mode"], "0")

    def test_lsb_passband_ends_at_carrier(self):
        params = self._parse(websdr_tuning_params(7100.0, mode="LSB", bandwidth_hz=2700))
        self.assertEqual(params["lo"], "-2.700")
        self.assertEqual(params["hi"], "0.000")
        self.assertEqual(params["mode"], "0")

    def test_am_uses_mode_code_one(self):
        params = self._parse(websdr_tuning_params(10000.0, mode="AM", bandwidth_hz=6000))
        self.assertEqual(params["mode"], "1")
        self.assertEqual(params["lo"], "-3.000")
        self.assertEqual(params["hi"], "3.000")

    def test_fm_uses_mode_code_four(self):
        params = self._parse(websdr_tuning_params(100000.0, mode="FM"))
        self.assertEqual(params["mode"], "4")

    def test_explicit_passband_edges_win(self):
        params = self._parse(
            websdr_tuning_params(7100.0, mode="CW", low_khz=-0.5, high_khz=0.5)
        )
        self.assertEqual(params["lo"], "-0.500")
        self.assertEqual(params["hi"], "0.500")

    def test_band_and_name_are_encoded(self):
        params = self._parse(
            websdr_tuning_params(7100.0, mode="USB", band=3, name="a b&c")
        )
        self.assertEqual(params["band"], "3")
        self.assertEqual(params["name"], "a b&c")

    def test_unknown_mode_is_rejected(self):
        with self.assertRaises(ValueError):
            websdr_tuning_params(7100.0, mode="RTTY")


class TestWavOutput(unittest.TestCase):
    def test_round_trip_through_file(self):
        samples = [-32768, -1, 0, 1, 32767]
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "out.wav")
            frames = write_wav(path, samples, 12000)
            self.assertEqual(frames, 5)
            with wave.open(path, "rb") as handle:
                self.assertEqual(handle.getnchannels(), 1)
                self.assertEqual(handle.getsampwidth(), 2)
                self.assertEqual(handle.getframerate(), 12000)
                self.assertEqual(handle.getnframes(), 5)
                got = struct.unpack("<5h", handle.readframes(5))
        self.assertEqual(list(got), samples)

    def test_round_trip_through_buffer(self):
        data = wav_bytes([100, -100, 200], 8000)
        with wave.open(io.BytesIO(data), "rb") as handle:
            self.assertEqual(handle.getnframes(), 3)
            self.assertEqual(handle.getframerate(), 8000)

    def test_partial_frame_is_trimmed(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "stereo.wav")
            frames = write_wav(path, [1, 2, 3], 8000, channels=2)
            self.assertEqual(frames, 1)

    def test_pcm_bytes_are_little_endian(self):
        self.assertEqual(pcm_bytes([1, -1]), b"\x01\x00\xff\xff")

    def test_empty_input_produces_valid_wav(self):
        with wave.open(io.BytesIO(wav_bytes([], 8000)), "rb") as handle:
            self.assertEqual(handle.getnframes(), 0)


if __name__ == "__main__":
    unittest.main()
