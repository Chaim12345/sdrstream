"""Tests for the SWPC space-weather fetch (fixture-based, no network)."""

from __future__ import annotations

import unittest
from unittest import mock

from sdrstream import spacewx


KP_ROWS = [
    {"time_tag": "2026-10-21T05:00:00Z", "kp_index": 2, "estimated_kp": 2.0},
    {"time_tag": "2026-10-21T06:00:00Z", "kp_index": 3, "estimated_kp": 3.0},
]

FLUX_ROWS = [
    # Trailing average rows arrive out of order; the newest tag must win.
    {"time_tag": "2026-10-20T22:00:00", "flux": 120.0},
    {"time_tag": "2026-09-10T17:00:00", "flux": 115.0},
]

XRAY_ROWS = [
    {"time_tag": "2026-10-21T06:39:00Z", "flux": 5.7e-07},
]

SUNSPOTS = [
    {"Region": 4551, "Location": "S17W70", "Latitude": -17, "Longitude": -70},
    {"Region": 4557, "Location": "N05W55", "Latitude": 5, "Longitude": -55},
]


def _products(responses):
    """Patch _get to serve canned URLs."""
    def fake_get(url, timeout=30.0):
        for fragment, payload in responses.items():
            if fragment in url:
                return payload
        return None
    return mock.patch.object(spacewx, "_get", side_effect=fake_get)


class SpaceWeatherTest(unittest.TestCase):
    def test_fetches_all_products(self) -> None:
        with _products({
            "planetary_k_index": KP_ROWS,
            "f107_cm_flux": FLUX_ROWS,
            "xrays": XRAY_ROWS,
            "sunspot_report": SUNSPOTS,
        }):
            report = spacewx.space_weather()
        self.assertEqual(report.kp, 3)
        self.assertEqual(report.kp_time.hour, 6)
        self.assertEqual(report.flux_107cm, 120.0)  # newest tag, not last row
        self.assertEqual(report.xray_flux, 5.7e-07)
        self.assertEqual(len(report.sunspot_regions), 2)
        self.assertEqual(report.sunspot_regions[0].number, 4551)
        self.assertEqual(report.sunspot_regions[0].latitude_deg, -17)

    def test_xray_class_thresholds(self) -> None:
        report = spacewx.SpaceWeather(xray_flux=5.7e-07)
        self.assertEqual(report.xray_class(), "B5.7")
        report = spacewx.SpaceWeather(xray_flux=2.5e-05)
        self.assertEqual(report.xray_class(), "M2.5")
        report = spacewx.SpaceWeather(xray_flux=2.5e-04)
        self.assertEqual(report.xray_class(), "X2.5")
        report = spacewx.SpaceWeather(xray_flux=9.0e-09)
        self.assertEqual(report.xray_class(), "A0.9")
        report = spacewx.SpaceWeather(xray_flux=None)
        self.assertEqual(report.xray_class(), "?")

    def test_one_down_product_does_not_hide_the_rest(self) -> None:
        with _products({
            "planetary_k_index": KP_ROWS,
            "f107_cm_flux": None,  # simulated outage
        }):
            report = spacewx.space_weather()
        self.assertEqual(report.kp, 3)
        self.assertIsNone(report.flux_107cm)

    def test_verdict_and_summary(self) -> None:
        report = spacewx.SpaceWeather(kp=1.0, flux_107cm=120.0,
                                      xray_flux=5.7e-07)
        self.assertIn("quiet", report.verdict())
        self.assertIn("Kp 1.0", report.verdict())
        text = report.summary()
        self.assertIn("planetary Kp", text)
        self.assertIn("B5.7", text)

    def test_disturbed_verdict(self) -> None:
        report = spacewx.SpaceWeather(kp=7.5)
        self.assertIn("disturbed", report.verdict())


if __name__ == "__main__":
    unittest.main()
