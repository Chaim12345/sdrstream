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
