"""A small web UI over the receiver and satellite tooling.

Run it with ``python -m sdrstream web`` or ``sdrstream web``, then open the
address it prints. It serves one page and a handful of JSON endpoints; there is
no build step, no template engine and no JavaScript framework, so the whole
thing stays readable and the server keeps working with the standard library
alone.

The server is deliberately a thin layer. Everything it does is a call into the
modules the command line already uses, so a bug in the web layer cannot change
what the tooling believes about a receiver or a satellite.

Recording is the one long-running operation. A browser asks for it with POST
``/api/record``, gets a job id back immediately, and then polls
``/api/record/<id>``; the audio itself is fetched from
``/api/record/<id>/audio`` once the job finishes. Jobs are held in memory and
the directory is a temporary one, so nothing survives a restart.

Binding: the default is 127.0.0.1. There is no authentication, and the record
endpoint will connect to whatever URL it is given, so do not expose the server
to a network you do not control.
"""

from __future__ import annotations

import asyncio
import json
import os

import threading
import time
import uuid
from dataclasses import asdict, dataclass
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any, Dict, List, Optional
from urllib.parse import parse_qs, urlparse

from . import catalogue, military, openwebrx_directory as directory
from .common import wav_bytes
from .openwebrx import OpenWebRXClient
from .satellites import SATELLITES, by_name
from .websdr import WebSDRClient

