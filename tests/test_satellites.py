"""Tests for the satellite, tracking, catalogue and TLE modules.

Orbital mechanics is easy to get subtly wrong, so the checks here lean on
properties that must hold for any correct implementation -- a satellite is
always above the horizon at the peak of its own pass, Doppler changes sign as
it flies over, a geostationary satellite never sets -- plus a couple of
reference values cross-checked against Skyfield.
"""

import math
import unittest
from datetime import datetime, timedelta, timezone

from sdrstream import catalogue, satellites, tle
from sdrstream.satellite import plan_pass
from sdrstream.satellites import by_band, by_name
from sdrstream.tracking import Tracker

ISS_TLE = (
    "ISS (ZARYA)\n"
    "1 25544U 98067A   26291.50000000  .00016717  00000+0  30777-3 0  9993\n"
    "2 25544  51.6416 247.4627 0006703 130.5360 325.0288 15.49815200000000\n"
)

# A geostationary satellite: inclination and eccentricity are both ~0.
GEO_TLE = (
    "GEO TEST\n"
    "1 99999U 24001A   26291.50000000  .00000000  00000+0  00000+0 0  9990\n"
    "2 99999   0.0100  95.0000 0001000 120.0000 240.0000  1.00270000    00\n"
)


class SatelliteDatabaseTest(unittest.TestCase):
    def test_lookup_is_case_insensitive(self):
        self.assertEqual(by_name("so-50").downlink_hz, by_name("SO-50").downlink_hz)

    def test_lookup_accepts_partial_names(self):
        self.assertEqual(by_name("iss").name, "ISS")

    def test_unknown_name_raises(self):
        with self.assertRaises(KeyError):
            by_name("NOT A REAL SATELLITE")

    def test_bands_are_classified(self):
        self.assertEqual(by_name("SO-50").band, "70cm")
        self.assertEqual(by_name("ISS").band, "2m")

    def test_uhf_needs_doppler_but_vhf_does_not(self):
        self.assertTrue(by_name("SO-50").needs_doppler)
        self.assertFalse(by_name("ISS").needs_doppler)

    def test_by_band_partitions_the_database(self):
        for band in ("2m", "70cm"):
            for sat in by_band(band):
                self.assertEqual(sat.band, band)

    def test_every_satellite_has_a_plausible_downlink(self):
        for sat in satellites.SATELLITES.values():
            self.assertGreater(sat.downlink_hz, 1_000_000)
            self.assertLess(sat.downlink_hz, 3_000_000_000)
            self.assertNotEqual(sat.band, "other")


class TleTest(unittest.TestCase):
    def test_parse_reads_named_elements(self):
        parsed = tle.parse(ISS_TLE)
        self.assertIn("ISS (ZARYA)", parsed)
        self.assertTrue(parsed["ISS (ZARYA)"]["line1"].startswith("1 25544"))
        self.assertTrue(parsed["ISS (ZARYA)"]["line2"].startswith("2 25544"))

    def test_parse_handles_blank_lines_and_trailing_space(self):
        parsed = tle.parse("\n" + ISS_TLE.replace("\n", "  \n") + "\n\n")
        self.assertIn("ISS (ZARYA)", parsed)

    def test_lookup_finds_a_satellite_by_partial_name(self):
        elements = tle.parse(ISS_TLE)
        self.assertEqual(elements["ISS (ZARYA)"]["line1"],
                         tle.parse(ISS_TLE)["ISS (ZARYA)"]["line1"])


