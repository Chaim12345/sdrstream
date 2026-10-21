"""Tests for the SatNOGS telemetry-frame fetch (token-authenticated)."""

from __future__ import annotations

import json
import unittest
import urllib.error
import urllib.parse
from datetime import datetime, timezone
from unittest import mock

from sdrstream import satnogs


class _FakeResponse:
    def __init__(self, body: str):
        self._body = body.encode("utf-8")

    def read(self) -> bytes:
        return self._body

    def __enter__(self):
        return self

    def __exit__(self, *args):
        return False


def _patch_urlopen(responses):
    """Patch urlopen; *responses* maps a URL substring to a body."""
    calls = []

    def fake_urlopen(request, timeout=30.0):
        url = getattr(request, "full_url", str(request))
        calls.append((url, dict(getattr(request, "headers", {}))))
        for fragment, body in responses.items():
            if fragment in url:
                return _FakeResponse(body)
        raise urllib.error.HTTPError(url, 404, "not found", {}, None)

    return calls, mock.patch("urllib.request.urlopen", side_effect=fake_urlopen)


FRAMES_BODY = json.dumps({
    "results": [
        {
            "id": 15244223,
            "sat_id": "XSKZ-5603-1870-9019-3066",
            "norad_cat_id": 25544,
            "transmitter": "DKkitSYmcbsaMq7upo6Ldg",
            "frame": "a8e0a8a066a0e0a0aab8aaaa8ba1c39d7626716c20",
            "decoded": "",
            "observation_id": 15244223,
            "station_id": 5153,
            "timestamp": "2026-10-21T04:35:44Z",
        },
    ],
    "next": None,
})


class TokenTest(unittest.TestCase):
    def test_explicit_token_wins(self) -> None:
        self.assertEqual(satnogs.api_token("abc"), "abc")

    def test_environment_token(self) -> None:
        with mock.patch.dict("os.environ", {"SATNOGS_TOKEN": "envtok"}):
            self.assertEqual(satnogs.api_token(), "envtok")
            self.assertEqual(satnogs.api_token("explicit"), "explicit")

    def test_no_token_is_none(self) -> None:
        self.assertIsNone(satnogs.api_token())


class TelemetryFramesTest(unittest.TestCase):
    def test_requires_a_token(self) -> None:
        with self.assertRaises(RuntimeError) as raised:
            satnogs.telemetry_frames(25544)
        self.assertIn("SATNOGS_TOKEN", str(raised.exception))

    def test_fetches_frames_with_auth_header(self) -> None:
        calls, patch = _patch_urlopen({"/api/telemetry/": FRAMES_BODY})
        with patch:
            frames = satnogs.telemetry_frames(25544, token="sekrit")
        self.assertEqual(len(frames), 1)
        frame = frames[0]
        self.assertEqual(frame.norad_id, 25544)
        self.assertEqual(frame.observation_id, 15244223)
        self.assertEqual(frame.station_id, 5153)
        self.assertEqual(frame.timestamp.year, 2026)
        self.assertIsNotNone(frame.frame)

        telemetry_calls = [(u, h) for u, h in calls if "/api/telemetry/" in u]
        self.assertEqual(len(telemetry_calls), 1)
        url, headers = telemetry_calls[0]
        # The endpoint's satellite filter takes the NORAD catalog number.
        self.assertIn("satellite=25544", url)
        self.assertEqual(headers.get("Authorization"), "Token sekrit")

    def test_satellite_uuid_filter(self) -> None:
        calls, patch = _patch_urlopen({"/api/telemetry/": FRAMES_BODY})
        with patch:
            satnogs.telemetry_frames(sat_id="XSKZ-5603-1870-9019-3066",
                                     token="tok")
        url, _ = [(u, h) for u, h in calls if "/api/telemetry/" in u][0]
        self.assertIn("sat_id=XSKZ-5603-1870-9019-3066", url)
        self.assertNotIn("satellite=", url)

    def test_unknown_satellite_returns_empty(self) -> None:
        empty = json.dumps({"results": [], "next": None})
        _, patch = _patch_urlopen({"/api/telemetry/": empty})
        with patch:
            frames = satnogs.telemetry_frames(999999, token="tok")
        self.assertEqual(frames, [])

    def test_pages_through_cursor(self) -> None:
        # The API returns an absolute next-page URL, but the cursor parameter
        # takes the token alone; sending the whole URL back is a 404.
        cursor = "cD0yMDI2LTEwLTIwKzE3JTNBNTclM0ExNSUyQjAwJTNBMDA="
        first = json.dumps({
            "results": [{"id": 1, "norad_cat_id": 25544, "frame": "aa"}],
            "next": "https://db.satnogs.org/api/telemetry/?cursor=%s&format=json"
                    % urllib.parse.quote(cursor, safe=""),
        })
        second = json.dumps({
            "results": [{"id": 2, "norad_cat_id": 25544, "frame": "bb"}],
            "next": None,
        })

        def fake_urlopen(request, timeout=30.0):
            url = getattr(request, "full_url", str(request))
            calls.append(url)
            return _FakeResponse(second if "cursor=" in url else first)

        calls: list = []
        with mock.patch("urllib.request.urlopen", side_effect=fake_urlopen):
            frames = satnogs.telemetry_frames(25544, limit=10, token="tok")

        self.assertEqual([f.id for f in frames], [1, 2])
        self.assertEqual(len(calls), 2)
        # The follow-up request must carry the bare token, not the whole URL.
        self.assertIn("cursor=" + urllib.parse.quote(cursor, safe=""), calls[1])
        self.assertNotIn("cursor=https", calls[1])

    def test_frame_summary(self) -> None:
        record = satnogs._telemetry_frame({
            "id": 7, "sat_id": "uuid", "norad_cat_id": 25544,
            "transmitter": "t", "frame": "aabb",
            "decoded": '{"v": 1}', "observation_id": 9, "station_id": 4,
            "timestamp": "2026-10-21T04:35:44Z",
        })
        summary = record.summary()
        self.assertIn("decoded)", summary)
        self.assertIn("NORAD 25544", summary)

    def test_api_time_format(self) -> None:
        stamp = satnogs._api_time(datetime(2026, 10, 21, 4, 35, 44,
                                           tzinfo=timezone.utc))
        self.assertEqual(stamp, "2026-10-21T04:35:44Z")


if __name__ == "__main__":
    unittest.main()