#: The page. Kept as one string so the server has no static file dependency.
PAGE = """<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>sdrstream</title>
<style>
  :root { color-scheme: dark; }
  * { box-sizing: border-box; }
  body { margin: 0; font: 14px/1.5 ui-monospace, SFMono-Regular, Menlo, monospace;
         background: #10141a; color: #d8dee9; }
  header { padding: 18px 22px; border-bottom: 1px solid #232a35; }
  h1 { margin: 0; font-size: 17px; font-weight: 600; letter-spacing: .02em; }
  h1 span { color: #6f7d91; font-weight: 400; }
  main { padding: 18px 22px 60px; max-width: 1100px; }
  nav { display: flex; gap: 6px; flex-wrap: wrap; margin: 16px 0 22px; }
  nav button { background: #1a2029; color: #9aa7b8; border: 1px solid #232a35;
               padding: 7px 13px; border-radius: 5px; cursor: pointer;
               font: inherit; }
  nav button:hover { color: #d8dee9; border-color: #35404f; }
  nav button[aria-current="true"] { background: #24303f; color: #e6edf5;
               border-color: #3d4c60; }
  section { display: none; }
  section.on { display: block; }
  fieldset { border: 1px solid #232a35; border-radius: 6px; padding: 14px 16px;
             margin: 0 0 18px; }
  legend { color: #6f7d91; padding: 0 6px; }
  label { display: inline-block; min-width: 92px; color: #9aa7b8; }
  input, select { background: #0b0e13; color: #d8dee9; border: 1px solid #232a35;
                  border-radius: 4px; padding: 6px 8px; font: inherit;
                  min-width: 190px; }
  button.go { background: #2c6e49; border-color: #3d8a5e; color: #eaf5ee;
              padding: 7px 15px; border-radius: 5px; cursor: pointer;
              font: inherit; }
  button.go:disabled { opacity: .5; cursor: default; }
  table { border-collapse: collapse; width: 100%; margin-top: 12px; }
  th, td { text-align: left; padding: 6px 10px; border-bottom: 1px solid #1d232c;
           vertical-align: top; }
  th { color: #6f7d91; font-weight: 500; }
  td.num { text-align: right; font-variant-numeric: tabular-nums; }
  .hint { color: #6f7d91; margin: 6px 0 14px; }
  .row { margin-bottom: 10px; }
  .pill { display: inline-block; padding: 1px 7px; border-radius: 9px;
          font-size: 12px; border: 1px solid #2b3644; color: #9aa7b8; }
  .open { color: #7fd1a0; border-color: #2c6e49; }
  .encrypted { color: #e0b072; border-color: #6e522c; }
  .err { color: #e88; }
  .ok { color: #7fd1a0; }
  audio { width: 100%; margin-top: 12px; }
  code { color: #9ecbff; }
</style>
</head>
<body>
<header>
  <h1>sdrstream <span>public SDR receiver and satellite workbench</span></h1>
</header>
<main>
<nav>
  <button data-tab="receivers" aria-current="true">Receivers</button>
  <button data-tab="satellites">Satellites</button>
  <button data-tab="passes">Passes</button>
  <button data-tab="military">Government &amp; military</button>
  <button data-tab="record">Record</button>
  <button data-tab="captures">Live captures</button>
  <button data-tab="undocumented">Undocumented</button>
</nav>

<section id="receivers" class="on">
  <p class="hint">Find receivers whose published tuning ranges cover a
  frequency. The directory is read live from the public OpenWebRX list.</p>
  <fieldset>
    <div class="row">
      <label for="r-freq">Frequency</label>
      <input id="r-freq" type="number" value="145800" step="0.1">
      <span class="hint">kHz</span>
    </div>
    <div class="row">
      <label for="r-live">Include live</label>
      <input id="r-live" type="checkbox" checked>
      <span class="hint">probe every OpenWebRX server, slower but complete</span>
    </div>
    <button class="go" id="r-go">Find receivers</button>
  </fieldset>
  <div id="r-out"></div>
</section>

<section id="satellites">
  <p class="hint">The amateur satellite table, and the receivers that cover
  each downlink.</p>
  <button class="go" id="s-go">List satellites</button>
  <div id="s-out"></div>
</section>

<section id="passes">
  <p class="hint">Predict passes for a satellite over a chosen receiver.
  Needs <code>sgp4</code>.</p>
  <fieldset>
    <div class="row">
      <label for="p-sat">Satellite</label>
      <select id="p-sat"></select>
    </div>
    <div class="row">
      <label for="p-lat">Latitude</label>
      <input id="p-lat" type="number" value="52.23" step="0.0001">
      <label for="p-lon">Longitude</label>
      <input id="p-lon" type="number" value="6.89" step="0.0001">
    </div>
    <div class="row">
      <label for="p-hours">Hours</label>
      <input id="p-hours" type="number" value="24" step="1">
      <label for="p-elev">Min elevation</label>
      <input id="p-elev" type="number" value="10" step="1">
    </div>
    <button class="go" id="p-go">Predict</button>
  </fieldset>
  <div id="p-out"></div>
</section>

<section id="military">
  <p class="hint">Government and military downlinks, with what a public
  receiver can actually do with each one. Nothing here decrypts anything:
  an encrypted downlink shows a carrier and its Doppler curve, no content.</p>
  <button class="go" id="m-go">Show table</button>
  <div id="m-out"></div>
</section>

<section id="record">
  <p class="hint">Record audio from a receiver to a WAV file. Recording runs
  in the background; the player appears when it finishes.</p>
  <fieldset>
    <div class="row">
      <label for="c-server">Server</label>
      <input id="c-server" size="46" placeholder="http://host:port">
    </div>
    <div class="row">
      <label for="c-kind">Protocol</label>
      <select id="c-kind">
        <option value="openwebrx">openwebrx</option>
        <option value="websdr">websdr</option>
      </select>
      <label for="c-freq">Frequency</label>
      <input id="c-freq" type="number" value="145800" step="0.1">
      <span class="hint">kHz</span>
    </div>
    <div class="row">
      <label for="c-mode">Mode</label>
      <select id="c-mode">
        <option>USB</option><option>LSB</option><option>AM</option>
        <option>FM</option><option>CW</option>
      </select>
      <label for="c-secs">Seconds</label>
      <input id="c-secs" type="number" value="15" step="1">
    </div>
    <button class="go" id="c-go">Record</button>
  </fieldset>
  <div id="c-out"></div>
</section>

<section id="captures">
  <p class="hint">Live view of a recording campaign. Reads the plan and the
  captures the recorder has written so far, refreshing every few seconds.
  Start a campaign with <code>python tools/record_sky.py --live</code>; set
  <code>SDRSTREAM_CAPTURES</code> if it writes somewhere other than
  <code>/tmp/captures</code>.</p>
  <fieldset>
    <div class="row">
      <label for="k-auto">Auto-refresh</label>
      <input id="k-auto" type="checkbox" checked>
      <button class="go" id="k-go">Refresh now</button>
    </div>
  </fieldset>
  <div id="k-out"></div>
</section>

<section id="undocumented">
  <p class="hint">Satellites heard by the SatNOGS network that the curated
  table does not list. Temporary 99xxx NORAD ids mark unidentified objects.
  Needs the network to be reachable.</p>
  <div class="row">
    <label for="u-limit">Scan</label>
    <input id="u-limit" type="number" value="60" step="10">
    <span class="hint">observations</span>
  </div>
  <button class="go" id="u-go">Scan</button>
  <div id="u-out"></div>
</section>
</main>
<script>
const $ = (id) => document.getElementById(id);
const esc = (v) => String(v == null ? "" : v).replace(/[&<>"]/g,
  (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));

document.querySelectorAll("nav button").forEach((button) => {
  button.addEventListener("click", () => {
    document.querySelectorAll("nav button").forEach((other) =>
      other.setAttribute("aria-current", String(other === button)));
    document.querySelectorAll("section").forEach((section) =>
      section.classList.toggle("on", section.id === button.dataset.tab));
  });
});

async function api(path, options) {
  const response = await fetch(path, options);
  const text = await response.text();
  let body;
  try { body = JSON.parse(text); } catch (e) { body = { error: text }; }
  if (!response.ok) throw new Error(body.error || response.statusText);
  return body;
}

function table(headers, rows) {
  if (!rows.length) return "<p class='hint'>nothing found</p>";
  const head = headers.map((h) => "<th>" + esc(h) + "</th>").join("");
  const body = rows.map((cells) => "<tr>" + cells.map((c, i) => {
    // A {raw: ...} cell is trusted markup (we build it ourselves); everything
    // else is escaped, since it can come from a remote receiver.
    const raw = c && typeof c === "object" && "raw" in c;
    const value = raw ? c.raw : esc(c);
    return "<td" + (i && typeof c === "number" ? " class='num'" : "") + ">" +
           value + "</td>";
  }).join("") + "</tr>").join("");
  return "<table><thead><tr>" + head + "</tr></thead><tbody>" + body +
         "</tbody></table>";
}

$("r-go").addEventListener("click", async () => {
  const out = $("r-out");
  out.innerHTML = "<p class='hint'>working, a live sweep takes about 20s...</p>";
  try {
    const query = "?frequency=" + encodeURIComponent($("r-freq").value) +
                  "&live=" + ($("r-live").checked ? "1" : "0");
    const data = await api("/api/receivers" + query);
    out.innerHTML = "<p class='hint'>" + data.count +
      " receiver(s) cover " + esc(data.frequency_mhz) + " MHz</p>" +
      table(["URL", "Protocol", "Latitude", "Longitude", "Bands"],
        data.receivers.map((r) => [r.url, r.kind, r.latitude, r.longitude,
                                   (r.satellite_bands || []).join(" ")]));
  } catch (error) { out.innerHTML = "<p class='err'>" + esc(error.message) + "</p>"; }
});

$("s-go").addEventListener("click", async () => {
  const out = $("s-out");
  try {
    const data = await api("/api/satellites");
    out.innerHTML = table(["Name", "NORAD", "Downlink MHz", "Mode", "Band", "Status"],
      data.satellites.map((s) => [s.name, s.norad_id, s.downlink_mhz, s.mode,
                                  s.band, s.status]));
  } catch (error) { out.innerHTML = "<p class='err'>" + esc(error.message) + "</p>"; }
});

$("p-go").addEventListener("click", async () => {
  const out = $("p-out");
  out.innerHTML = "<p class='hint'>predicting...</p>";
  try {
    const query = "?satellite=" + encodeURIComponent($("p-sat").value) +
      "&lat=" + $("p-lat").value + "&lon=" + $("p-lon").value +
      "&hours=" + $("p-hours").value + "&min_elevation=" + $("p-elev").value;
    const data = await api("/api/passes" + query);
    out.innerHTML = "<p class='hint'>" + esc(data.satellite) + " over " +
      esc(data.latitude) + ", " + esc(data.longitude) + "</p>" +
      table(["Start (UTC)", "Peak (UTC)", "Max elevation", "Duration s"],
        data.passes.map((p) => [p.start, p.peak, p.max_elevation,
                                p.duration_seconds]));
  } catch (error) { out.innerHTML = "<p class='err'>" + esc(error.message) + "</p>"; }
});

$("m-go").addEventListener("click", async () => {
  const out = $("m-out");
  out.innerHTML = "<p class='hint'>working...</p>";
  try {
    const data = await api("/api/military?live=1");
    out.innerHTML = "<p class='hint'>reachability checked against " +
      data.checked + " live receiver(s). open = payload readable.</p>" +
      table(["Name", "Operator", "MHz", "Mode", "Band", "Reach", "Encryption"],
        data.satellites.map((s) => [s.name, s.operator, s.downlink_mhz,
          s.mode, s.band, s.reachable ? s.receivers + " rx" : "none",
          s.encryption])) +
      "<h3>Out of reach</h3>" +
      table(["System", "Why"], data.out_of_reach.map((o) => [o.name, o.why]));
  } catch (error) { out.innerHTML = "<p class='err'>" + esc(error.message) + "</p>"; }
});

$("u-go").addEventListener("click", async () => {
  const out = $("u-out");
  out.innerHTML = "<p class='hint'>asking the network...</p>";
  try {
    const data = await api("/api/undocumented?limit=" + $("u-limit").value);
    out.innerHTML = "<p class='hint'>scanned " + data.scanned +
      " observation(s), " + data.unknown.length + " unknown</p>" +
      table(["NORAD", "Downlink MHz", "Mode", "Times heard", "Unidentified"],
        data.unknown.map((u) => [u.norad_id, u.frequency_mhz, u.mode,
                                 u.count, u.temporary_id ? "yes" : ""]));
  } catch (error) { out.innerHTML = "<p class='err'>" + esc(error.message) + "</p>"; }
});

let jobTimer = null;
$("c-go").addEventListener("click", async () => {
  const out = $("c-out");
  out.innerHTML = "<p class='hint'>starting...</p>";
  try {
    const body = JSON.stringify({
      server: $("c-server").value.trim(), kind: $("c-kind").value,
      frequency_khz: Number($("c-freq").value), mode: $("c-mode").value,
      seconds: Number($("c-secs").value)
    });
    const job = await api("/api/record", {
      method: "POST", headers: { "Content-Type": "application/json" }, body
    });
    poll(job.id, out);
  } catch (error) { out.innerHTML = "<p class='err'>" + esc(error.message) + "</p>"; }
});

function poll(id, out) {
  clearInterval(jobTimer);
  jobTimer = setInterval(async () => {
    try {
      const job = await api("/api/record/" + id);
      if (job.state === "running") {
        out.innerHTML = "<p class='hint'>recording, " + job.seconds +
          "s requested...</p>";
        return;
      }
      clearInterval(jobTimer);
      if (job.state === "error") {
        out.innerHTML = "<p class='err'>" + esc(job.error) + "</p>";
        return;
      }
      out.innerHTML = "<p class='ok'>wrote " + job.samples + " samples at " +
        job.samplerate + " Hz, peak " + job.peak + "</p>" +
        "<audio controls src='/api/record/" + id + "/audio'></audio>";
    } catch (error) {
      clearInterval(jobTimer);
      out.innerHTML = "<p class='err'>" + esc(error.message) + "</p>";
    }
  }, 1000);
}

fetch("/api/satellites").then((r) => r.json()).then((data) => {
  $("p-sat").innerHTML = data.satellites.map((s) =>
    "<option value='" + esc(s.name) + "'>" + esc(s.name) + "</option>").join("");
}).catch(() => {});

function captureRows(data) {
  return data.captures.map((c) => {
    const cell = c.available
      ? { raw: "<audio controls preload='none' src='/api/captures/" +
          encodeURIComponent(c.name) + "'></audio>" }
      : "<span class='err'>" + esc(c.error || "no audio") + "</span>";
    return [c.satellite, c.receiver, c.samples, c.samplerate, c.peak, cell];
  });
}

async function refreshCaptures() {
  const out = $("k-out");
  try {
    const data = await api("/api/captures");
    const plan = data.plan.length
      ? table(["Satellite", "Band", "MHz", "Rise (UTC)", "Max el", "Candidates"],
          data.plan.map((p) => [p.satellite, p.band, p.downlink_mhz,
            p.rise_utc, Math.round(p.max_elevation),
            p.candidates.length]))
      : "<p class='hint'>no plan yet; the recorder writes plan.json at start.</p>";
    const got = data.captures.length
      ? table(["Satellite", "Receiver", "Samples", "Hz", "Peak", "Audio"],
          captureRows(data))
      : "<p class='hint'>no captures yet.</p>";
    out.innerHTML = "<p class='hint'>reading " + esc(data.directory) +
      "</p><h3>Queued</h3>" + plan + "<h3>Captured</h3>" + got;
  } catch (error) {
    out.innerHTML = "<p class='err'>" + esc(error.message) + "</p>";
  }
}

let captureTimer = null;
function armCaptures() {
  clearInterval(captureTimer);
  if ($("k-auto").checked) captureTimer = setInterval(refreshCaptures, 5000);
}
$("k-go").addEventListener("click", refreshCaptures);
$("k-auto").addEventListener("change", armCaptures);
armCaptures();
refreshCaptures();
</script>
</body>
</html>
"""


