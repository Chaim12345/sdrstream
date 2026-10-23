"""Tests for the in-stream retuning chase session."""

from __future__ import annotations

import asyncio
import unittest
from datetime import datetime, timedelta, timezone
from unittest import mock

from sdrstream import catalogue, satellite
from sdrstream.satellite import ChasePlan, _ChaseSession
from sdrstream.satellites import by_name


class _FakeChunk:
    def __init__(self, samplerate=8000):
        self.samplerate = samplerate
        self.samples = [0, 0, 0]


class _FakeClient:
    """A WebSDR-ish client that counts connects, tunes and records."""

    instances = []

    def __init__(self, url, reconnect=False):
        self.url = url
        self.entered = 0
        self.exited = 0
        self.tunes = []
        self.records = []
        self.fail_next = False
        _FakeClient.instances.append(self)

    async def __aenter__(self):
        self.entered += 1
        return self

    async def __aexit__(self, *args):
        self.exited += 1
        return False

    async def tune(self, frequency_khz=None, mode="USB", **kwargs):
        self.tunes.append((frequency_khz, mode))

    async def record(self, duration):
        if self.fail_next:
            self.fail_next = False
            raise RuntimeError("connection dropped")
        self.records.append(duration)
        return [_FakeChunk()]

    @staticmethod
    def concat(chunks):
        out = []
        for chunk in chunks:
            out.extend(chunk.samples)
        return out


def _plan() -> ChasePlan:
    sat = by_name("ISS")
    receiver = catalogue.Receiver(url="http://example/websdr", kind="websdr",
                                 latitude=32.79, longitude=34.99)
    now = datetime.now(timezone.utc)
    return ChasePlan(
        satellite=sat, receiver=receiver, passes=[],
        start=now - timedelta(minutes=5), end=now + timedelta(minutes=5),
        start_frequency_hz=sat.downlink_hz, doppler_range_hz=(0, 0),
    )


class ChaseSessionTest(unittest.TestCase):
    def setUp(self):
        _FakeClient.instances = []
        self._patch = mock.patch("sdrstream.satellite.WebSDRClient",
                                 _FakeClient)
        self._patch.start()
        self.addCleanup(self._patch.stop)

    def test_records_without_reconnecting_between_segments(self):
        session = _ChaseSession(_plan())
        try:
            for _ in range(3):
                samples, rate = asyncio.run(
                    session.record(145_800_000.0, 1.0))
                self.assertEqual(rate, 8000)
                self.assertEqual(len(samples), 3)  # the chunk's samples
        finally:
            asyncio.run(session.close())
        self.assertEqual(len(_FakeClient.instances), 1,
                         "one connection should serve the whole pass")
        client = _FakeClient.instances[0]
        self.assertEqual(client.entered, 1)
        self.assertEqual(client.exited, 1)
        self.assertEqual(len(client.tunes), 3, "retune in-stream each window")
        self.assertEqual(len(client.records), 3)

    def test_reconnects_after_a_failure(self):
        session = _ChaseSession(_plan())
        try:
            asyncio.run(session.record(145_800_000.0, 1.0))  # open the client
            session._client.fail_next = True
            with self.assertRaises(RuntimeError):
                asyncio.run(session.record(145_800_000.0, 1.0))
            # The failed connection is torn down, the next call rebuilds it.
            samples, _ = asyncio.run(session.record(145_800_000.0, 1.0))
            self.assertEqual(len(samples), 3)
        finally:
            asyncio.run(session.close())
        self.assertEqual(len(_FakeClient.instances), 2)
        self.assertEqual(_FakeClient.instances[0].exited, 1,
                         "the failed client was closed")

    def test_tuning_uses_khz_and_satellite_mode(self):
        plan = _plan()
        session = _ChaseSession(plan)
        try:
            asyncio.run(session.record(437_825_000.0, 1.0))
        finally:
            asyncio.run(session.close())
        client = _FakeClient.instances[0]
        frequency_khz, mode = client.tunes[0]
        self.assertAlmostEqual(frequency_khz, 437825.0, places=3)
        self.assertEqual(mode, plan.satellite.mode.upper())


if __name__ == "__main__":
    unittest.main()


class _FakeOpenWebRXClient(_FakeClient):
    """An OpenWebRX-ish fake that records tune kwargs and profiles."""

    def __init__(self, url, reconnect=False):
        super().__init__(url, reconnect=reconnect)
        self.profiles = [
            {"id": "rtlsdr-1|HF", "name": "RTL-SDR A 40m"},
            {"id": "rtlsdr-1|NOAA 2", "name": "RTL-SDR A NOAA 136-138"},
        ]
        self.selected = []

    async def tune(self, frequency=None, modulation=None, bandwidth=None,
                   **kwargs):
        self.tunes.append((frequency, modulation, bandwidth))

    async def select_profile(self, key):
        self.selected.append(key)


def _weather_plan() -> ChasePlan:
    from sdrstream.satellites import WEATHER_SATELLITES
    sat = WEATHER_SATELLITES["NOAA 15"]
    receiver = catalogue.Receiver(
        url="http://example/openwebrx", kind="openwebrx",
        latitude=51.80, longitude=5.71,
        bands=(catalogue.Band(name="NOAA 136-138", center_khz=137000.0,
                              samplerate_khz=2048.0),))
    now = datetime.now(timezone.utc)
    return ChasePlan(
        satellite=sat, receiver=receiver, passes=[],
        start=now - timedelta(minutes=5), end=now + timedelta(minutes=5),
        start_frequency_hz=sat.downlink_hz, doppler_range_hz=(0, 0),
    )


class AptDemodTest(unittest.TestCase):
    def setUp(self):
        _FakeClient.instances = []
        self._patch = mock.patch("sdrstream.satellite.OpenWebRXClient",
                                 _FakeOpenWebRXClient)
        self._patch.start()
        self.addCleanup(self._patch.stop)

    def test_bandwidth_mapping(self):
        from sdrstream.satellite import APT_BANDWIDTH_HZ, _bandwidth_for
        from sdrstream.satellites import WEATHER_SATELLITES, by_name
        noaa = WEATHER_SATELLITES["NOAA 15"]
        self.assertEqual(_bandwidth_for("openwebrx", noaa), APT_BANDWIDTH_HZ)
        self.assertIsNone(_bandwidth_for("websdr", noaa))
        self.assertIsNone(_bandwidth_for("openwebrx", by_name("ISS")))

    def test_weather_chase_tunes_wide_and_selects_profile(self):
        messages = []
        session = _ChaseSession(_weather_plan(), messages.append)
        try:
            samples, _ = asyncio.run(session.record(137_620_000.0, 1.0))
            self.assertEqual(len(samples), 3)
        finally:
            asyncio.run(session.close())
        client = _FakeClient.instances[0]
        self.assertEqual(client.selected, ["rtlsdr-1|NOAA 2"],
                         "band name must select the NOAA profile")
        freq, mode, bandwidth = client.tunes[0]
        self.assertEqual((mode, bandwidth), ("nfm", 40000),
                         "APT needs wide FM, not the ~12.5 kHz default")
        self.assertTrue(any("selected profile rtlsdr-1|NOAA 2" in m
                            for m in messages),
                        "capture log must prove the selected profile")
