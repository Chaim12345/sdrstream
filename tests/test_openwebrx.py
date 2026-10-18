"""End-to-end tests against a real WebSocket server speaking the OpenWebRX
protocol.

The server is a real :mod:`websockets` server bound to localhost, so the client
exercises its genuine connect, handshake, tuning, framing and decoding paths.
"""

import asyncio
import json
import struct
import unittest

import websockets

from sdrstream.adpcm import AdpcmDecoder
from sdrstream.openwebrx import (
    OPCODE_AUDIO,
    OPCODE_FFT,
    OpenWebRXClient,
)

HANDSHAKE_PREFIX = "SERVER DE CLIENT"


def _sync_stream():
    """Same payload used by tests/test_adpcm.py, with known reference output."""
    parts = [((i * 37 + 11) & 0xFF) for i in range(16)]
    parts += list(b"SYNC")
    parts += list(struct.pack("<hh", 40, -1234))
    parts += [((i * 91 + 7) & 0xFF) for i in range(40)]
    return bytes(parts)


def _pcm_frame(samples):
    return bytes([OPCODE_AUDIO]) + struct.pack("<%dh" % len(samples), *samples)


class _Server:
    """A scriptable OpenWebRX server used by the tests."""

    def __init__(self, *, audio_compression="none", frames=(), fft_frames=(),
                 config_parts=None):
        self.audio_compression = audio_compression
        self.frames = list(frames)
        self.fft_frames = list(fft_frames)
        self.config_parts = config_parts or [
            {"fft_compression": "none", "fft_size": 4096},
            {
                "center_freq": 7100000,
                "samp_rate": 2400000,
                "start_mod": "lsb",
                "audio_compression": audio_compression,
            },
        ]
        self.received = []
        self.connected = 0
        self._server = None
        self.port = None

    async def start(self):
        self._server = await websockets.serve(self._handler, "127.0.0.1", 0)
        self.port = self._server.sockets[0].getsockname()[1]

    async def stop(self):
        if self._server is not None:
            self._server.close()
            await self._server.wait_closed()
            self._server = None

    @property
    def url(self):
        return "ws://127.0.0.1:%d/ws/" % self.port

    async def _handler(self, ws):
        self.connected += 1
        handshake = await ws.recv()
        self.received.append(handshake)
        await ws.send("CLIENT DE SERVER server=openwebrx version=v1.2.126")
        for part in self.config_parts:
            await ws.send(json.dumps({"type": "config", "value": part}))
        await ws.send(json.dumps({"type": "modes", "value": [
            {"modulation": "lsb"}, {"modulation": "usb"},
        ]}))
        await ws.send(json.dumps({"type": "smeter", "value": -73}))
        async for message in ws:
            self.received.append(message)
            if "connectionproperties" in message:
                continue
            if "dspcontrol" in message:
                for frame in self.frames:
                    await ws.send(frame)
                    await asyncio.sleep(0.005)
                for frame in self.fft_frames:
                    await ws.send(frame)
                    await asyncio.sleep(0.005)


def _run(coro):
    return asyncio.run(coro)