@dataclass
class RecordJob:
    """One background recording."""

    id: str
    state: str = "running"
    seconds: float = 0.0
    samples: int = 0
    samplerate: int = 0
    peak: int = 0
    error: str = ""
    wav: Optional[bytes] = None

    def public(self) -> Dict[str, Any]:
        data = asdict(self)
        data.pop("wav", None)
        return data


#: Finished and running jobs, keyed by id. In memory only.
JOBS: Dict[str, RecordJob] = {}
#: Cached live directory sweep, so the page does not re-probe on every click.
_DIRECTORY_CACHE: Dict[str, Any] = {"at": 0.0, "receivers": []}
_DIRECTORY_TTL = 300.0
_DIRECTORY_LOCK = threading.Lock()


def _satellite_payload() -> Dict[str, Any]:
    return {"satellites": [
        {"name": satellite.name, "norad_id": satellite.norad_id,
         "downlink_mhz": round(satellite.downlink_khz / 1000.0, 5),
         "mode": satellite.mode, "band": satellite.band,
         "status": satellite.status}
        for satellite in SATELLITES.values()]}


def _live_receivers(timeout: float = 20.0) -> List[Any]:
    """Probe the public directory, caching the result briefly."""
    with _DIRECTORY_LOCK:
        age = time.time() - _DIRECTORY_CACHE["at"]
        if _DIRECTORY_CACHE["receivers"] and age < _DIRECTORY_TTL:
            return _DIRECTORY_CACHE["receivers"]
    found = directory.discover(timeout=timeout)
    with _DIRECTORY_LOCK:
        _DIRECTORY_CACHE["receivers"] = found
        _DIRECTORY_CACHE["at"] = time.time()
    return found


