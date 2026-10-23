# Sources - every external feed and service sdrstream touches

Current as of **2026-10-22** (`feat/ledger-on-pr5`, `4af3fee`).

Ground rules for every source on this list:

- **Open/unencrypted sources only.** No credential abuse, no scraping
  behind logins (the single exception is aisstream.io, which requires the
  user's own free API key and is disabled without it).
- **Polite rates.** Pollers use the intervals below; streams are one
  persistent connection each with reconnect backoff (5 s doubling to
  5 min). Public SDR receivers get one connection per receiver at a time,
  under a fleet-wide concurrency cap and load guard.
- **Terms marked "not recorded" are unknown, not cleared.** Check the
  provider's own terms before reusing or redistributing their data. The
  UI says the same thing per feed (`go/internal/feeds/terms.go`).
- ToS check is required **before adding any new feed** to this list.

## 1. Live feeds wired into the web UI (`go/internal/feeds`, started by `feeds.Start`)

| Source | Endpoint / transport | Polling interval | Licence / terms status | Notes |
| --- | --- | --- | --- | --- |
| adsb.lol | `https://api.adsb.lol` REST | 60 s | **ODbL** (recorded in `terms.go`) | Aircraft positions within 250 NM of `--feeds-lat/lon`. Max age 2 min. |
| Digitraffic (Finland marine) | `https://meri.digitraffic.fi/api/ais/v1/locations` REST | 2 min | **CC BY 4.0** | AIS positions. Max age 30 min. |
| NOAA SWPC | `https://services.swpc.noaa.gov/products/noaa-planetary-k-index.json` REST | 5 min | **US government data** (public domain) | Planetary K-index space weather. Max age 12 h. |
| SondeHub | `https://api.v2.sondehub.org/sondes/websocket` (MQTT-over-WS, broker discovered via API) | persistent stream | **CC BY-SA 2.0** | Radiosonde telemetry. Max age 2 h. |
| APRS-IS | `rotate.aprs2.net:14580` TCP | persistent stream, read-only login (passcode -1), range filter ~400 km | **not recorded** - check aprs-is.net terms | Max age 1 h. |
| PSK Reporter | `mqtt.pskreporter.info:1883` MQTT | persistent stream | **not recorded** - check pskreporter.info terms | Digimode spots. Max age 30 min. Items carry a provider ref for the proof ledger. |
| Reverse Beacon Network | `telnet.reversebeacon.net:7000` telnet | persistent stream | **not recorded** - check reversebeacon.net terms | CW/RTTY skimmer spots. Max age 30 min. Items carry a provider ref. |
| DX cluster | `dxc.ve7cc.net:23` telnet (CC Cluster) | persistent stream | **not recorded** - cluster network, check terms | Max age 30 min. |
| POTA | `https://api.pota.app` REST | 90 s | **not recorded** - check pota.app terms | Parks on the Air spots. Max age 1 h. |
| SOTA | `https://api2.sota.org.uk` (SOTA Watch v2) REST | 90 s | **not recorded** - check sota.org.uk terms | Max age 1 h. |
| wspr.live | `https://db1.wspr.live` ClickHouse SQL query | 150 s | **not recorded** - public query results, check wspr.live terms | Community mirror of the WSPRnet database; used so wsprnet.org is not scraped. Small LIMIT, newest 15-min window only. Max age 30 min. |
| aisstream.io | `wss://stream.aisstream.io/v0/stream` WebSocket | persistent stream | **not recorded** - check aisstream.io terms; requires the user's own free API key (OAuth signup) via `SDRSTREAM_AIS_KEY` / `--ais-key` | **Disabled without a key** - every other feed runs regardless. Max age 10 min. |

## 2. Feeds present in code but not wired into the web manager

| Source | Endpoint / transport | Rate | Licence / terms status | Where used |
| --- | --- | --- | --- | --- |
| wsprnet.org | `https://www.wsprnet.org` HTTP query | on demand only - one query per cross-check, no streaming | **not recorded** - check wsprnet.org terms | Cross-check: does WSPRnet confirm a callsign our decoder heard? Max age 24 h. |
| airframes.io | `https://api.airframes.io/v1/messages` REST + socket.io firehose | 60 s REST poll (default), then firehose | **not recorded** - check airframes.io terms; anonymous REST works, key only raises limits | `avmarine` command only. **Pending owner decision: left as is, not in the web feeds** (see HANDOFF.md section 7). |
| openwaters.io | `wss://ais.openwaters.io/v1/stream` WebSocket | persistent stream | Raw NMEA re-served under each upstream source's own terms; upstream attribution kept on every item. Anonymous read access per the provider | `avmarine` command / raw AIS capture. |
| Icecast directory (NWR) | `https://dir.xiph.org/yp.xml` (~9 MB XML) | cached on disk, refreshed on demand | Directory data terms **not recorded** - check dir.xiph.org | NOAA Weather Radio SAME stream discovery (`go/internal/icecast`). |
| SatNOGS | SatNOGS network API, unauthenticated | rate-limited by provider; responses disk-cached for process lifetime | **not recorded** - check network.satnogs.org terms | Real downlink metadata: frequencies, audio/waterfall links (`go/internal/satnogs`). |
| CelesTrak / AMSAT TLE | `https://celestrak.org/NORAD/elements/gp.php`, fallback `https://www.amsat.org/tle/current/nasa.all` | on demand, disk cache; TLEs refreshed when stale | CelesTrak data is free for use with attribution per celestrak.org; **recorded here as check-provider-terms** | Orbit elements for satellite pass tracking (`go/internal/tle`). CelesTrak rate-limits; AMSAT is the fallback. |
| NOAA radiofax schedule | `https://ocean.weather.gov/radiofax_update.txt` | on demand | **US government data** (public domain) | WEFAX broadcast schedule for targeted HF listening (`go/internal/hfschedule`). |
| receiverbook directory | `https://receiverbook.de/map` | `--scan-directory-refresh`, default 1 h; failed/empty refresh keeps the previous list | Directory listing terms **not recorded** - check receiverbook.de | Public receiver roster for the fleet scanner (`go/internal/directory`). |

## 3. Public SDR receivers (WebSDR / OpenWebRX / KiwiSDR)

Not feeds in the package sense, but the primary data sources.

- **Terms:** each receiver is individually operated; there is no global
  licence for received audio or decoded content. Per-receiver terms, where
  the operator publishes any, are **not recorded here** - treat as unknown
  per receiver.
- **Politeness policy (code-enforced):** one connection per receiver,
  bounded fleet concurrency with a ramp, load guard, per-host
  blocklist/backoff on refusals and timeouts, and per-visit target limits
  (`--scan-target-limit`, `--scan-station-session`).
- **WebSDR audio table caveat:** the companding table in
  `go/internal/vocoder/table.go` is reproduced from the WebSDR HTML5
  client (copyright PA3FWM, all rights reserved). Redistribution
  permission has **not** been obtained - this is a pending owner decision
  tracked in `NOTICE` and HANDOFF.md section 7.

## 4. Data processors (not sources, listed for completeness)

| Service | Use | Terms status |
| --- | --- | --- |
| NVIDIA NIM (`grpc.nvcf.nvidia.com`) | Hosted ASR for voice captures (whisper-large-v3 / parakeet) | Requires the owner's NVIDIA API key; subject to NVIDIA's cloud terms - not recorded here. Captures sent for transcription are audio off public receivers. |

## 5. Adding a new feed - checklist

1. Check the provider's ToS **before** writing code; record the result in
   `go/internal/feeds/terms.go` (or mark "not recorded - check provider
   terms"). Never invent a licence.
2. Choose a polite interval at or above the norms above (60 s REST floor;
   prefer streams when the provider offers one).
3. Set a staleness `maxAges` entry so the UI flags a dead feed.
4. Items must carry provider time, fetch time, decoded-by and a provider
   ref so the proof ledger stays honest.