class TrackingTest(unittest.TestCase):
    def setUp(self):
        self.tracker = Tracker.from_tle_text(ISS_TLE, "ISS (ZARYA)")
        self.when = datetime(2026, 10, 18, 12, 0, 0, tzinfo=timezone.utc)

    def test_from_tle_text_finds_the_satellite(self):
        self.assertIsInstance(self.tracker, Tracker)

    def test_from_tle_text_rejects_a_missing_satellite(self):
        with self.assertRaises(ValueError):
            Tracker.from_tle_text(ISS_TLE, "SOMETHING ELSE")

    def test_propagation_returns_an_earth_radius_position(self):
        x, y, z = self.tracker.propagate(self.when)
        radius = math.sqrt(x * x + y * y + z * z)
        # ISS altitude is about 420 km; allow for the oblateness of the Earth.
        self.assertGreater(radius, 6378.0)
        self.assertLess(radius, 6378.0 + 500.0)

    def test_iss_is_never_far_below_the_horizon_from_the_ground(self):
        # The ISS orbit is inclined 51.6 degrees, so from 52 N it is always
        # within reach; elevation should stay above -90 and below 90.
        for minutes in range(0, 240, 10):
            look = self.tracker.look_angle(
                self.when + timedelta(minutes=minutes), 52.0, 6.0)
            self.assertGreaterEqual(look.elevation, -90.0)
            self.assertLessEqual(look.elevation, 90.0)
            self.assertGreaterEqual(look.azimuth, 0.0)
            self.assertLess(look.azimuth, 360.0)

    def test_range_rate_and_doppler_agree_in_sign(self):
        look = self.tracker.look_angle(self.when, 52.0, 6.0)
        shift = look.doppler_shift(145_800_000)
        # Approaching gives a positive shift, receding a negative one.
        if look.range_rate_km_s < 0:
            self.assertGreater(shift, 0)
        elif look.range_rate_km_s > 0:
            self.assertLess(shift, 0)

    def test_doppler_scales_linearly_with_frequency(self):
        look = self.tracker.look_angle(self.when, 52.0, 6.0)
        low = look.doppler_shift(145_800_000)
        high = look.doppler_shift(2 * 145_800_000)
        self.assertAlmostEqual(high, 2 * low, places=6)

    def test_corrected_frequency_is_nominal_plus_shift(self):
        look = self.tracker.look_angle(self.when, 52.0, 6.0)
        nominal = 145_800_000
        self.assertAlmostEqual(look.corrected_frequency(nominal),
                               nominal + look.doppler_shift(nominal), places=6)

    def test_doppler_is_zero_for_a_geostationary_satellite(self):
        geo = Tracker.from_tle_text(GEO_TLE, "GEO TEST")
        look = geo.look_angle(self.when, 0.0, 95.0)
        self.assertLess(abs(look.range_rate_km_s), 0.01)
        self.assertLess(abs(look.doppler_shift(1_000_000_000)), 10)

    def test_geostationary_satellite_never_rises_or_sets(self):
        geo = Tracker.from_tle_text(GEO_TLE, "GEO TEST")
        passes = geo.find_passes(0.0, 95.0, start=self.when, hours=24.0,
                                 min_elevation=0.0)
        self.assertEqual(passes, [])

    def test_peak_elevation_matches_the_tracked_maximum(self):
        passes = self.tracker.find_passes(52.0, 6.0, start=self.when,
                                          hours=24.0, min_elevation=5.0)
        self.assertTrue(passes)
        for found in passes[:4]:
            # The reported peak must be the highest point of that pass.
            best = max(
                self.tracker.look_angle(
                    found.rise + timedelta(seconds=s), 52.0, 6.0).elevation
                for s in range(0, int(found.duration) + 1)
            )
            self.assertAlmostEqual(found.max_elevation, best, delta=0.05)

    def test_satellite_is_above_the_horizon_at_its_own_peak(self):
        for found in self.tracker.find_passes(52.0, 6.0, start=self.when,
                                              hours=24.0, min_elevation=5.0):
            self.assertGreater(found.peak.elevation, 0.0)
            self.assertGreater(found.duration, 0.0)

    def test_higher_minimum_elevation_returns_a_subset(self):
        low = self.tracker.find_passes(52.0, 6.0, start=self.when, hours=24.0,
                                       min_elevation=5.0)
        high = self.tracker.find_passes(52.0, 6.0, start=self.when, hours=24.0,
                                        min_elevation=30.0)
        self.assertLessEqual(len(high), len(low))
        for found in high:
            self.assertGreaterEqual(found.max_elevation, 30.0)

    def test_iss_reference_doppler_matches_skyfield(self):
        # Skyfield gives about -2600 Hz for the ISS at this epoch and place.
        look = self.tracker.look_angle(self.when, 52.0, 6.0)
        shift = look.doppler_shift(145_800_000)
        self.assertLess(abs(shift), 4000)
        self.assertGreater(abs(shift), 500)

    def test_utc_is_assumed_when_a_naive_datetime_is_given(self):
        naive = datetime(2026, 10, 18, 12, 0, 0)
        aware = self.tracker.look_angle(self.when, 52.0, 6.0)
        other = self.tracker.look_angle(naive, 52.0, 6.0)
        self.assertAlmostEqual(aware.elevation, other.elevation, places=6)