def _static_receivers() -> List[Any]:
    return list(catalogue.RECEIVERS.values())


def _receivers_payload(query: Dict[str, List[str]]) -> Dict[str, Any]:
    raw = (query.get("frequency") or ["145800"])[0]
    frequency_hz = float(raw) * 1000.0
    live = (query.get("live") or ["0"])[0] not in ("0", "", "false")
    pool = _live_receivers() if live else _static_receivers()
    found = [r for r in pool if r.can_hear(frequency_hz)]
    return {"frequency_mhz": round(frequency_hz / 1e6, 4),
            "source": "live directory" if live else "bundled catalogue",
            "count": len(found),
            "receivers": [
                {"url": r.url, "kind": r.kind, "latitude": r.latitude,
                 "longitude": r.longitude,
                 "satellite_bands": list(r.satellite_bands)}
                for r in sorted(found, key=lambda r: r.url)]}


def _military_payload(query: Dict[str, List[str]]) -> Dict[str, Any]:
    live = (query.get("live") or ["0"])[0] not in ("0", "", "false")
    pool = _live_receivers() if live else _static_receivers()
    results = military.reachable(pool)
    by_name = {item.satellite.name: item for item in results}
    satellites = []
    for satellite in military.GOVERNMENT_SATELLITES.values():
        item = by_name[satellite.name]
        satellites.append({
            "name": satellite.name, "operator": satellite.operator,
            "downlink_mhz": round(satellite.downlink_mhz, 5),
            "mode": satellite.mode, "band": satellite.band,
            "category": satellite.category, "encryption": satellite.encryption,
            "reachable": item.reachable, "receivers": item.count,
            "notes": satellite.notes, "source": satellite.source,
        })
    return {"checked": len(pool), "satellites": satellites,
            "out_of_reach": [{"name": name, "why": why}
                             for name, why in military.OUT_OF_REACH.items()]}


