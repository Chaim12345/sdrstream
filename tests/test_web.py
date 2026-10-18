"""Tests for the web UI layer.

The HTTP handler is exercised directly, in process, against a real
``ThreadingHTTPServer`` on 127.0.0.1 with an ephemeral port. That keeps the
routing, the JSON encoding and the record job lifecycle honest without needing
a browser. Nothing here reaches the network: the live directory and the
SatNOGS feed are only touched through endpoints the tests do not call.
"""

import json
import threading
import unittest
import urllib.error
import urllib.request
from http.server import ThreadingHTTPServer

from sdrstream import web


class ServerFixture:
    """A real server on a free port, started for the duration of a test."""

    def __enter__(self):
        self.httpd = ThreadingHTTPServer(("127.0.0.1", 0), web.Handler)
        self.port = self.httpd.server_address[1]
        self.thread = threading.Thread(target=self.httpd.serve_forever,
                                       daemon=True)
        self.thread.start()
        return self

    def __exit__(self, *exc_info):
        self.httpd.shutdown()
        self.httpd.server_close()
        self.thread.join(timeout=5)

    def get(self, path):
        url = "http://127.0.0.1:%d%s" % (self.port, path)
        try:
            with urllib.request.urlopen(url, timeout=20) as response:
                return response.status, response.read()
        except urllib.error.HTTPError as error:
            return error.code, error.read()

    def get_json(self, path):
        status, body = self.get(path)
        return status, json.loads(body)

    def post(self, path, payload):
        url = "http://127.0.0.1:%d%s" % (self.port, path)
        data = json.dumps(payload).encode("utf-8")
        request = urllib.request.Request(
            url, data=data, headers={"Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(request, timeout=20) as response:
                return response.status, json.loads(response.read())
        except urllib.error.HTTPError as error:
            return error.code, json.loads(error.read())


class PageTest(unittest.TestCase):
    def test_the_page_is_served(self):
        with ServerFixture() as server:
            status, body = server.get("/")
        self.assertEqual(status, 200)
        self.assertIn(b"<!doctype html>", body)
        self.assertIn(b"sdrstream", body)

    def test_the_page_has_the_tabs(self):
        with ServerFixture() as server:
            _status, body = server.get("/")
        text = body.decode("utf-8")
        for tab in ("Receivers", "Satellites", "Passes", "Record",
                    "Government &amp; military", "Undocumented"):
            self.assertIn(tab, text)

    def test_a_trailing_slash_still_reaches_the_page(self):
        with ServerFixture() as server:
            status, _body = server.get("/")
        self.assertEqual(status, 200)


class SatellitesEndpointTest(unittest.TestCase):
    def test_it_lists_the_amateur_table(self):
        with ServerFixture() as server:
            status, body = server.get_json("/api/satellites")
        self.assertEqual(status, 200)
        self.assertGreater(len(body["satellites"]), 5)

    def test_each_entry_has_the_fields_the_page_uses(self):
        with ServerFixture() as server:
            _status, body = server.get_json("/api/satellites")
        for satellite in body["satellites"]:
            for key in ("name", "norad_id", "downlink_mhz", "mode", "band",
                        "status"):
                self.assertIn(key, satellite)

    def test_downlink_is_reported_in_megahertz(self):
        with ServerFixture() as server:
            _status, body = server.get_json("/api/satellites")
        iss = next(s for s in body["satellites"] if s["name"] == "ISS")
        self.assertAlmostEqual(iss["downlink_mhz"], 145.8, places=4)


class ReceiversEndpointTest(unittest.TestCase):
    def test_the_bundled_catalogue_is_used_by_default(self):
        with ServerFixture() as server:
            status, body = server.get_json("/api/receivers?frequency=145800")
        self.assertEqual(status, 200)
        self.assertEqual(body["source"], "bundled catalogue")
        self.assertIn("receivers", body)

    def test_the_frequency_is_echoed_back_in_megahertz(self):
        with ServerFixture() as server:
            _status, body = server.get_json("/api/receivers?frequency=435000")
        self.assertAlmostEqual(body["frequency_mhz"], 435.0, places=3)

    def test_a_nonsense_frequency_is_a_client_error(self):
        with ServerFixture() as server:
            status, body = server.get_json("/api/receivers?frequency=abc")
        self.assertEqual(status, 400)
        self.assertIn("error", body)


class MilitaryEndpointTest(unittest.TestCase):
    def test_it_returns_the_table_with_reachability(self):
        with ServerFixture() as server:
            status, body = server.get_json("/api/military")
        self.assertEqual(status, 200)
        self.assertGreater(len(body["satellites"]), 5)
        for satellite in body["satellites"]:
            for key in ("name", "operator", "downlink_mhz", "band",
                        "encryption", "reachable", "receivers", "source"):
                self.assertIn(key, satellite)

    def test_it_reports_what_is_out_of_reach(self):
        with ServerFixture() as server:
            _status, body = server.get_json("/api/military")
        names = {entry["name"] for entry in body["out_of_reach"]}
        self.assertIn("Milstar / AEHF", names)

    def test_fltsatcom_is_present_and_labelled_open(self):
        with ServerFixture() as server:
            _status, body = server.get_json("/api/military")
        flt = next(s for s in body["satellites"] if s["name"] == "FLTSATCOM")
        self.assertEqual(flt["encryption"], "open")
        self.assertEqual(flt["downlink_mhz"], 255.55)


class PassesEndpointTest(unittest.TestCase):
    def test_a_missing_satellite_is_a_client_error(self):
        with ServerFixture() as server:
            status, body = server.get_json("/api/passes")
        self.assertEqual(status, 400)
        self.assertIn("error", body)

    def test_an_unknown_satellite_is_a_client_error(self):
        with ServerFixture() as server:
            status, body = server.get_json("/api/passes?satellite=NOPE-1")
        self.assertEqual(status, 400)


class RecordEndpointTest(unittest.TestCase):
    def test_a_job_without_a_server_is_reported_as_an_error(self):
        with ServerFixture() as server:
            status, body = server.post("/api/record",
                                       {"seconds": 1, "server": ""})
            self.assertEqual(status, 202)
            job_id = body["id"]
            for _ in range(50):
                _status, job = server.get_json("/api/record/" + job_id)
                if job["state"] != "running":
                    break
        self.assertEqual(job["state"], "error")
        self.assertIn("server is required", job["error"])

    def test_an_unknown_job_is_not_found(self):
        with ServerFixture() as server:
            status, body = server.get_json("/api/record/deadbeef")
        self.assertEqual(status, 404)
        self.assertIn("error", body)

    def test_audio_is_not_served_before_a_job_finishes(self):
        with ServerFixture() as server:
            _status, body = server.post("/api/record",
                                        {"seconds": 1, "server": ""})
            status, payload = server.get_json(
                "/api/record/%s/audio" % body["id"])
        self.assertEqual(status, 409)
        self.assertIn("error", payload)

    def test_a_finished_job_reports_its_numbers(self):
        # Driven straight through the job object, so no socket is opened.
        job = web.RecordJob(id="test", seconds=2.0)
        job.state, job.samples, job.samplerate, job.peak = "done", 100, 8000, 7
        public = job.public()
        self.assertEqual(public["state"], "done")
        self.assertEqual(public["samples"], 100)
        self.assertNotIn("wav", public)


class RoutingTest(unittest.TestCase):
    def test_an_unknown_path_is_not_found(self):
        with ServerFixture() as server:
            status, body = server.get_json("/api/nonsense")
        self.assertEqual(status, 404)
        self.assertIn("error", body)

    def test_an_unknown_post_is_not_found(self):
        with ServerFixture() as server:
            status, body = server.post("/api/nonsense", {})
        self.assertEqual(status, 404)
        self.assertIn("error", body)

    def test_bad_json_is_a_client_error(self):
        with ServerFixture() as server:
            url = "http://127.0.0.1:%d/api/record" % server.port
            request = urllib.request.Request(
                url, data=b"{not json",
                headers={"Content-Type": "application/json"})
            try:
                with urllib.request.urlopen(request, timeout=20) as response:
                    status = response.status
            except urllib.error.HTTPError as error:
                status = error.code
        self.assertEqual(status, 400)


if __name__ == "__main__":
    unittest.main()
