"""Tests for the government and military satellite table.

Everything here is offline. The "receivers" are small stand-ins that answer
``can_hear`` the way a real :class:`sdrstream.catalogue.Receiver` does, so the
reachability logic is exercised without probing anything.
"""

import unittest

from sdrstream import military
from sdrstream.catalogue import Band, Receiver


def receiver(url, low_hz, high_hz, bands=()):
    """A receiver with one published tuning range."""
    center_khz = (low_hz + high_hz) / 2.0 / 1000.0
    samplerate_khz = (high_hz - low_hz) / 1000.0
    return Receiver(url=url, kind="openwebrx", latitude=0.0, longitude=0.0,
                    bands=(Band(name="test", center_khz=center_khz,
                                samplerate_khz=samplerate_khz),),
                    satellite_bands=tuple(bands))


class TableTest(unittest.TestCase):
    def test_the_table_is_not_empty(self):
        self.assertGreater(len(military.GOVERNMENT_SATELLITES), 5)

    def test_every_entry_has_a_plausible_frequency(self):
        for satellite in military.GOVERNMENT_SATELLITES.values():
            self.assertGreater(satellite.downlink_hz, 100_000_000,
                               satellite.name)
            self.assertLess(satellite.downlink_hz, 40_000_000_000,
                            satellite.name)

    def test_every_entry_names_a_source(self):
        for satellite in military.GOVERNMENT_SATELLITES.values():
            self.assertTrue(satellite.source, satellite.name)
            self.assertTrue(satellite.notes, satellite.name)

    def test_encryption_labels_are_known(self):
        allowed = {"open", "encrypted", "beacon"}
        for satellite in military.GOVERNMENT_SATELLITES.values():
            self.assertIn(satellite.encryption, allowed, satellite.name)

    def test_the_uhf_satcom_repeaters_are_open(self):
        # The whole point of listing FLTSATCOM is that its traffic is not
        # encrypted, so this is the claim most worth pinning down.
        self.assertTrue(military.GOVERNMENT_SATELLITES["FLTSATCOM"].is_open)
        self.assertEqual(
            military.GOVERNMENT_SATELLITES["FLTSATCOM"].downlink_hz,
            255_550_000)

    def test_encrypted_entries_are_not_marked_open(self):
        self.assertFalse(military.GOVERNMENT_SATELLITES["DMSP"].is_open)
        self.assertEqual(
            military.GOVERNMENT_SATELLITES["DMSP"].encryption, "encrypted")

    def test_extra_downlinks_are_all_plausible(self):
        for satellite in military.GOVERNMENT_SATELLITES.values():
            for frequency_hz, _mode in satellite.extra_downlinks:
                self.assertGreater(frequency_hz, 100_000_000, satellite.name)

    def test_all_downlinks_starts_with_the_primary(self):
        dmsp = military.GOVERNMENT_SATELLITES["DMSP"]
        self.assertEqual(dmsp.all_downlinks()[0], (400_032_000, "FM"))
        self.assertIn((400_822_000, "FM"), dmsp.all_downlinks())


class BandNameTest(unittest.TestCase):
    def test_uhf_satcom_is_named(self):
        sat = military.GOVERNMENT_SATELLITES["FLTSATCOM"]
        self.assertEqual(sat.band, "UHF SATCOM")

    def test_the_400_mhz_range_is_named(self):
        sat = military.GOVERNMENT_SATELLITES["DMSP"]
        self.assertEqual(sat.band, "400-470 MHz")

    def test_the_s_band_range_is_named(self):
        sat = military.GOVERNMENT_SATELLITES["DSP / SBIRS"]
        self.assertEqual(sat.band, "S-band")

    def test_an_odd_frequency_falls_through_to_other(self):
        sat = military.GOVERNMENT_SATELLITES["Molniya"]
        self.assertEqual(sat.band, "other")


class ReachabilityTest(unittest.TestCase):
    def test_a_receiver_covering_the_downlink_counts(self):
        pool = [receiver("http://a", 250_000_000, 260_000_000)]
        found = {item.satellite.name: item for item in military.reachable(pool)}
        self.assertTrue(found["FLTSATCOM"].reachable)
        self.assertEqual(found["FLTSATCOM"].count, 1)

    def test_a_receiver_out_of_range_does_not_count(self):
        pool = [receiver("http://a", 144_000_000, 146_000_000)]
        found = {item.satellite.name: item for item in military.reachable(pool)}
        self.assertFalse(found["FLTSATCOM"].reachable)
        self.assertIsNone(found["FLTSATCOM"].downlink_hz)

    def test_an_extra_downlink_can_make_a_satellite_reachable(self):
        # Gonets is listed at 260 MHz with a 388 MHz extra; a receiver that
        # only reaches the extra must still be counted.
        pool = [receiver("http://a", 380_000_000, 400_000_000)]
        found = {item.satellite.name: item for item in military.reachable(pool)}
        item = found["Gonets-M / Strela-3"]
        self.assertTrue(item.reachable)
        self.assertEqual(item.downlink_hz, 388_000_000)

    def test_results_are_sorted_by_coverage(self):
        pool = [receiver("http://a", 250_000_000, 260_000_000),
                receiver("http://b", 250_000_000, 260_000_000)]
        found = military.reachable(pool)
        counts = [item.count for item in found]
        self.assertEqual(counts, sorted(counts, reverse=True))

    def test_no_receivers_means_nothing_is_reachable(self):
        for item in military.reachable([]):
            self.assertFalse(item.reachable)

    def test_the_summary_reads_sensibly(self):
        pool = [receiver("http://a", 250_000_000, 260_000_000)]
        found = {item.satellite.name: item for item in military.reachable(pool)}
        self.assertIn("1 receiver", found["FLTSATCOM"].summary())
        self.assertIn("not reachable", found["DSP / SBIRS"].summary())


class QueryTest(unittest.TestCase):
    def test_open_and_encrypted_can_be_separated(self):
        self.assertTrue(military.by_encryption("open"))
        for satellite in military.by_encryption("open"):
            self.assertTrue(satellite.is_open)
        for satellite in military.by_encryption("encrypted"):
            self.assertFalse(satellite.is_open)

    def test_categories_are_selectable(self):
        comms = military.by_category("comms")
        self.assertTrue(comms)
        for satellite in comms:
            self.assertEqual(satellite.category, "comms")

    def test_an_unknown_category_is_empty_not_an_error(self):
        self.assertEqual(military.by_category("teleportation"), [])


class ReportTest(unittest.TestCase):
    def test_the_report_lists_every_entry(self):
        text = military.report()
        for name in military.GOVERNMENT_SATELLITES:
            self.assertIn(name, text)

    def test_the_report_says_what_encryption_means(self):
        self.assertIn("open = payload readable", military.report())

    def test_the_report_names_what_is_out_of_reach(self):
        text = military.report()
        for name in military.OUT_OF_REACH:
            self.assertIn(name, text)

    def test_the_report_includes_reachability_when_given_receivers(self):
        pool = [receiver("http://a", 250_000_000, 260_000_000)]
        text = military.report(pool)
        self.assertIn("reachability against 1 receiver(s)", text)


if __name__ == "__main__":
    unittest.main()
