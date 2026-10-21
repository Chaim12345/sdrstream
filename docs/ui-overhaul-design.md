# sdrstream UI overhaul - design plan (workstream E)

## Goals

- Mobile-first: the owner uses a phone, late at night, one thumb. Every view must work at 390px wide.
- Live and dynamic: the monitor is a living system; the UI should feel alive (streaming spectrum/waterfall, sliding-in activity, countdowns, toasts) without a build step or framework.
- Map feeds: receivers, live activity, satellite ground tracks, ISS position and pass paths on a dark world map.
- No CDN dependency without an offline fallback; everything works behind the existing password login; API contracts unchanged.

## Information architecture

Bottom tab bar on phones (thumb reach), left sidebar on desktop (>= 900px). Nine views:

1. **Overview** - stat cards (monitor state, receivers, passes, captures, activity), next-pass countdown, spectrum + waterfall, live activity preview, latest captures.
2. **Map** - full-screen map with layer toggles.
3. **Activity** - full timeline with kind filter chips (voice/data/cw/carrier).
4. **Passes** - upcoming pass cards with live countdowns and max elevation.
5. **Gallery** - Live / SatNOGS / Test fixtures tabs, lightbox with caption.
6. **Captures** - audio cards with inline players and metadata.
7. **Scan** - scanner status board, cards grouped by station, state-colored.
8. **Stations** - searchable directory, jump-to-map per station.
9. **Satellites** - satellite table (cards on mobile).

## Visual system

- Dark theme, layered surfaces: bg #070a12 / panel #0e1420 / raised #151c2c, hairline borders #1e2637.
- Accent cyan #5cc8ff (unchanged brand), semantic colors: good #34d399, warn #fbbf24, bad #f87171, data #c084fc, voice #60a5fa.
- Type: system sans for UI, ui-monospace for frequencies, coordinates and timestamps.
- 8px spacing grid, 12px radii, 44px minimum touch targets, visible focus rings.
- Motion: 150-250ms ease transitions; new activity cards slide in; live pill pulses; respects `prefers-reduced-motion`.

## Dynamic elements

- **Spectrum + waterfall**: the existing 1s spectrum feed is also stacked client-side into a scrolling waterfall (no backend change).
- **Live activity timeline**: new items animate in, relative timestamps tick, a toast announces each new transmission kind.
- **Next-pass countdown** on the overview, ticking every second.
- **Skeletons** on first load of every panel; stale-data notes where the old UI had them (station directory).
- **Lightbox** for gallery images with caption and keyboard/touch close.
- **Toasts** for new activity and recoverable errors.

## Map feeds

Library: **Leaflet 1.9.4, vendored** into `go/internal/web/static/vendor/leaflet/` (no CDN). Tiles come through a same-origin proxy so the strict CSP (`img-src 'self'`) keeps working.

- **Receivers layer**: catalogue receivers with coordinates (accent circles, popup with URL, kind, satellite bands).
- **Station directory layer**: all public WebSDR/OpenWebRX/KiwiSDR stations from `/api/stations.json` (dim dots, capped for performance).
- **Satellite layer**: every active satellite's current position (labelled pulsing marker, ISS highlighted) plus a ground-track polyline sampled for roughly one orbit; tracks split at the antimeridian.
- **Live activity layer**: recent transmissions plotted at their source receiver's coordinates, pulsing, labelled with kind and frequency.
- **Pass links**: for passes in progress, a line from the satellite's current position to the receiving station.
- **Offline fallback**: tiles are drawn onto canvas by a custom GridLayer; when the proxy or upstream is unreachable the tile falls back to a dark graticule grid, and every data layer (markers, tracks, paths) keeps working.

## New read-only API (minimal, listed for the integrator)

1. `GET /api/map.json` - `{generated_at, satellites_available, receivers:[{url,kind,latitude,longitude,satellite_bands}], satellites:[{name,norad_id,downlink_hz,mode,latitude,longitude,altitude_km,track:[[lat,lon],...]}], passes:[{satellite,receiver,receiver_latitude,receiver_longitude,rise,set,max_elevation,active}]}`. Cached 60s server-side; TLE failure degrades to `satellites_available:false` with receivers still served.
2. `GET /api/tiles/{z}/{x}/{y}.png` - same-origin tile proxy (CARTO dark, OSM fallback), in-memory cache, z/x/y validated, 10s upstream timeout. Exists only to keep `img-src 'self'`; the UI survives it failing.

## CSP change (web.go, small hunk)

`script-src` and `style-src` gain `'self'` so the vendored assets load. Everything else unchanged: no remote origins are ever added, images remain `'self' data:`.

## Files

- `go/internal/web/static/index.html` - markup for all nine views (embedded as `indexPage`).
- `go/internal/web/static/app.css`, `app.js` - design system + behaviour.
- `go/internal/web/static/vendor/leaflet/` - vendored Leaflet 1.9.4 (MIT).
- `go/internal/web/page.go` - now just `//go:embed static/index.html` into `indexPage`.
- `go/internal/web/map.go` - the two new endpoints above.
- `go/internal/web/web.go` - small hunks only: embed FS + `/static/` file server, two route registrations, CSP `'self'` addition.
