"""Tests for the catalogue generator.

The generator runs offline against a captured server list, so its parsing and
band maths can be checked without touching the network.
"""

import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "tools"))

import build_catalogue  # noqa: E402


SERVER_LIST = """var dummy = 1;
[
  {"url": "http://a.example:8901/", "lat": 52.0, "lon": 6.0,
   "bands": [{"c": "2m", "l": 144.0, "h": 146.0}]},
  {"url": "http://b.example:8901/", "lat": 40.0, "lon": -3.0,
   "bands": [{"c": "hf", "l": 7.0, "h": 7.2}]},
  {"url": "http://c.example:8901/", "lat": 10.0, "lon": 20.0,
   "bands": [{"c": "70cm", "l": 435.0, "h": 438.0},
             {"c": "23cm", "l": 1296.0, "h": 1298.0}]}
]"""


class ParseServerListTest(unittest.TestCase):
    def test_preamble_is_skipped(self):
        entries = build_catalogue.parse_server_list(SERVER_LIST)
        self.assertEqual(len(entries), 3)

    def test_missing_array_is_an_error(self):
        with self.assertRaises(ValueError):
            build_catalogue.parse_server_list("nothing here")


class BandsFromEntryTest(unittest.TestCase):
    def test_edges_become_centre_and_sample_rate(self):
        bands = build_catalogue.bands_from_entry(
            {"bands": [{"c": "2m", "l": 144.0, "h": 146.0}]})
        self.assertEqual(len(bands), 1)
        self.assertAlmostEqual(bands[0].center_khz, 145_000.0)
        self.assertAlmostEqual(bands[0].samplerate_khz, 2000.0)

    def test_round_trip_preserves_the_edges(self):
        bands = build_catalogue.bands_from_entry(
            {"bands": [{"c": "70cm", "l": 435.0, "h": 438.0}]})
        self.assertAlmostEqual(bands[0].low_hz, 435_000_000.0, places=3)
        self.assertAlmostEqual(bands[0].high_hz, 438_000_000.0, places=3)

    def test_a_degenerate_band_is_dropped(self):
        bands = build_catalogue.bands_from_entry(
            {"bands": [{"c": "x", "l": 100.0, "h": 100.0}]})
        self.assertEqual(bands, [])

    def test_an_incomplete_band_is_dropped(self):
        bands = build_catalogue.bands_from_entry({"bands": [{"c": "x", "l": 100.0}]})
        self.assertEqual(bands, [])

    def test_no_bands_is_an_empty_list(self):
        self.assertEqual(build_catalogue.bands_from_entry({}), [])


class SatelliteBandTest(unittest.TestCase):
    def test_2m_is_recognised(self):
        bands = build_catalogue.bands_from_entry(
            {"bands": [{"c": "2m", "l": 144.0, "h": 146.0}]})
        self.assertEqual(build_catalogue.satellite_bands(bands), ["2m"])

    def test_hf_only_receivers_are_not_kept(self):
        bands = build_catalogue.bands_from_entry(
            {"bands": [{"c": "hf", "l": 7.0, "h": 7.2}]})
        self.assertEqual(build_catalogue.satellite_bands(bands), [])

    def test_several_bands_are_all_reported_and_sorted(self):
        bands = build_catalogue.bands_from_entry(
            {"bands": [{"c": "70cm", "l": 435.0, "h": 438.0},
                       {"c": "23cm", "l": 1296.0, "h": 1298.0}]})
        self.assertEqual(build_catalogue.satellite_bands(bands), ["23cm", "70cm"])

    def test_a_partial_overlap_still_counts(self):
        # 432-434 MHz touches the bottom of the 70 cm satellite band.
        bands = build_catalogue.bands_from_entry(
            {"bands": [{"c": "m70", "l": 432.0, "h": 434.0}]})
        self.assertEqual(build_catalogue.satellite_bands(bands), ["70cm"])

    def test_bands_just_outside_do_not_count(self):
        bands = build_catalogue.bands_from_entry(
            {"bands": [{"c": "m70", "l": 420.0, "h": 429.0}]})
        self.assertEqual(build_catalogue.satellite_bands(bands), [])


class OfflineGatherTest(unittest.TestCase):
    """Base class that serves a captured server list instead of the network."""

    def setUp(self):
        self.original_fetch = build_catalogue.fetch
        build_catalogue.fetch = lambda url, timeout=30.0: SERVER_LIST

    def tearDown(self):
        build_catalogue.fetch = self.original_fetch


class GatherTest(OfflineGatherTest):
    def test_only_receivers_reaching_a_satellite_band_are_kept(self):
        found = build_catalogue.gather()
        # Sorted west to east: a is at 6 degrees, c at 20.
        self.assertEqual([r.url for r in found], ["http://a.example:8901",
                                                  "http://c.example:8901"])

    def test_coordinates_are_carried_through(self):
        found = {r.url: r for r in build_catalogue.gather()}
        self.assertAlmostEqual(found["http://a.example:8901"].latitude, 52.0)
        self.assertAlmostEqual(found["http://a.example:8901"].longitude, 6.0)


class RenderTest(OfflineGatherTest):
    def test_rendered_module_has_one_call_per_receiver(self):
        receivers = build_catalogue.gather()
        text = build_catalogue.render(receivers)
        # One definition in the header plus one call per receiver.
        self.assertEqual(text.count("_add("), len(receivers) + 1)

    def test_rendered_module_compiles(self):
        receivers = build_catalogue.gather()
        compile(build_catalogue.render(receivers), "catalogue.py", "exec")

    def test_rendered_bands_keep_their_edges(self):
        receivers = build_catalogue.gather()
        text = build_catalogue.render(receivers)
        namespace = {}
        exec(compile(text, "catalogue.py", "exec"), namespace)
        receivers_in_module = namespace["RECEIVERS"]
        self.assertEqual(len(receivers_in_module), 2)
        found = receivers_in_module["http://a.example:8901"]
        self.assertAlmostEqual(found.bands[0].low_hz, 144_000_000.0, places=3)


if __name__ == "__main__":
    unittest.main()
