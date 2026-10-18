"""Tests for the SatNOGS reader.

The network calls are rate limited and not always available, so these tests
exercise the parsing and cache paths, which is where the bugs live. Anything
that needs the live API is checked by hand, not here.
"""

import json
import tempfile
import unittest
from datetime import datetime, timezone
from pathlib import Path

from sdrstream import satnogs

RAW_OBSERVATION = {
    "id": 15234238,
    "sat_id": "BFEA-6446-3541-2879-0397",
    "norad_cat_id": 27607,
    "station_name": "SA2KNG Omni UHF/VHF",
    "station_lat": 63.854,
    "station_lng": 20.208,
    "start": "2026-10-18T19:36:43Z",
    "end": "2026-10-18T19:47:29Z",
    "max_altitude": 17.0,
    "observation_frequency": 436795000,
    "transmitter_mode": "FM",
    "status": "good",
    "waterfall": "https://example.invalid/wf.png",
    "demoddata": [{"payload_demod": "https://example.invalid/data"}],
    "tle1": "1 27607U 02058C   26291.50000000  .00000000  00000+0  00000+0 0  9990",
    "tle2": "2 27607  64.5553  10.7462 0088486  29.1423 331.3628 14.77696420    01",
}


class ObservationParsingTest(unittest.TestCase):
    def test_observation_reads_the_flat_fields(self):
        observation = satnogs._observation(RAW_OBSERVATION)
        self.assertEqual(observation.id, 15234238)
        self.assertEqual(observation.norad_id, 27607)
        self.assertEqual(observation.station_name, "SA2KNG Omni UHF/VHF")
        self.assertAlmostEqual(observation.latitude, 63.854)
        self.assertAlmostEqual(observation.longitude, 20.208)
        self.assertEqual(observation.status, "good")
        self.assertEqual(observation.mode, "FM")

    def test_observation_converts_the_timestamps_to_utc(self):
        observation = satnogs._observation(RAW_OBSERVATION)
        self.assertEqual(observation.start,
                         datetime(2026, 10, 18, 19, 36, 43, tzinfo=timezone.utc))
        self.assertEqual(observation.end,
                         datetime(2026, 10, 18, 19, 47, 29, tzinfo=timezone.utc))

    def test_duration_is_the_span_of_the_observation(self):
        observation = satnogs._observation(RAW_OBSERVATION)
        self.assertAlmostEqual(observation.duration, 646.0, places=3)

    def test_duration_is_zero_without_timestamps(self):
        raw = dict(RAW_OBSERVATION, start=None, end=None)
        self.assertEqual(satnogs._observation(raw).duration, 0.0)

    def test_nested_transmitter_supplies_missing_frequency(self):
        raw = dict(RAW_OBSERVATION, observation_frequency=None,
                   transmitter_mode=None,
                   transmitter={"downlink_low": 436793000, "mode": "FM"})
        observation = satnogs._observation(raw)
        self.assertEqual(observation.frequency_hz, 436793000)
        self.assertEqual(observation.mode, "FM")

    def test_demod_url_comes_from_the_first_artifact(self):
        observation = satnogs._observation(RAW_OBSERVATION)
        self.assertEqual(observation.demod_url, "https://example.invalid/data")

    def test_demod_url_is_none_when_nothing_was_captured(self):
        raw = dict(RAW_OBSERVATION, demoddata=[])
        self.assertIsNone(satnogs._observation(raw).demod_url)

    def test_summary_mentions_the_satellite_and_frequency(self):
        text = satnogs._observation(RAW_OBSERVATION).summary()
        self.assertIn("436.7950", text)
        self.assertIn("FM", text)

    def test_parse_time_rejects_rubbish(self):
        self.assertIsNone(satnogs._parse_time("not a timestamp"))
        self.assertIsNone(satnogs._parse_time(None))

    def test_parse_time_assumes_utc_for_a_naive_value(self):
        parsed = satnogs._parse_time("2026-10-18T19:36:43")
        self.assertEqual(parsed.tzinfo, timezone.utc)


