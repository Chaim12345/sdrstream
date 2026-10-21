"""Tests for the orbit-record layer (TLE and GP/OMM element formats)."""

from __future__ import annotations

import unittest

from sdrstream import orbit
from sdrstream.tracking import Tracker

ISS_NAME = "ISS (ZARYA)"
ISS_LINE1 = "1 25544U 98067A   24293.51782528  .00016717  00000-0  10270-3 0  9996"
ISS_LINE2 = "2 25544  51.6416 247.4627 0006703 130.5360 325.0288 15.54225950231730"

#: A CCSDS OMM object with the CelesTrak GP JSON field names, carrying the
#: same ISS element set as the TLE above.
ISS_OMM_JSON = """[
  {
    "OBJECT_NAME": "ISS (ZARYA)",
    "OBJECT_ID": "1998-067A",
    "NORAD_CAT_ID": "25544",
    "CLASSIFICATION_TYPE": "U",
    "EPOCH": "2024-10-19T12:25:40.104192",
    "MEAN_MOTION": 15.54225950,
    "ECCENTRICITY": 0.0006703,
    "INCLINATION": 51.6416,
    "RA_OF_ASC_NODE": 247.4627,
    "ARG_PERICENTER": 130.5360,
    "MEAN_ANOMALY": 325.0288,
    "EPHEMERIS_TYPE": 0,
    "BSTAR": 0.0001027,
    "MEAN_MOTION_DOT": 0.00016717,
    "MEAN_MOTION_DDOT": 0.0,
    "REV_AT_EPOCH": 46830
  }
]"""


class TleRecordTest(unittest.TestCase):
    def test_reads_elements_from_columns(self) -> None:
        record = orbit.from_tle(ISS_NAME, ISS_LINE1, ISS_LINE2)
        self.assertEqual(record.norad_id, 25544)
        self.assertEqual(record.name, ISS_NAME)
        self.assertAlmostEqual(record.mean_motion_rev_per_day, 15.54225950, places=7)
        self.assertAlmostEqual(record.eccentricity, 0.0006703, places=9)
        self.assertAlmostEqual(record.argp_deg, 130.5360, places=4)
        self.assertAlmostEqual(record.inc_deg, 51.6416, places=4)
        self.assertAlmostEqual(record.raan_deg, 247.4627, places=4)
        self.assertAlmostEqual(record.mean_anomaly_deg, 325.0288, places=4)
        self.assertAlmostEqual(record.bstar, 0.0001027, places=9)

    def test_tle_float_handles_drag_shorthand(self) -> None:
        # "10270-3" means 0.10270e-3; " 00000-0" is zero.
        self.assertAlmostEqual(orbit._tle_float("10270-3"), 1.027e-4, places=12)
        self.assertEqual(orbit._tle_float(" 00000-0"), 0.0)
        self.assertAlmostEqual(orbit._tle_float("-40538-4"), -4.0538e-5, places=12)

    def test_propagation_matches_twoline2rv(self) -> None:
        from sgp4.api import Satrec

        reference = Satrec.twoline2rv(ISS_LINE1, ISS_LINE2)
        record = orbit.from_tle(ISS_NAME, ISS_LINE1, ISS_LINE2)
        sat = record.to_satrec()
        for offset in (0.0, 0.25, 1.5, 12.0):
            error, position, _ = reference.sgp4(reference.jdsatepoch + offset, 0.0)
            error2, position2, _ = sat.sgp4(reference.jdsatepoch + offset, 0.0)
            self.assertEqual(error, 0)
            self.assertEqual(error2, 0)
            worst = max(abs(a - b) for a, b in zip(position, position2))
            # Sub-metre agreement; the residue is day-fraction rounding.
            self.assertLess(worst, 0.001, "offset %s drifted %s km" % (offset, worst))

    def test_six_digit_catalog_numbers_are_supported(self) -> None:
        # The TLE layout cannot hold these; direct element initialisation can.
        record = orbit.from_tle(ISS_NAME, ISS_LINE1, ISS_LINE2)
        record = orbit.OrbitRecord(
            name="NEW-BIRD", norad_id=100001, epoch=record.epoch,
            mean_motion_rev_per_day=record.mean_motion_rev_per_day,
            eccentricity=record.eccentricity, argp_deg=record.argp_deg,
            inc_deg=record.inc_deg, mean_anomaly_deg=record.mean_anomaly_deg,
            raan_deg=record.raan_deg, bstar=record.bstar, source="omm",
        )
        sat = record.to_satrec()
        self.assertEqual(sat.satnum, 100001)
        error, position, _ = sat.sgp4(record.epoch_julian() + 0.25, 0.0)
        self.assertEqual(error, 0)
        radius = sum(c * c for c in position) ** 0.5
        self.assertGreater(radius, 6300.0)  # above the surface
        self.assertLess(radius, 8000.0)  # and in LEO


