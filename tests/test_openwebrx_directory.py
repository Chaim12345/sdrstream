"""Tests for OpenWebRX receiver discovery.

The directory page and the status documents are captured fixtures, so the
parsing and band maths are checked without touching the network.
"""

import unittest

from sdrstream import openwebrx_directory as directory

# A directory page in the same shape as the real one: a JSON array assigned to
# `var receivers`, with operator-written labels that can contain brackets.
DIRECTORY_HTML = """<!DOCTYPE html>
<html><body>
<script>
        var receivers = [{"label":"Site \\"A\\" [test]","location":{"coordinates":[6.0,52.0],"type":"Point"},
        "receivers":[{"label":"OpenWebRX A","type":"OpenWebRX","url":"http://a.example:8073"},
        {"label":"Kiwi A","type":"KiwiSDR","url":"http://kiwi.example:8073"}]},
        {"label":"Site B","location":{"coordinates":[20.0,10.0],"type":"Point"},
        "receivers":[{"label":"OpenWebRX B","type":"OpenWebRX","url":"http://b.example:8073"},
        {"label":"OpenWebRX A again","type":"OpenWebRX","url":"http://a.example:8073"}]}];
</script>
</body></html>
"""

STATUS_2M = {
    "version": "v1.2.126",
    "receiver": {"name": "Test", "gps": {"lat": 52.41876, "lon": 13.30633}},
    "sdrs": [{"name": "RTL-SDR", "type": "RtlSdrSource",
              "profiles": [{"name": "2m", "center_freq": 145_000_000,
                            "sample_rate": 2_048_000}]}],
}

STATUS_HF_ONLY = {
    "receiver": {"name": "HF only", "gps": {"lat": 50.0, "lon": 6.0}},
    "sdrs": [{"name": "Kiwi", "profiles": [
        {"name": "10 MHz", "center_freq": 10_000_000, "sample_rate": 1_000_000}]}],
}

STATUS_NO_GPS = {
    "receiver": {"name": "No position"},
    "sdrs": [{"name": "RTL", "profiles": [
        {"name": "2m", "center_freq": 145_000_000, "sample_rate": 2_048_000}]}],
}


class ParseDirectoryTest(unittest.TestCase):
    def test_receivers_are_found(self):
        entries = directory.parse_directory(DIRECTORY_HTML)
        self.assertEqual(len(entries), 2)

    def test_a_bracket_in_a_label_does_not_cut_the_array_short(self):
        entries = directory.parse_directory(DIRECTORY_HTML)
        self.assertEqual(len(entries[0]["receivers"]), 2)

    def test_missing_array_is_an_error(self):
        with self.assertRaises(ValueError):
            directory.parse_directory("<html>nothing</html>")


class DirectoryUrlsTest(unittest.TestCase):
    def test_only_the_requested_kind_is_returned(self):
        urls = directory.directory_urls(DIRECTORY_HTML, kind="OpenWebRX")
        self.assertEqual(urls, ["http://a.example:8073", "http://b.example:8073"])

    def test_duplicate_urls_are_collapsed(self):
        urls = directory.directory_urls(DIRECTORY_HTML)
        self.assertEqual(len(urls), len(set(urls)))

    def test_a_different_kind_selects_other_servers(self):
        urls = directory.directory_urls(DIRECTORY_HTML, kind="KiwiSDR")
        self.assertEqual(urls, ["http://kiwi.example:8073"])


class BandsFromStatusTest(unittest.TestCase):
    def test_a_profile_becomes_a_band(self):
        bands = directory.bands_from_status(STATUS_2M)
        self.assertEqual(len(bands), 1)
        self.assertAlmostEqual(bands[0].center_khz, 145_000.0)
        self.assertAlmostEqual(bands[0].samplerate_khz, 2048.0)

    def test_edges_are_derived_from_centre_and_rate(self):
        band = directory.bands_from_status(STATUS_2M)[0]
        self.assertAlmostEqual(band.low_hz, 143_976_000.0, places=3)
        self.assertAlmostEqual(band.high_hz, 146_024_000.0, places=3)

    def test_a_profile_without_a_frequency_is_skipped(self):
        status = {"sdrs": [{"profiles": [{"name": "broken"}]}]}
        self.assertEqual(directory.bands_from_status(status), [])

    def test_no_sdrs_is_an_empty_list(self):
        self.assertEqual(directory.bands_from_status({}), [])


class SatelliteBandsTest(unittest.TestCase):
    def test_2m_coverage_is_labelled(self):
        bands = directory.bands_from_status(STATUS_2M)
        self.assertEqual(directory.satellite_bands(bands), ["2m"])

    def test_hf_coverage_is_not_a_satellite_band(self):
        bands = directory.bands_from_status(STATUS_HF_ONLY)
        self.assertEqual(directory.satellite_bands(bands), [])

    def test_70cm_coverage_is_labelled(self):
        status = {"sdrs": [{"profiles": [
            {"name": "70cm", "center_freq": 436_000_000,
             "sample_rate": 2_880_000}]}]}
        bands = directory.bands_from_status(status)
        self.assertEqual(directory.satellite_bands(bands), ["70cm"])


class ReceiverFromStatusTest(unittest.TestCase):
    def test_coordinates_come_from_the_status_document(self):
        receiver = directory.receiver_from_status("http://a.example", STATUS_2M)
        self.assertAlmostEqual(receiver.latitude, 52.41876)
        self.assertAlmostEqual(receiver.longitude, 13.30633)
        self.assertEqual(receiver.kind, "openwebrx")

    def test_the_receiver_can_hear_its_own_band(self):
        receiver = directory.receiver_from_status("http://a.example", STATUS_2M)
        self.assertTrue(receiver.can_hear(145_800_000))
        self.assertFalse(receiver.can_hear(436_795_000))

    def test_a_server_without_coordinates_is_rejected(self):
        self.assertIsNone(
            directory.receiver_from_status("http://a.example", STATUS_NO_GPS))


class ForFrequencyTest(unittest.TestCase):
    def setUp(self):
        self.two_metre = directory.receiver_from_status("http://a.example",
                                                        STATUS_2M)
        self.hf = directory.receiver_from_status("http://hf.example",
                                                 STATUS_HF_ONLY)

    def test_only_covering_receivers_are_returned(self):
        found = directory.for_frequency(145_800_000, [self.two_metre, self.hf])
        self.assertEqual([r.url for r in found], ["http://a.example"])

    def test_the_band_filter_narrows_the_result(self):
        self.assertEqual(
            directory.for_frequency(145_800_000, [self.two_metre], band="70cm"),
            [])
        self.assertEqual(
            len(directory.for_frequency(145_800_000, [self.two_metre], band="2m")),
            1)


class ProbeOneTest(unittest.TestCase):
    """probe_one() must never raise; unreachable hosts return None."""

    def test_an_unreachable_host_returns_none(self):
        # A reserved, non-routable address: connection fails fast.
        self.assertIsNone(
            directory.probe_one("http://127.0.0.1:1", timeout=1.0))


if __name__ == "__main__":
    unittest.main()