def _passes_payload(query: Dict[str, List[str]]) -> Dict[str, Any]:
    from . import tle, tracking

    name = (query.get("satellite") or [""])[0]
    if not name:
        raise ValueError("satellite is required")
    sat = by_name(name)
    latitude = float((query.get("lat") or ["52.23"])[0])
    longitude = float((query.get("lon") or ["6.89"])[0])
    hours = float((query.get("hours") or ["24"])[0])
    min_elevation = float((query.get("min_elevation") or ["10"])[0])

    text = tle.load()
    tracker = tracking.Tracker.from_tle_text(text, sat.name)
    passes = tracker.find_passes(latitude, longitude, hours=hours,
                                 min_elevation=min_elevation)
    return {
        "satellite": sat.name, "latitude": latitude, "longitude": longitude,
        "passes": [{"start": p.rise.strftime("%Y-%m-%d %H:%M:%S"),
                    "peak": p.peak.time.strftime("%Y-%m-%d %H:%M:%S"),
                    "max_elevation": round(p.max_elevation, 1),
                    "duration_seconds": round(p.duration, 1)}
                   for p in passes],
    }


def _undocumented_payload(query: Dict[str, List[str]]) -> Dict[str, Any]:
    from . import discovery

    limit = int((query.get("limit") or ["60"])[0])
    found = discovery.unknown_satellites(limit=limit)
    return {"scanned": limit,
            "unknown": [
                {"norad_id": entry.norad_id,
                 "name": entry.name,
                 "frequency_mhz": round((entry.downlink_hz or 0) / 1e6, 4),
                 "mode": "/".join(sorted(set(entry.modes))) or "unknown",
                 "count": entry.observation_count,
                 # SatNOGS gives unidentified objects temporary 99xxx ids.
                 "temporary_id": 99000 <= entry.norad_id < 100000}
                for entry in found]}