class OmmRecordTest(unittest.TestCase):
    def test_parse_omm_json(self) -> None:
        records = orbit.parse_omm_json(ISS_OMM_JSON)
        self.assertEqual(len(records), 1)
        record = records[0]
        self.assertEqual(record.name, ISS_NAME)
        self.assertEqual(record.norad_id, 25544)
        self.assertEqual(record.source, "omm")
        self.assertAlmostEqual(record.mean_motion_rev_per_day, 15.54225950, places=7)
        self.assertAlmostEqual(record.inc_deg, 51.6416, places=4)
        self.assertAlmostEqual(record.bstar, 0.0001027, places=9)
        self.assertEqual(record.epoch.year, 2024)
        self.assertEqual(record.epoch.month, 10)
        self.assertEqual(record.epoch.day, 19)

    def test_epoch_parsing(self) -> None:
        parsed = orbit._parse_epoch("2026-10-21T04:35:12.123456")
        self.assertEqual(parsed.year, 2026)
        self.assertEqual(parsed.second, 12)
        self.assertEqual(parsed.microsecond, 123456)
        parsed = orbit._parse_epoch("2026-01-01T00:00:00Z")
        self.assertEqual(parsed.hour, 0)

    def test_omm_matches_same_elements_as_tle(self) -> None:
        from sgp4.api import Satrec

        reference = Satrec.twoline2rv(ISS_LINE1, ISS_LINE2)
        record = orbit.parse_omm_json(ISS_OMM_JSON)[0]
        sat = record.to_satrec()
        ref_epoch = reference.jdsatepoch + reference.jdsatepochF
        error, position, _ = reference.sgp4(ref_epoch + 0.25, 0.0)
        error2, position2, _ = sat.sgp4(record.epoch_julian() + 0.25, 0.0)
        self.assertEqual(error, 0)
        self.assertEqual(error2, 0)
        worst = max(abs(a - b) for a, b in zip(position, position2))
        self.assertLess(worst, 0.01, "OMM record drifted %s km from TLE" % worst)


class TrackerIntegrationTest(unittest.TestCase):
    def test_tracker_from_record_finds_passes(self) -> None:
        record = orbit.from_tle(ISS_NAME, ISS_LINE1, ISS_LINE2)
        tracker = Tracker.from_record(record)
        self.assertEqual(tracker.satellite.satnum, 25544)
        # Deterministic: measured from the record's epoch, not the wall clock.
        from datetime import datetime, timezone

        start = record.epoch.replace(tzinfo=timezone.utc)
        passes = tracker.find_passes(32.79, 34.99, start=start, hours=24.0,
                                     min_elevation=5.0)
        self.assertGreater(len(passes), 0, "ISS should pass over Haifa district")

    def test_tracker_from_records_partial_name_match(self) -> None:
        records = orbit.parse_omm_json(ISS_OMM_JSON)
        tracker = Tracker.from_records(records, "ISS")
        self.assertEqual(tracker.satellite.satnum, 25544)

    def test_tracker_from_records_miss(self) -> None:
        records = orbit.parse_omm_json(ISS_OMM_JSON)
        with self.assertRaises(ValueError):
            Tracker.from_records(records, "NO-SUCH-SAT")


class PlanPassIntegrationTest(unittest.TestCase):
    def test_plan_pass_with_orbit_records(self) -> None:
        from datetime import datetime, timezone

        from sdrstream import satellite as satellite_module
        from sdrstream.satellites import by_name

        records = orbit.parse_omm_json(ISS_OMM_JSON)
        # Rename to the table's name so the lookup matches.
        table_sat = by_name("ISS")
        start = records[0].epoch.replace(tzinfo=timezone.utc)
        plan = satellite_module.plan_pass(
            table_sat, latitude=32.79, longitude=34.99,
            start=start, hours=24.0, min_elevation=10.0,
            orbit_records=records,
        )
        self.assertIsNotNone(plan.orbit_record)
        self.assertEqual(plan.orbit_record.norad_id, 25544)
        self.assertGreater(len(plan.passes), 0)


if __name__ == "__main__":
    unittest.main()
