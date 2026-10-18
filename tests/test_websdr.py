"""End-to-end tests against a real WebSocket server speaking the WebSDR
protocol.

The fixtures captured from a live server are replayed over a real localhost
WebSocket, so the client's connect, Origin header, tuning and stream handling
are exercised for real.
"""

import asyncio
import os
import struct
import unittest

import websockets

from sdrstream.vocoder import WebSdrVocoder
from sdrstream.websdr import WebSDRClient

DATA = os.path.join(os.path.dirname(__file__), "data")


def _read(name):
    with open(os.path.join(DATA, name), "rb") as handle:
        return handle.read()


def _samples(name):
    raw = _read(name)
    return list(struct.unpack("<%dh" % (len(raw) // 2), raw))


class _Server:
    """A WebSDR-like server that replays captured frames."""

    def __init__(self, frames, *, require_origin=True, close_after=None):
        self.frames = list(frames)
        self.require_origin = require_origin
        self.close_after = close_after
        self.origins = []
        self.params = []
        self.connected = 0
        self._server = None
        self.port = None

    async def start(self):
        self._server = await websockets.serve(
            self._handler, "127.0.0.1", 0, process_request=self._check_origin
        )
        self.port = self._server.sockets[0].getsockname()[1]

    async def stop(self):
        if self._server is not None:
            self._server.close()
            await self._server.wait_closed()
            self._server = None

    @property
    def url(self):
        return "http://127.0.0.1:%d" % self.port

    def _check_origin(self, connection, request):
        self.origins.append(request.headers.get("Origin"))
        if self.require_origin and not request.headers.get("Origin"):
            return connection.respond(403, "Origin required\n")
        return None

    async def _handler(self, ws):
        self.connected += 1
        for frame in self.frames:
            await ws.send(frame)
            await asyncio.sleep(0.005)
        if self.close_after is not None:
            await ws.close()
            return
        try:
            async for message in ws:
                self.params.append(message)
        except websockets.ConnectionClosed:
            pass


def _run(coro):
    return asyncio.run(coro)


class TestWebSDRIntegration(unittest.TestCase):
    def test_stream_is_decoded_and_origin_sent(self):
        """The Origin header is mandatory; without it the server refuses."""
        async def main():
            server = _Server([_read("websdr_0000.bin"), _read("websdr_0020.bin")])
            await server.start()
            try:
                async with WebSDRClient(server.url) as client:
                    chunks = await client.record(1.0)
                return server, client, chunks
            finally:
                await server.stop()

        server, client, chunks = _run(main())
        self.assertEqual(server.connected, 1)
        self.assertEqual(server.origins, ["http://127.0.0.1:%d" % server.port])
        self.assertEqual(WebSDRClient.concat(chunks),
                         _samples("websdr_stream_a_ref.bin"))
        self.assertEqual(client.samplerate, 7119)

    def test_stream_matches_reference_decoder(self):
        """Decoding through the client equals the streaming reference output.

        The reference is produced by the official client's decoder fed the same
        messages in sequence; filter state carries across messages, so the
        expected samples cannot be assembled from per-frame decodes.
        """
        frames = [_read("websdr_0000.bin"), _read("websdr_0004.bin"),
                  _read("websdr_0020.bin")]
        expected = _samples("websdr_stream_b_ref.bin")

        async def main():
            server = _Server(frames)
            await server.start()
            try:
                async with WebSDRClient(server.url) as client:
                    return WebSDRClient.concat(await client.record(1.5))
            finally:
                await server.stop()

        self.assertEqual(_run(main()), expected)
        # Cross-check the fixture against a direct streaming decode.
        decoder = WebSdrVocoder()
        direct = []
        for frame in frames:
            direct.extend(decoder.decode(frame))
        self.assertEqual(direct, expected)

    def test_tuning_request_is_sent(self):
        async def main():
            server = _Server([_read("websdr_0000.bin")])
            await server.start()
            try:
                async with WebSDRClient(server.url) as client:
                    await client.tune(frequency_khz=10000.0, mode="AM",
                                      bandwidth_hz=6000)
                    await asyncio.sleep(0.2)
                return server.params
            finally:
                await server.stop()

        params = _run(main())
        self.assertTrue(params)
        request = params[0]
        self.assertTrue(request.startswith("GET /~~param?"))
        self.assertIn("f=10000.000", request)
        self.assertIn("mode=1", request)

    def test_extra_params_are_sent(self):
        async def main():
            server = _Server([_read("websdr_0000.bin")])
            await server.start()
            try:
                async with WebSDRClient(server.url) as client:
                    await client.set_squelch(3)
                    await client.set_noise_reduction(-999)
                    await asyncio.sleep(0.2)
                return server.params
            finally:
                await server.stop()

        params = _run(main())
        self.assertIn("GET /~~param?squelch=3", params)
        self.assertIn("GET /~~param?noisered=-999", params)

    def test_reconnects_after_drop(self):
        """A dropped connection is re-established and the stream resumes."""
        async def main():
            server = _Server([_read("websdr_0000.bin")], close_after=True)
            await server.start()
            try:
                client = WebSDRClient(server.url, reconnect=True,
                                      reconnect_initial_delay=0.05)
                await client.connect()
                deadline = asyncio.get_running_loop().time() + 6.0
                while server.connected < 3 and asyncio.get_running_loop().time() < deadline:
                    await asyncio.sleep(0.05)
                await client.close()
                return server.connected
            finally:
                await server.stop()

        self.assertGreaterEqual(_run(main()), 3)

    def test_no_reconnect_ends_stream(self):
        """With reconnect disabled the stream ends after the server closes.

        The frame sent before the close is delivered; what matters is that the
        reader stops rather than reconnecting.
        """
        async def main():
            server = _Server([_read("websdr_0000.bin")], close_after=True)
            await server.start()
            try:
                client = WebSDRClient(server.url, reconnect=False)
                await client.connect()
                chunks = await asyncio.wait_for(client.record(3.0), timeout=10)
                await client.close()
                return server, chunks
            finally:
                await server.stop()

        server, chunks = _run(main())
        self.assertEqual(server.connected, 1)
        self.assertEqual(WebSDRClient.concat(chunks), _samples("websdr_0000_ref.bin"))


if __name__ == "__main__":
    unittest.main()
