# Live public data panels

The Live data tab now has independent panels for NOAA SWPC planetary Kp,
wspr.live recent WSPR spots, SondeHub radiosondes, Digitraffic Baltic AIS,
and nearby adsb.lol aircraft. These are internet observations, not local
receiver decodes. Provider observation times and fetch times are distinct.

## Operation

The existing `sdrstream web` feed manager starts the sources. `--no-feeds`
disables them. `--feeds-lat` and `--feeds-lon` configure the ADS-B centre;
the current radius is 250 nautical miles. Restart to change the point.
These flags do not filter the global WSPR/sonde feeds or Baltic AIS.

- SWPC: one GET per 5 minutes; latest observed 3-hour Kp, not a forecast.
- wspr.live: one bounded, 15-minute SQL query per 2 minutes, at most 100 spots.
- Digitraffic: one gzip GET per 2 minutes; newest 150 vessel positions,
  explicitly Baltic coverage rather than a global or Mediterranean AIS feed.
- adsb.lol: existing 60-second cache, API v2 (deprecated upstream; may change).
- SondeHub: one historical bootstrap per process, followed by MQTT over secure
  WebSocket (`sondes/#`). Reconnects never repoll telemetry history. Broker
  discovery is repeated only on stream reconnect, with existing backoff.

Opening/refreshing the UI only reads the shared cache, not upstream hosts.
The API adds four per-source samples independently of the global 500-item
list, preventing aircraft volume from hiding SWPC or WSPR panels. Successful
polls replace their source cache; failures preserve the last good data with
an error. The manager's age windows prune old data; panels also hide stale
samples immediately. Empty results are not represented as zero real-world
activity. Source errors clear on recovery. Responses, decompression and
stream packets are bounded; HTTP requests have timeouts and cancellation.
No secrets or additional API keys are needed for these five sources.

## Sources and credit

All adapters are original Go code, not copied provider code. Provider data
is normalized for display (SondeHub: latest position per sonde; AIS: latest
150 vessels; SWPC: latest Kp; WSPR: most recent 100 spots).

- NOAA SWPC, US government data:
  https://services.swpc.noaa.gov/products/noaa-planetary-k-index.json
- wspr.live/WSPR contributors, public query results. Public-use conditions
  and limit (20 requests/minute) documented at https://wspr.live/ .
- SondeHub contributors, CC BY-SA 2.0:
  https://github.com/projecthorus/sondehub-infra/wiki/API-%28Beta%29
  https://creativecommons.org/licenses/by-sa/2.0/
  API explicitly forbids regular telemetry HTTP polling.
- Fintraffic/Digitraffic, CC BY 4.0:
  https://www.digitraffic.fi/en/marine-traffic/
  https://creativecommons.org/licenses/by/4.0/
- adsb.lol contributors, ODbL:
  https://www.adsb.lol/docs/open-data/api/
  https://opendatacommons.org/licenses/odbl/1-0/

Panels link to provider attribution. Source data licences apply to exported
or redistributed data separately from the application's code licence.

## Verification

Unit tests use synthetic JSON and local HTTP fixtures (gzip with and without
Content-Encoding, HTTP failures, malformed input, newest timestamp selection,
GeoJSON coordinate order, WSPR Hz conversion, per-source samples under global
truncation, cache replacement/error recovery, bootstrap-once reconnects and
MQTT telemetry parsing). No network requests occur in normal tests.

The isolated live check on 2026-10-22 fetched all five providers successfully.
SondeHub connected to the discovered secure MQTT broker and received changing
telemetry timestamps. The UI was checked in local headless Chrome at desktop
1440x1200 and mobile 390x844: five panels, no JavaScript page errors or
horizontal document overflow. No running deployment or tunnel was modified.