class CacheTest(unittest.TestCase):
    """The cache is a plain JSON file, so it can be driven without a network."""

    def setUp(self):
        self.directory = Path(tempfile.mkdtemp())
        payload = [{
            "id": 1, "satellite": "SO-50", "norad_id": 27607,
            "station_name": "test", "latitude": 50.0, "longitude": 6.0,
            "start": "2026-10-18T19:36:43Z", "end": "2026-10-18T19:47:29Z",
            "max_elevation": 42.0, "frequency_hz": 436793000, "mode": "FM",
            "status": "good", "waterfall_url": None, "demod_url": None,
            "tle_line1": None, "tle_line2": None,
        }, {
            "id": 2, "satellite": "SO-50", "norad_id": 27607,
            "station_name": "test", "latitude": 50.0, "longitude": 6.0,
            "start": "2026-10-18T20:36:43Z", "end": "2026-10-18T20:47:29Z",
            "max_elevation": 20.0, "frequency_hz": 436793000, "mode": "FM",
            "status": "good", "waterfall_url": None, "demod_url": None,
            "tle_line1": None, "tle_line2": None,
        }, {
            "id": 3, "satellite": "SO-50", "norad_id": 27607,
            "station_name": "test", "latitude": 50.0, "longitude": 6.0,
            "start": "2026-10-18T21:36:43Z", "end": "2026-10-18T21:47:29Z",
            "max_elevation": 11.0, "frequency_hz": 436795000, "mode": "FM",
            "status": "good", "waterfall_url": None, "demod_url": None,
            "tle_line1": None, "tle_line2": None,
        }]
        (self.directory / "satnogs_27607.json").write_text(json.dumps(payload))

    def test_cached_observations_reads_the_cache_without_network(self):
        found = satnogs.cached_observations(27607, cache_dir=self.directory)
        self.assertEqual(len(found), 3)
        self.assertEqual(found[0].id, 1)

    def test_cached_observations_returns_typed_objects(self):
        found = satnogs.cached_observations(27607, cache_dir=self.directory)
        self.assertIsInstance(found[0], satnogs.Observation)
        self.assertEqual(found[0].start.tzinfo, timezone.utc)

    def test_observed_frequencies_counts_per_downlink(self):
        counts = satnogs.observed_frequencies(27607, cache_dir=self.directory)
        self.assertEqual(counts[436793000], 2)
        self.assertEqual(counts[436795000], 1)

    def test_observed_frequencies_are_sorted_by_popularity(self):
        counts = satnogs.observed_frequencies(27607, cache_dir=self.directory)
        self.assertEqual(list(counts), [436793000, 436795000])


class DownloadTest(unittest.TestCase):
    """download() streams a URL to disk; file:// exercises that without a network."""

    def setUp(self):
        self.directory = Path(tempfile.mkdtemp())

    def test_download_copies_the_whole_payload(self):
        source = self.directory / "source.bin"
        payload = bytes(range(256)) * 1000
        source.write_bytes(payload)
        target = self.directory / "nested" / "target.bin"
        result = satnogs.download(source.as_uri(), target)
        self.assertEqual(result, str(target))
        self.assertEqual(target.read_bytes(), payload)

    def test_download_creates_the_parent_directory(self):
        source = self.directory / "source.bin"
        source.write_bytes(b"hello")
        target = self.directory / "a" / "b" / "target.bin"
        satnogs.download(source.as_uri(), target)
        self.assertTrue(target.exists())


class TransmitterRankingTest(unittest.TestCase):
    def setUp(self):
        self.records = [
            # An S-band beacon: alive but no public receiver can hear it.
            {"norad_cat_id": 1, "alive": True, "downlink_low": 2_200_000_000,
             "mode": "BPSK", "description": "S-band"},
            # The real amateur downlink.
            {"norad_cat_id": 1, "alive": True, "downlink_low": 145_960_000,
             "mode": "USB", "description": "linear transponder"},
            # A dead transmitter in the right band.
            {"norad_cat_id": 1, "alive": False, "downlink_low": 145_960_000,
             "mode": "USB", "description": "old"},
        ]
        self.original = satnogs._transmitter_table
        satnogs._transmitter_table = lambda **kwargs: self.records

    def tearDown(self):
        satnogs._transmitter_table = self.original

    def test_transmitter_filters_by_norad_id(self):
        self.assertEqual(len(satnogs.transmitter(1)), 3)

    def test_best_downlink_prefers_an_amateur_band(self):
        best = satnogs.best_downlink(1)
        self.assertEqual(best["downlink_low"], 145_960_000)

    def test_best_downlink_is_none_for_an_unknown_satellite(self):
        self.assertIsNone(satnogs.best_downlink(999999))


if __name__ == "__main__":
    unittest.main()
