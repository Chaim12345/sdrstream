# sdrstream

Stream and decode audio from public **WebSDR** and **OpenWebRX / OpenWebRX+**
servers, in pure Python.

The library talks to the servers directly over their native WebSocket protocols,
decodes the audio the same way the official browser clients do, and hands you
16-bit PCM you can write to a WAV file or process with numpy.

It also tracks amateur satellites: fetch current orbital elements, predict
passes over a receiver, and record a pass while following the Doppler shift.

Both decoders are verified **bit-exact** against the reference JavaScript
clients:

* `sdrstream.vocoder` reproduces the WebSDR `websdr-sound.js` output sample for
  sample.
* `sdrstream.adpcm` reproduces the openwebrx-js `AdpcmDecoder` output sample for
  sample.

## Install

```bash
pip install .              # library only
pip install .[satellites]  # adds sgp4 for pass prediction
pip install .[analysis]    # adds numpy for spectrum work
```

The only runtime dependency is [`websockets`](https://pypi.org/project/websockets/).
Satellite tracking additionally needs [`sgp4`](https://pypi.org/project/sgp4/),
and WAV output uses the standard library; `numpy`/`scipy` are not required.

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

# Open the browser UI
python -m sdrstream web --port 8000
```

## Web UI

`python -m sdrstream web` serves a single page over the same modules the
command line uses. There is no build step, no template engine and no JavaScript
framework: the page is one string in `sdrstream/web.py` and the endpoints
return JSON, so the server needs nothing beyond the standard library.

The tabs are the tasks that are awkward from a shell:

* **Receivers** — find receivers covering a frequency, either from the bundled
  catalogue or a live sweep of the public OpenWebRX directory.
* **Satellites** — the amateur table with downlinks and bands.
* **Passes** — predict passes over a chosen latitude and longitude.
* **Government & military** — the table described below, with a live
  reachability count against real receivers.
* **Record** — record to a WAV and play it back in the page.
* **Undocumented** — satellites the network has heard that the table lacks.

Recording is asynchronous. The browser posts to `/api/record`, gets a job id
immediately, and polls `/api/record/<id>`; the WAV is fetched from
`/api/record/<id>/audio` once the job is done. Jobs live in memory and do not
survive a restart.

The server binds to `127.0.0.1` by default and has **no authentication**, and
the record endpoint will connect to whatever URL it is given. Do not expose it
to a network you do not control.

## Government and military downlinks

`sdrstream.military` lists government and military spacecraft whose downlinks a
public receiver can reach, and it is careful about what "reach" means. Each
entry carries an `encryption` field:

* `open` — the downlink carries unencrypted traffic. This is rare and usually
  means a legacy system built before encryption was practical.
* `encrypted` — the downlink is receivable but its payload is encrypted. You
  can see that the satellite is transmitting, measure its frequency, watch the
  Doppler curve and time the passes. You cannot read the content.
* `beacon` — only a carrier or unmodulated beacon is receivable.

```bash
python -m sdrstream military           # against the bundled catalogue
python -m sdrstream military --live    # against a live directory sweep
```

The most interesting entries are the UHF SATCOM repeaters in 240–270 MHz.
FLTSATCOM, UFO and Skynet were built in the 1970s and 1980s, before practical
encryption, so their transponders still relay whatever reaches them — in
practice, usually unlicensed third-party traffic. They are the most widely
heard military downlinks in the world. Against a live sweep, 25 of the roughly
440 public OpenWebRX receivers reach that band.

The rest of the table is mostly `encrypted`: DMSP at 400.032 MHz, DSP/SBIRS
SGLS carriers near 2237.5 MHz, Molniya's spread-spectrum links near 1000 MHz.
A public receiver can confirm a pass and nothing more. Systems that are out of
reach entirely — Milstar/AEHF at EHF, Lacrosse at X-band, KH-11 — are listed in
`OUT_OF_REACH` with the reason, so the gap is stated rather than implied.

Nothing here decrypts anything, and there is no key material or cryptanalysis
anywhere in the package. Receiving a signal broadcast over the air is generally
lawful to listen to; what is not lawful in most places is decrypting a
protected service or using what you hear.

## Non-text protocols

`sdrstream.dvbs2` handles the binary framing underneath the text protocols: the
two custom IP-over-satellite layouts documented by the
[Don't Look Up](https://github.com/ucsdsysnet/dontlookup) project. Both turn out
to be plain TFTP DATA packets (RFC 1350) with a fixed block size — a `00 03`
opcode, a 16-bit block number, then 512 or 1024 bytes of payload.

```python
from sdrstream import dvbs2

found = dvbs2.carve(data)          # find every frame, record the gaps
found.payload()                    # payloads in capture order
found.ordered_payload()            # reassembled by block number
found.missing()                    # holes in the reassembled stream
dvbs2.reassemble(data)             # the common case, one call
```

Carving works by scanning every byte offset for the opcode, because the capture
does not say where frames begin. A frame is only accepted when it fits, so a
stray opcode near the end cannot produce a truncated frame. The 16-bit block
counter wraps, so ordering is derived from the largest circular gap between
block numbers rather than by sorting, which would put the tail of a long
transfer first. `best_carve` runs both layouts and keeps whichever recovers
more payload, which is how the reference tool resolves the ambiguity.

## Audio decoders

The Go build carries a growing set of demodulators that turn recorded audio
back into data. They all run offline over a WAV file, so a capture from any
receiver can be decoded later:

```bash
cd go
go run ./cmd/sdrstream decode capture.wav              # every mode
go run ./cmd/sdrstream decode -mode aprs capture.wav   # one mode
```

| Mode | What it reads | Modulation |
| --- | --- | --- |
| `aprs` | APRS/AX.25 packets | 1200 baud AFSK, HDLC |
| `morse` | CW text | on/off keying |
| `dtmf` | telephone digits | two-tone pairs |
| `rtty` | Baudot teletype | 45.45 baud FSK |
| `psk31` | PSK31 text | BPSK, varicode |
| `sstv` | image mode headers | VIS tone code |
| `pocsag` | pager pages (numeric and alphanumeric) | 1200/2400 baud FSK, BCH |
| `eas` | US Emergency Alert System headers | 520.83 baud AFSK, 8N1 |
| `navtex` | maritime navigational warnings | SITOR-B FEC, CCIR 476 |

Each decoder has a reference test built from a generated fixture, so the suite
catches regressions in the bit-level work. The EAS decoder uses the sixteen
`0xAB` preamble bytes to find the byte boundary before reading the 8N1 stream;
the NAVTEX decoder picks the bit alignment that maximises valid CCIR 476
characters, then drops the FEC repeat characters.

## Satellites

The satellite side answers three questions: what is up there, when will it be
over a receiver, and what did it sound like.

```bash
# What amateur satellites are there, and which band do they use
python -m sdrstream satellites

# Details, plus the public receivers that cover the downlink
python -m sdrstream satellites SO-50

# When the next passes are, from a receiver in the catalogue
python -m sdrstream passes SO-50 --server http://vhf-goonhilly.batc.org.uk:8902     --hours 24 --min-elevation 20

# Or from your own coordinates
python -m sdrstream passes ISS --lat 52.23 --lon 6.89 --hours 12

# Record a whole pass, retuning as Doppler shifts the signal
python -m sdrstream satrecord SO-50     --server http://vhf-goonhilly.batc.org.uk:8902     --min-elevation 20 --out so50.wav

# Show a plan without connecting to anything
python -m sdrstream satrecord SO-50 --server ... --dry-run
```

`satrecord` reconnects for each 20 second segment and retunes between segments,
so a dropped connection costs one segment rather than the pass. It waits for
the pass to start, so it is fine to launch it early.

### Where the data comes from

* **Orbital elements** come from Celestrak's amateur group, cached for a day
  (`sdrstream.tle`). A TLE is only good for a few days; stale elements put the
  satellite in the wrong place, so refetch rather than trust an old cache.
* **The satellite table** (`sdrstream.satellites`) is a small curated list of
  the downlinks most likely to be audible. Frequencies drift, so treat it as a
  starting point.
* **The receiver catalogue** (`sdrstream.catalogue`) is generated by
  `tools/build_catalogue.py`, which reads the WebSDR project's own server list
  and keeps the receivers whose published tuning ranges overlap an amateur
  satellite band. Coordinates come from that list and are good to a few
  kilometres, which is plenty for deciding whether a pass is plausible.

```bash
python tools/build_catalogue.py --out sdrstream/catalogue.py
```

* **Observed frequencies** can be checked against the SatNOGS network, which
  records what ground stations actually heard:

```bash
python -m sdrstream observations SO-50
```

The SatNOGS network also stores what it captured, so the waterfall image and
any demodulated data can be pulled down directly:

```bash
python -m sdrstream fetch ISS --out captures --count 2
```

### More receivers, and satellites we do not know about

The WebSDR catalogue is a static file. The other large public network,
OpenWebRX, is discovered live instead, because those servers come and go
quickly. Every OpenWebRX server publishes a small JSON document at
`/status.json` giving the operator's coordinates and every tuning profile it
offers, which is exactly what is needed to decide whether it can hear a
downlink.

```bash
# Every OpenWebRX server that can hear 145.800 MHz
python -m sdrstream discover --frequency 145800

# Just the ones covering 70 cm
python -m sdrstream discover --band 70cm
```

A full sweep of the public directory finds roughly 1,300 receivers, of which
about 380 reach an amateur satellite band (360 on 2 m, 330 on 70 cm). Each
result carries a position and a band list, so it can be fed straight into
`passes` or `satrecord`.

The curated satellite table cannot keep up with the network, which records
around ten thousand observations a day. Newly launched spacecraft and
lightly documented transmitters appear in the feed before anyone writes them
down, so the feed is the place to look for them:

```bash
# Satellites heard recently that the table does not list
python -m sdrstream undocumented
```

SatNOGS assigns temporary NORAD ids in the `99xxx` range to satellites it has
heard but not yet identified, so an id in that range is a good sign that
something is new. The report marks whether each unknown downlink falls in a
band our receivers can actually hear.

### Tracking accuracy

`sdrstream.tracking` implements SGP4 look angles, pass prediction and Doppler
in about 300 lines, using `sgp4` for propagation only. It was cross-checked
against Skyfield: peak elevation and pass times agree to better than 0.01
degrees, and Doppler agrees with the textbook values at 145 MHz, 437 MHz and
1296 MHz.

Doppler is worth correcting above about 400 MHz. At 437 MHz a low pass shifts
the signal by roughly +/- 9 kHz, which is more than an FM channel, so a fixed
frequency loses the signal at the edges of the pass.

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

The suite has 233 tests and needs no network:

* **Decoder tests** compare against fixtures generated by the reference
  JavaScript decoders, including captured frames from a live WebSDR server.
* **Integration tests** run real `websockets` servers on localhost and drive the
  clients through connect, handshake, tuning, framing, decoding, Origin
  validation and reconnect.

The satellite tests are offline and deterministic; they pin the orbital maths
to properties that must hold (a geostationary satellite never rises, Doppler
changes sign as a satellite flies over) plus values cross-checked against
Skyfield.

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

On the government and military table, the same rule applies with more force.
Every entry is a downlink that is broadcast over the air and documented in
public sources, and the table states for each one whether the payload is
readable or encrypted. The package contains no decryption, no key material and
no cryptanalysis, and it does not attempt to break a protected service.
Reception of an unencrypted broadcast is a listening activity; decrypting a
protected service, or acting on what you hear, may not be lawful where you are.
That is your call to make, not this library's.

## License

MIT. The WebSDR companding table in `sdrstream/_websdr_table.py` is reproduced
from the public WebSDR HTML5 client (copyright 2013–2025 PA3FWM), which permits
redistribution as part of a client.