def _captures_dir() -> Path:
    """Where a running recorder writes its plan, captures and sidecar.

    Set ``SDRSTREAM_CAPTURES`` to point at the recorder's ``--outdir``; the
    default matches the tool's own default so the two agree out of the box.
    """
    return Path(os.environ.get("SDRSTREAM_CAPTURES", "/tmp/captures"))


def _captures_payload() -> Dict[str, Any]:
    """The recorder's plan and results, read from its output directory.

    The recorder writes ``plan.json`` up front and rewrites ``captures.json``
    after every pass, so reading both gives a live view of what is queued and
    what has landed without the server having to own the campaign.
    """
    directory_ = _captures_dir()
    plan: List[Dict[str, Any]] = []
    captures: List[Dict[str, Any]] = []
    plan_path = directory_ / "plan.json"
    capture_path = directory_ / "captures.json"
    if plan_path.exists():
        try:
            plan = json.loads(plan_path.read_text())
        except (OSError, json.JSONDecodeError):
            plan = []
    if capture_path.exists():
        try:
            captures = json.loads(capture_path.read_text())
        except (OSError, json.JSONDecodeError):
            captures = []
    for record in captures:
        wav = record.get("file")
        record["available"] = bool(
            record.get("samples") and wav and Path(wav).exists())
        record["name"] = Path(wav).name if wav else None
    return {"directory": str(directory_), "plan": plan, "captures": captures}


def _capture_file(name: str) -> Optional[Path]:
    """Resolve a capture name to a path, refusing anything outside the dir."""
    if not name or "/" in name or "\\" in name or name.startswith("."):
        return None
    directory_ = _captures_dir()
    for record in _captures_payload()["captures"]:
        if record.get("name") == name:
            path = Path(record.get("file") or "")
            if path.exists() and path.parent == directory_:
                return path
    return None


def _run_recording(job: RecordJob, request: Dict[str, Any]) -> None:
    """Record in a worker thread and fill in the job when it finishes."""
    try:
        samples, rate = asyncio.run(_record(request))
        if not samples:
            job.state, job.error = "error", "no audio received"
            return
        job.samples = len(samples)
        job.samplerate = rate
        job.peak = max(abs(sample) for sample in samples)
        job.wav = wav_bytes(samples, rate)
        job.state = "done"
    except Exception as exc:  # surfaced to the browser as text
        job.state, job.error = "error", "%s: %s" % (type(exc).__name__, exc)


