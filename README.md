# sdrstream

Stream and decode audio from public **WebSDR** and **OpenWebRX / OpenWebRX+**
servers, in pure Python.

The library talks to the servers directly over their native WebSocket protocols,
decodes the audio the same way the official browser clients do, and hands you
16-bit PCM you can write to a WAV file or process with numpy.

Both decoders are verified **bit-exact** against the reference JavaScript
clients:

* `sdrstream.vocoder` reproduces the WebSDR `websdr-sound.js` output sample for
  sample.
* `sdrstream.adpcm` reproduces the openwebrx-js `AdpcmDecoder` output sample for
  sample.

## Install

```bash
pip install .            # library only
pip install .[analysis]  # adds numpy for spectrum work
```

The only runtime dependency is [`websockets`](https://pypi.org/project/websockets/).
WAV output uses the standard library; `numpy`/`scipy` are not required.

## Quick start

### OpenWebRX

```python
import asyncio
from sdrstream import OpenWebRXClient, write_wav

async def main():
    async with OpenWebRXClient("wss://sdr.example.org/ws/") as client:
        print(client.config.center_freq, client.config.audio_compression)
        await client.tune(frequency=7100000, modulation="lsb")
        chunks = await client.record(10.0)          # 10 seconds
        samples = OpenWebRXClient.concat(chunks)
        write_wav("40m.wav", samples, chunks[0].samplerate)

asyncio.run(main())
```

### WebSDR

```python
import asyncio
from sdrstream import WebSDRClient, write_wav

async def main():
    async with WebSDRClient("http://websdr.ewi.utwente.nl:8901") as client:
        await client.tune(frequency_khz=10000.0, mode="AM")
        chunks = await client.record(10.0)
        samples = WebSDRClient.concat(chunks)
        write_wav("wwv.wav", samples, chunks[0].samplerate)

asyncio.run(main())
```

### Streaming instead of recording

`audio()` yields chunks as they arrive, which suits live processing:

```python
async with OpenWebRXClient(url) as client:
    await client.tune(frequency=7100000, modulation="lsb")
    async for chunk in client.audio():
        process(chunk.samples, chunk.samplerate)
```

`events()` yields everything instead: audio, FFT frames, S-meter readings,
configuration updates and errors.

## Command line

```bash
# Record 15 s of 40 m LSB from an OpenWebRX server
python -m sdrstream record --server wss://sdr.example.org/ws/ \
    --freq 7150000 --mode lsb --seconds 15 --out 40m.wav

# Record from a WebSDR server (frequency in kHz, mode in capitals)
python -m sdrstream record --kind websdr --server websdr.example.org:8901 \
    --freq 10000 --mode AM --seconds 10 --out wwv.wav

# Show what a server offers without recording
python -m sdrstream info --server wss://sdr.example.org/ws/
```

## Protocol notes

Both protocols are undocumented in any formal sense, so the details below come
from reading the reference clients and observing live servers.

### OpenWebRX

1. Connect to `ws(s)://<host>/ws/` and send the text handshake
   `SERVER DE CLIENT client=<name> type=receiver`.
2. The server replies `CLIENT DE SERVER ...` then sends JSON control messages.
   The `config` message is **split over several frames** and must be merged;
   only the final part carries `center_freq`.
3. Send `{"type": "dspcontrol", ...}` with the tuning parameters, then
   `{"type": "dspcontrol", "action": "start"}`.
4. Binary frames follow. The first byte is an opcode:

   | Opcode | Payload | Meaning |
   |--------|---------|---------|
   | `0x01` | FFT | Waterfall spectrum |
   | `0x02` | AUDIO | Demodulated audio |
   | `0x03` | — | Secondary FFT |
   | `0x04` | HD AUDIO | High-definition audio |

   Audio may be raw int16 PCM or IMA ADPCM, per `audio_compression` in the
   config. ADPCM audio embeds `SYNC` markers every 1000 bytes carrying the
   decoder state, so a stream can be joined mid-flight; FFT ADPCM has no
   markers.

### WebSDR

1. Connect to `ws(s)://<host>/~~stream`. There is no handshake — audio starts
   immediately. The server **requires an `Origin` header** and drops the
   connection without one.
2. Retune by sending a plain text frame:
   `GET /~~param?f=<kHz>&band=0&lo=<kHz>&hi=<kHz>&mode=<code>&name=`.
   Mode codes are `USB=0`, `LSB=0`, `CW=0`, `AM=1`, `AMSYNC=2`, `FM=4`; SSB and
   CW differ only in the passband offsets.
3. Binary frames carry a mixed stream. Control bytes: `0x80` (128 samples from a
   companding table), `0x81` (sample rate), `0x82` (AGC gain), `0x83` (audio
   flags), `0x84` (128 samples of silence), `0x85` (true frequency), `0x86`
   (filter switch), `0x87` (timestamp). Bytes in `0x90`–`0xDF` start an LPC
   block whose quantiser order comes from the high nibble; bytes below `0x80`
   start an uncompressed LPC block.

The LPC path is a 20th-order synthesis filter driven by an adaptive quantiser.
The reference implementation relies on JavaScript's 32-bit integer semantics for
quantiser overflow, so `sdrstream.vocoder` reproduces those semantics explicitly
(`_i32` / `_to_int32`). Two other behaviours are easy to get wrong and are
handled deliberately:

* Sample writes wrap modulo 2^16 rather than clamping, matching the reference
  ring buffer.
* A truncated `0x80` frame is padded with silence: the reference reads past the
  end of the message and relies on `undefined` coercing to zero.

## Testing

```bash
python -m unittest discover -s tests -t .
```

The suite has 48 tests and needs no network:

* **Decoder tests** compare against fixtures generated by the reference
  JavaScript decoders, including captured frames from a live WebSDR server.
* **Integration tests** run real `websockets` servers on localhost and drive the
  clients through connect, handshake, tuning, framing, decoding, Origin
  validation and reconnect.

To exercise live infrastructure:

```bash
python examples/live_smoke_test.py --seconds 5 --outdir /tmp/smoke
```

## Scope and responsible use

This is a client for **public, intentionally-shared** amateur-radio receivers
that anyone can listen to in a browser. Use it accordingly:

* Respect each server's terms and any connection limits.
* Do not use it to intercept private communications.
* Public WebSDR/OpenWebRX receivers are operated by volunteers; keep request
  rates modest.

## License

MIT. The WebSDR companding table in `sdrstream/_websdr_table.py` is reproduced
from the public WebSDR HTML5 client (copyright 2013–2025 PA3FWM), which permits
redistribution as part of a client.
