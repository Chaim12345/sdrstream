"""Tests for the live catalogue refresh (mocked WebSDR server list)."""

from __future__ import annotations

import json
import tempfile
import unittest
import urllib.error
from pathlib import Path
from unittest import mock

from sdrstream import catalogue


SERVER_LIST_BODY = (
    'websdrlist({"version": 1});'
    + json.dumps([
        {
            "url": "http://example-2m:8901",
            "lat": 32.79, "lon": 34.99,
            "bands": [{"c": "m2", "l": 144.0, "h": 146.0}],
        },
        {
            "url": "http://example-hf-only:8902",
            "lat": 0.0, "lon": 0.0,
            "bands": [{"c": "m40", "l": 7.0, "h": 7.2}],
        },
        {
            "url": "http://example-bad:8903",
            "lat": 1.0, "lon": 1.0,
            "bands": [{"c": "x", "l": 435.0, "h": 435.0}],  # zero width
        },
        {
            "url": "",
            "lat": 2.0, "lon": 2.0,
            "bands": [{"c": "m70", "l": 435.0, "h": 438.0}],
        },
    ])
)


class _FakeResponse:
    def __init__(self, body: str):
        self._body = body.encode("utf-8")

    def read(self) -> bytes:
        return self._body

    def __enter__(self):
        return self

    def __exit__(self, *args):
        return False


class CatalogueRefreshTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.cache_dir = Path(self._tmp.name)
        # Redirect the cache location so tests never touch the real one.
        self._env = mock.patch.dict(
            "os.environ", {"SDRSTREAM_CACHE": str(self.cache_dir)})
        self._env.start()
        self._reload_cache_path()
        self._original = dict(catalogue.RECEIVERS)
        self.addCleanup(self._restore)

    def tearDown(self):
        self._tmp.cleanup()

    def _reload_cache_path(self) -> None:
        # Re-derive the cache path under the patched environment.
        catalogue._CATALOGUE_CACHE = (self.cache_dir / "websdr_catalogue.json")

    def _restore(self) -> None:
        self._env.stop()
        catalogue._CATALOGUE_CACHE = (Path(
            catalogue.__dict__.get("_CATALOGUE_CACHE_DIR_FALLBACK",
                                  Path.home() / ".cache" / "sdrstream"))
            / "websdr_catalogue.json")
        catalogue.RECEIVERS.clear()
        catalogue.RECEIVERS.update(self._original)

    def _patch_urlopen(self, body: str):
        def fake_urlopen(request, timeout=30.0):
            return _FakeResponse(body)
        return mock.patch("urllib.request.urlopen", side_effect=fake_urlopen)

    def test_refresh_swaps_in_live_receivers(self) -> None:
        with self._patch_urlopen(SERVER_LIST_BODY):
            count = catalogue.refresh()
        # The HF-only server has no satellite overlap but is still kept as a
        # receiver with published bands; the zero-width band and the missing
        # URL are dropped.
        self.assertEqual(count, 2)
        self.assertIn("http://example-2m:8901", catalogue.RECEIVERS)
        receiver = catalogue.RECEIVERS["http://example-2m:8901"]
        self.assertIn("2m", receiver.satellite_bands)
        self.assertTrue(receiver.can_hear(145_800_000.0))

    def test_refresh_writes_the_cache(self) -> None:
        with self._patch_urlopen(SERVER_LIST_BODY):
            catalogue.refresh()
        self.assertTrue(catalogue._CATALOGUE_CACHE.exists())
        # The cache holds the list as fetched, preamble included.
        stored = catalogue._parse_server_list(
            catalogue._CATALOGUE_CACHE.read_text())
        self.assertEqual(len(stored), 4)

    def test_refresh_falls_back_to_the_cache_when_offline(self) -> None:
        with self._patch_urlopen(SERVER_LIST_BODY):
            catalogue.refresh()
        def failing_urlopen(request, timeout=30.0):
            raise urllib.error.URLError("network down")
        with mock.patch("urllib.request.urlopen", side_effect=failing_urlopen):
            # A second refresh with the network down uses the cached copy.
            count = catalogue.refresh()
        self.assertEqual(count, 2)

    def test_refresh_without_cache_or_network_raises(self) -> None:
        def failing_urlopen(request, timeout=30.0):
            raise urllib.error.URLError("network down")
        with mock.patch("urllib.request.urlopen", side_effect=failing_urlopen):
            with self.assertRaises(urllib.error.URLError):
                catalogue.refresh(cache=False)

    def test_parse_server_list_skips_the_preamble(self) -> None:
        entries = catalogue._parse_server_list(SERVER_LIST_BODY)
        self.assertEqual(len(entries), 4)


if __name__ == "__main__":
    unittest.main()