async def _record(request: Dict[str, Any]):
    server = (request.get("server") or "").strip()
    if not server:
        raise ValueError("server is required")
    if not server.startswith(("http://", "https://", "ws://", "wss://")):
        server = "http://" + server
    seconds = float(request.get("seconds") or 15)
    seconds = max(1.0, min(seconds, 300.0))
    kind = request.get("kind") or "openwebrx"
    mode = (request.get("mode") or "USB").upper()
    frequency_khz = request.get("frequency_khz")

    if kind == "websdr":
        async with WebSDRClient(server) as client:
            await client.tune(
                frequency_khz=float(frequency_khz) if frequency_khz else None,
                mode=mode)
            chunks = await client.record(seconds)
            samples = WebSDRClient.concat(chunks)
            rate = chunks[0].samplerate if chunks else 8000
    else:
        async with OpenWebRXClient(server) as client:
            await client.await_metadata(timeout=2.0)
            await client.tune(
                frequency=int(float(frequency_khz) * 1000)
                if frequency_khz else None,
                modulation=mode)
            chunks = await client.record(seconds)
            samples = OpenWebRXClient.concat(chunks)
            rate = chunks[0].samplerate if chunks else 12000
    return samples, rate


class Handler(BaseHTTPRequestHandler):
    """Routes a small set of paths and nothing else."""

    server_version = "sdrstream"
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):  # quieter than the default
        pass

    def _send(self, status: int, body: bytes, content_type: str) -> None:
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _json(self, status: int, payload: Any) -> None:
        body = json.dumps(payload).encode("utf-8")
        self._send(status, body, "application/json; charset=utf-8")

    def _error(self, status: int, message: str) -> None:
        self._json(status, {"error": message})

    def do_GET(self):  # noqa: N802 - name fixed by BaseHTTPRequestHandler
        parsed = urlparse(self.path)
        query = parse_qs(parsed.query)
        path = parsed.path.rstrip("/") or "/"
        try:
            if path == "/":
                self._send(200, PAGE.encode("utf-8"),
                           "text/html; charset=utf-8")
            elif path == "/api/satellites":
                self._json(200, _satellite_payload())
            elif path == "/api/receivers":
                self._json(200, _receivers_payload(query))
            elif path == "/api/military":
                self._json(200, _military_payload(query))
            elif path == "/api/passes":
                self._json(200, _passes_payload(query))
            elif path == "/api/undocumented":
                self._json(200, _undocumented_payload(query))
            elif path == "/api/captures":
                self._json(200, _captures_payload())
            elif path.startswith("/api/captures/"):
                self._get_capture(path)
            elif path.startswith("/api/record/"):
                self._get_record(path)
            else:
                self._error(404, "no such path: %s" % path)
        except (ValueError, KeyError, FileNotFoundError) as exc:
            self._error(400, str(exc))
        except Exception as exc:
            self._error(502, "%s: %s" % (type(exc).__name__, exc))

    def _get_capture(self, path: str) -> None:
        # /api/captures/<name> serves the WAV itself.
        name = path[len("/api/captures/"):]
        resolved = _capture_file(name)
        if resolved is None:
            self._error(404, "no such capture")
            return
        self._send(200, resolved.read_bytes(), "audio/wav")

    def _get_record(self, path: str) -> None:
        parts = path.split("/")
        # /api/record/<id> or /api/record/<id>/audio
        if len(parts) < 4:
            self._error(404, "no such path")
            return
        job = JOBS.get(parts[3])
        if job is None:
            self._error(404, "no such job")
            return
        if len(parts) >= 5 and parts[4] == "audio":
            if job.state != "done" or job.wav is None:
                self._error(409, "recording is not ready")
                return
            self._send(200, job.wav, "audio/wav")
        else:
            self._json(200, job.public())

    def do_POST(self):  # noqa: N802 - name fixed by BaseHTTPRequestHandler
        parsed = urlparse(self.path)
        path = parsed.path.rstrip("/") or "/"
        if path != "/api/record":
            self._error(404, "no such path: %s" % path)
            return
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b"{}"
        try:
            request = json.loads(raw or b"{}")
        except json.JSONDecodeError as exc:
            self._error(400, "bad JSON: %s" % exc)
            return
        job = RecordJob(id=uuid.uuid4().hex[:12],
                        seconds=float(request.get("seconds") or 15))
        JOBS[job.id] = job
        thread = threading.Thread(target=_run_recording, args=(job, request),
                                  daemon=True)
        thread.start()
        self._json(202, job.public())


def serve(host: str = "127.0.0.1", port: int = 8000) -> None:
    """Run the server until interrupted."""
    httpd = ThreadingHTTPServer((host, port), Handler)
    url = "http://%s:%d" % (host, httpd.server_address[1])
    print("sdrstream web UI on %s" % url)
    print("press Ctrl-C to stop")
    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        print("\nstopping")
    finally:
        httpd.server_close()