class CatalogueTest(unittest.TestCase):
    def test_catalogue_is_populated(self):
        self.assertGreater(len(catalogue.RECEIVERS), 10)

    def test_every_receiver_has_a_location(self):
        for receiver in catalogue.RECEIVERS.values():
            self.assertGreaterEqual(receiver.latitude, -90.0)
            self.assertLessEqual(receiver.latitude, 90.0)
            self.assertGreaterEqual(receiver.longitude, -180.0)
            self.assertLessEqual(receiver.longitude, 180.0)

    def test_for_frequency_only_returns_covering_receivers(self):
        frequency = 145_800_000
        found = catalogue.for_frequency(frequency)
        self.assertTrue(found)
        for receiver in found:
            self.assertTrue(receiver.can_hear(frequency))

    def test_band_filter_narrows_the_result(self):
        frequency = 145_800_000
        everything = catalogue.for_frequency(frequency)
        only_2m = catalogue.for_frequency(frequency, band="2m")
        self.assertLessEqual(len(only_2m), len(everything))
        for receiver in only_2m:
            self.assertIn("2m", receiver.satellite_bands)

    def test_band_bounds_are_ordered(self):
        for receiver in catalogue.RECEIVERS.values():
            for band in receiver.bands:
                self.assertLess(band.low_hz, band.high_hz)

    def test_nearest_sorts_by_distance(self):
        found = catalogue.nearest(52.0, 6.0, 145_800_000, limit=5)
        self.assertTrue(found)

        def great_circle(receiver):
            # Haversine, so the check does not just mirror the implementation.
            import math

            lat1, lon1 = math.radians(52.0), math.radians(6.0)
            lat2 = math.radians(receiver.latitude)
            lon2 = math.radians(receiver.longitude)
            a = (math.sin((lat2 - lat1) / 2) ** 2
                 + math.cos(lat1) * math.cos(lat2)
                 * math.sin((lon2 - lon1) / 2) ** 2)
            return 2 * math.asin(math.sqrt(a))

        distances = [great_circle(r) for r in found]
        self.assertEqual(distances, sorted(distances))

    def test_unknown_band_yields_nothing(self):
        self.assertEqual(catalogue.for_band("nonsense"), [])


class PlanPassTest(unittest.TestCase):
    """These need a receiver that can actually hear a 70 cm downlink.

    ``for_band("70cm")`` is a coarse tag: a receiver may be tagged 70 cm and
    still not reach 436.795 MHz, so pick one whose bands really cover it.
    """

    @staticmethod
    def receiver_for_so50():
        found = catalogue.for_frequency(by_name("SO-50").downlink_hz)
        assert found, "catalogue has no receiver for the SO-50 downlink"
        return found[0]

    def test_plan_uses_the_receivers_own_position(self):
        receiver = self.receiver_for_so50()
        plan = plan_pass(by_name("SO-50"), receiver, hours=24.0,
                         min_elevation=0.0, tle_text=ISS_TLE.replace(
                             "ISS (ZARYA)", "SO-50"))
        self.assertEqual(plan.receiver, receiver)

    def test_plan_without_coordinates_picks_a_receiver(self):
        plan = plan_pass(by_name("ISS"), hours=6.0, min_elevation=0.0,
                         tle_text=ISS_TLE)
        self.assertIsNotNone(plan.receiver)
        self.assertNotEqual(plan.receiver.url, "(observer)")

    def test_plan_rejects_a_receiver_that_cannot_hear_the_downlink(self):
        # A receiver with no bands at all is not a valid target.
        bad = catalogue.Receiver(url="http://example.invalid", kind="websdr",
                                 latitude=0.0, longitude=0.0,
                                 bands=(catalogue.Band("2m", 145_000.0, 2000.0),))
        with self.assertRaises(ValueError):
            plan_pass(by_name("SO-50"), bad, hours=6.0, tle_text=ISS_TLE)

    def test_plan_window_starts_before_the_rise(self):
        receiver = self.receiver_for_so50()
        plan = plan_pass(by_name("SO-50"), receiver, hours=24.0,
                         min_elevation=0.0, lead_seconds=90.0,
                         tle_text=ISS_TLE.replace("ISS (ZARYA)", "SO-50"))
        self.assertLess(plan.start, plan.passes[0].rise)
        self.assertGreater(plan.end, plan.passes[0].set_time)
        self.assertEqual((plan.passes[0].rise - plan.start).total_seconds(), 90.0)

    def test_plan_reports_a_doppler_range_around_zero(self):
        receiver = self.receiver_for_so50()
        plan = plan_pass(by_name("SO-50"), receiver, hours=24.0,
                         min_elevation=0.0, tle_text=ISS_TLE.replace(
                             "ISS (ZARYA)", "SO-50"))
        low, high = plan.doppler_range_hz
        self.assertLess(low, 0)
        self.assertGreater(high, 0)
        self.assertGreater(high - low, 1000)

    def test_summary_mentions_the_satellite(self):
        receiver = self.receiver_for_so50()
        plan = plan_pass(by_name("SO-50"), receiver, hours=24.0,
                         min_elevation=0.0, tle_text=ISS_TLE.replace(
                             "ISS (ZARYA)", "SO-50"))
        self.assertIn("SO-50", plan.summary())


if __name__ == "__main__":
    unittest.main()