class TestOpenWebRXIntegration(unittest.TestCase):
    def test_pcm_audio_is_streamed(self):
        async def main():
            samples = [0, 1000, -1000, 32767, -32768, 500]
            server = _Server(frames=[_pcm_frame(samples)])
            await server.start()
            try:
                async with OpenWebRXClient(server.url) as client:
                    chunks = await client.record(1.5)
                return server, client, chunks
            finally:
                await server.stop()

        server, client, chunks = _run(main())
        self.assertEqual(client.config.center_freq, 7100000)
        self.assertEqual(client.config.audio_compression, "none")
        self.assertTrue(server.received[0].startswith(HANDSHAKE_PREFIX))
        self.assertEqual(OpenWebRXClient.concat(chunks), [0, 1000, -1000, 32767, -32768, 500])
        self.assertEqual(chunks[0].samplerate, 12000)
        self.assertFalse(chunks[0].compressed)

    def test_adpcm_audio_is_decoded(self):
        """ADPCM payloads must match the reference decoder exactly."""
        stream = _sync_stream()
        expected = AdpcmDecoder().decode_with_sync(stream)

        async def main():
            server = _Server(audio_compression="adpcm",
                             frames=[bytes([OPCODE_AUDIO]) + stream])
            await server.start()
            try:
                async with OpenWebRXClient(server.url) as client:
                    chunks = await client.record(1.5)
                return chunks
            finally:
                await server.stop()

        chunks = _run(main())
        self.assertEqual(OpenWebRXClient.concat(chunks), expected)
        self.assertTrue(chunks[0].compressed)

    def test_adpcm_state_carries_across_messages(self):
        """Splitting an ADPCM payload across messages must not change output."""
        stream = _sync_stream()
        expected = AdpcmDecoder().decode_with_sync(stream)
        cut = 20

        async def main():
            server = _Server(
                audio_compression="adpcm",
                frames=[
                    bytes([OPCODE_AUDIO]) + stream[:cut],
                    bytes([OPCODE_AUDIO]) + stream[cut:],
                ],
            )
            await server.start()
            try:
                async with OpenWebRXClient(server.url) as client:
                    chunks = await client.record(1.5)
                return chunks
            finally:
                await server.stop()

        chunks = _run(main())
        self.assertEqual(OpenWebRXClient.concat(chunks), expected)

    def test_await_metadata_collects_lists(self):
        """``await_metadata`` reads until modes and profiles have arrived."""
        async def main():
            server = _Server()
            await server.start()
            try:
                async with OpenWebRXClient(server.url) as client:
                    await client.await_metadata()
                    return client.modes, client.profiles
            finally:
                await server.stop()

        modes, profiles = _run(main())
        self.assertEqual([m["modulation"] for m in modes], ["lsb", "usb"])
        self.assertEqual(profiles, [])

    def test_split_config_is_merged(self):
        async def main():
            server = _Server()
            await server.start()
            try:
                async with OpenWebRXClient(server.url) as client:
                    # ``modes`` and ``smeter`` are pushed after connect() has
                    # already returned, so drain events until they arrive.
                    async def drain():
                        async for event in client.events():
                            if event["type"] == "smeter":
                                return
                    await asyncio.wait_for(drain(), timeout=5)
                    return client.config, client.modes, client.smeter
            finally:
                await server.stop()

        config, modes, smeter = _run(main())
        self.assertEqual(config.center_freq, 7100000)
        self.assertEqual(config.samp_rate, 2400000)
        self.assertEqual(config.audio_compression, "none")
        self.assertEqual(len(modes), 2)
        self.assertEqual(smeter, -73)

    def test_tuning_message_is_sent(self):
        async def main():
            server = _Server()
            await server.start()
            try:
                async with OpenWebRXClient(server.url) as client:
                    await client.tune(frequency=7150000, modulation="usb", bandwidth=2400)
                    await asyncio.sleep(0.1)
                    return server.received
            finally:
                await server.stop()

        received = _run(main())
        controls = [json.loads(m) for m in received if m.startswith("{") and "dspcontrol" in m]
        tuning = [c for c in controls if c.get("frequency") == 7150000]
        self.assertTrue(tuning, controls)
        self.assertEqual(tuning[0]["modulation"], "usb")
        self.assertEqual(tuning[0]["bandwidth"], 2400)
        self.assertTrue(any(c.get("action") == "start" for c in controls))

    def test_fft_frames_are_reported(self):
        fft_payload = bytes([OPCODE_FFT]) + bytes(range(32))

        async def main():
            server = _Server(fft_frames=[fft_payload])
            await server.start()
            try:
                async with OpenWebRXClient(server.url) as client:
                    events = []
                    async for event in client.events():
                        events.append(event)
                        if any(e["type"] == "fft" for e in events):
                            return events
                return events
            finally:
                await server.stop()

        events = _run(main())
        fft = [e for e in events if e["type"] == "fft"]
        self.assertTrue(fft)
        # Uncompressed FFT payloads are raw little-endian int16 samples.
        self.assertEqual(len(fft[0]["value"]), 16)

    def test_closing_without_reconnect_ends_stream(self):
        async def main():
            server = _Server()
            await server.start()
            try:
                client = OpenWebRXClient(server.url, reconnect=False)
                await client.connect()
                await server.stop()
                chunks = await asyncio.wait_for(client.record(3.0), timeout=10)
                await client.close()
                return chunks
            finally:
                await server.stop()

        self.assertEqual(_run(main()), [])


if __name__ == "__main__":
    unittest.main()
