"""Tests for the KiwiSDR public-directory parser and policy filter."""

from __future__ import annotations

import json
import tempfile
import unittest
import urllib.error
from pathlib import Path
from unittest import mock

from sdrstream import kiwisdr_directory


def _payload(receivers, schema=1):
    return json.dumps({
        "schema": schema,
        "source": "https://files.kiwisdr.com/public/",
        "fetched_at": "2026-10-21T06:00:20Z",
        "receiver_count": len(receivers),
        "receivers": receivers,
    })


RECEIVERS = [
    {
        "url": "http://api-ok:8073", "name": "API open", "loc": "Somewhere",
        "grid": "JN05ot", "gps": [46.5, 1.5], "bands": [0, 30000000],
        "users": 2, "users_max": 8, "ext_api": 4, "offline": False,
    },
    {
        "url": "http://api-off:8073", "name": "Browser only",
        "gps": [46.5, 1.5], "bands": [0, 30000000],
        "users": 8, "users_max": 8, "ext_api": 0, "offline": False,
    },
    {
        "url": "http://no-policy:8073", "name": "Policy not published",
        "gps": [46.5, 1.5], "bands": [0, 30000000],
        "users": 0, "users_max": 4, "offline": False,
    },
    {
        "url": "http://offline-rx:8073", "name": "Asleep",
        "gps": [46.5, 1.5], "bands": [0, 30000000],
        "ext_api": 2, "offline": True,
    },
    {
        "url": "", "name": "No URL is skipped",
        "gps": [1.0, 1.0], "bands": [0, 30000000], "ext_api": 4,
    },
    {
        "url": "http://no-gps:8073", "name": "Null island",
        "gps": [0, 0], "bands": [0, 30000000], "ext_api": 4,
    },
    {
        "url": "http://bad-bands:8073", "name": "Zero width",
        "gps": [1.0, 1.0], "bands": [435000000, 435000000], "ext_api": 4,
    },
]


class ParseDirectoryTest(unittest.TestCase):
    def test_parses_entries_and_policies(self) -> None:
        receivers = kiwisdr_directory.parse_directory(_payload(RECEIVERS))
        # The no-URL entry is skipped; everything else is kept.
        self.assertEqual(len(receivers), 6)
        by_url = {r.url: r for r in receivers}
        self.assertEqual(by_url["http://api-ok:8073"].ext_api, 4)
        self.assertEqual(by_url["http://api-off:8073"].ext_api, 0)
        self.assertEqual(by_url["http://no-policy:8073"].ext_api, -1)
        self.assertFalse(by_url["http://no-policy:8073"].api_policy_published)
        self.assertEqual(by_url["http://offline-rx:8073"].offline, True)

    def test_null_island_gps_is_no_fix(self) -> None:
        receivers = kiwisdr_directory.parse_directory(_payload(RECEIVERS))
        by_url = {r.url: r for r in receivers}
        self.assertIsNone(by_url["http://no-gps:8073"].latitude)
        self.assertIsNone(by_url["http://no-gps:8073"].longitude)
        self.assertIsNotNone(by_url["http://api-ok:8073"].latitude)

    def test_zero_width_band_is_dropped(self) -> None:
        receivers = kiwisdr_directory.parse_directory(_payload(RECEIVERS))
        by_url = {r.url: r for r in receivers}
        self.assertEqual(by_url["http://bad-bands:8073"].bands, ())

    def test_array_envelope_is_refused(self) -> None:
        with self.assertRaises(ValueError):
            kiwisdr_directory.parse_directory("[]")

    def test_unknown_schema_is_refused(self) -> None:
        with self.assertRaises(ValueError):
            kiwisdr_directory.parse_directory(_payload(RECEIVERS, schema=2))


class PolicyFilterTest(unittest.TestCase):
    def _receivers(self):
        return kiwisdr_directory.parse_directory(_payload(RECEIVERS))

    def test_only_explicitly_permitted_receivers(self) -> None:
        permitted = kiwisdr_directory.api_permitted(self._receivers())
        urls = [r.url for r in permitted]
        self.assertIn("http://api-ok:8073", urls)
        self.assertNotIn("http://api-off:8073", urls)
        self.assertNotIn("http://no-policy:8073", urls)
        self.assertNotIn("http://offline-rx:8073", urls)

    def test_offline_included_on_request(self) -> None:
        permitted = kiwisdr_directory.api_permitted(
            self._receivers(), include_offline=True)
        self.assertIn("http://offline-rx:8073", [r.url for r in permitted])

    def test_for_frequency(self) -> None:
        receivers = self._receivers()
        on_20m = kiwisdr_directory.for_frequency(14_070_000, receivers)
        self.assertIn("http://api-ok:8073", [r.url for r in on_20m])
        # The bad-bands receiver only publishes a zero-width band.
        self.assertNotIn("http://bad-bands:8073",
                         [r.url for r in on_20m])


class DiscoverTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self._env = mock.patch.dict(
            "os.environ", {"SDRSTREAM_CACHE": str(Path(self._tmp.name))})
        self._env.start()
        kiwisdr_directory._CACHE_PATH = (
            Path(self._tmp.name) / "kiwisdr_directory.json")
        self.addCleanup(self._env.stop)

    def tearDown(self):
        self._tmp.cleanup()

    def test_discover_fetches_and_caches(self) -> None:
        body = _payload(RECEIVERS)
        calls = []

        def fake_urlopen(request, timeout=30.0):
            calls.append(getattr(request, "full_url", str(request)))
            class _Resp:
                def read(self):
                    return body.encode("utf-8")

                def __enter__(self):
                    return self

                def __exit__(self, *args):
                    return False
            return _Resp()

        with mock.patch("urllib.request.urlopen", side_effect=fake_urlopen):
            first = kiwisdr_directory.discover()
            second = kiwisdr_directory.discover()
        self.assertEqual(len(first), 6)
        # The second call is served from the cache: one network hit total.
        self.assertEqual(len(calls), 1)

    def test_discover_offline_uses_stale_cache(self) -> None:
        with mock.patch("urllib.request.urlopen",
                        return_value=None) as opener:
            opener.side_effect = urllib.error.URLError("down")
            with self.assertRaises(urllib.error.URLError):
                kiwisdr_directory.discover()
        # Seed the cache, then go offline: discovery still works.
        kiwisdr_directory._CACHE_PATH.write_text(_payload(RECEIVERS))

        def failing(request, timeout=30.0):
            raise urllib.error.URLError("down")

        with mock.patch("urllib.request.urlopen", side_effect=failing):
            # Expire the cache so the network path is attempted first.
            import os
            os.utime(kiwisdr_directory._CACHE_PATH, (0, 0))
            offline = kiwisdr_directory.discover()
        self.assertEqual(len(offline), 6)


if __name__ == "__main__":
    unittest.main()
