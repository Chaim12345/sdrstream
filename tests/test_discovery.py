"""Tests for undocumented-satellite discovery.

The observation feed is served from a captured fixture so the identification
logic is checked without touching the network.
"""

import unittest

from sdrstream import discovery


def observation(norad_id, name, frequency_hz, mode, when="2026-10-18T14:13:03Z"):
    return {
        "id": norad_id * 10 + int(frequency_hz or 0) % 7,
        "norad_cat_id": norad_id,
        "tle0": name,
        "start": when,
        "end": when,
        "transmitter": {"mode": mode, "downlink_low": frequency_hz},
        "demoddata": [],
        "waterfall": "https://example.invalid/w.png",
        "vetted_status": "good",
    }


class RecentTest(unittest.TestCase):
    def setUp(self):
        self.original_get = discovery._get
        self.payload = [
            observation(25544, "ISS (ZARYA)", 437_800_000, "AFSK"),
            observation(25544, "ISS (ZARYA)", 437_800_000, "AFSK"),
            observation(98371, "BLACK KITE-2", 437_500_000, "FSK"),
        ]
        discovery._get = lambda url, timeout=45.0: self.payload

    def tearDown(self):
        discovery._get = self.original_get

    def test_every_satellite_is_counted(self):
        seen = discovery.recent()
        self.assertEqual(set(seen), {25544, 98371})

    def test_observations_are_accumulated_per_satellite(self):
        seen = discovery.recent()
        self.assertEqual(seen[25544].observation_count, 2)
        self.assertEqual(seen[98371].observation_count, 1)

    def test_the_frequency_is_recorded(self):
        seen = discovery.recent()
        self.assertEqual(seen[98371].frequencies_hz, [437_500_000])

    def test_an_observation_without_a_norad_id_is_ignored(self):
        self.payload.append({"tle0": "ghost"})
        self.assertEqual(set(discovery.recent()), {25544, 98371})


class SeenSatelliteTest(unittest.TestCase):
    def test_the_most_observed_frequency_wins(self):
        satellite = discovery.SeenSatellite(
            norad_id=1, name="X",
            frequencies_hz=[437_500_000, 437_500_000, 435_000_000])
        self.assertEqual(satellite.downlink_hz, 437_500_000)

    def test_no_frequencies_means_no_downlink(self):
        satellite = discovery.SeenSatellite(norad_id=1, name="X")
        self.assertIsNone(satellite.downlink_hz)

    def test_the_summary_shows_frequency_and_mode(self):
        satellite = discovery.SeenSatellite(
            norad_id=98371, name="BLACK KITE-2",
            frequencies_hz=[437_500_000], modes=["FSK"])
        self.assertIn("98371", satellite.summary())
        self.assertIn("437.5000 MHz", satellite.summary())
        self.assertIn("FSK", satellite.summary())


class UnknownSatellitesTest(unittest.TestCase):
    def setUp(self):
        self.original_get = discovery._get
        self.known = next(iter(discovery.SATELLITES.values())).norad_id
        discovery._get = lambda url, timeout=45.0: [
            observation(self.known, "already listed", 145_800_000, "AFSK"),
            observation(98371, "BLACK KITE-2", 437_500_000, "FSK"),
        ]

    def tearDown(self):
        discovery._get = self.original_get

    def test_satellites_already_in_the_table_are_excluded(self):
        found = discovery.unknown_satellites()
        self.assertEqual([s.norad_id for s in found], [98371])

    def test_an_empty_table_means_everything_is_unknown(self):
        original = discovery.known_ids
        discovery.known_ids = lambda: set()
        try:
            self.assertEqual(len(discovery.unknown_satellites()), 2)
        finally:
            discovery.known_ids = original

    def test_the_report_mentions_the_unknown_satellite(self):
        text = discovery.report()
        self.assertIn("BLACK KITE-2", text)
        self.assertNotIn("already listed", text)


class AmateurBandTest(unittest.TestCase):
    def test_a_70cm_downlink_is_in_band(self):
        satellite = discovery.SeenSatellite(
            norad_id=1, name="X", frequencies_hz=[437_500_000])
        self.assertTrue(discovery.in_amateur_bands(satellite))

    def test_an_s_band_downlink_is_out_of_band(self):
        satellite = discovery.SeenSatellite(
            norad_id=1, name="X", frequencies_hz=[2_200_000_000])
        self.assertFalse(discovery.in_amateur_bands(satellite))

    def test_no_frequency_is_out_of_band(self):
        self.assertFalse(discovery.in_amateur_bands(
            discovery.SeenSatellite(norad_id=1, name="X")))


if __name__ == "__main__":
    unittest.main()
