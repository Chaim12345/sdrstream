package web

// indexPage is the browser UI: a live monitor with a spectrum and an
// activity feed, plus the satellite, pass, gallery, capture and station
// tables. It is one string on purpose: no build step, no framework, and
// every endpoint returns JSON.
const indexPage = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>sdrstream</title>
<style>
  :root {
    color-scheme: dark;
    --bg: #0a0d14;
    --panel: #11151f;
    --panel-2: #161b28;
    --line: #1f2635;
    --text: #d9e1ef;
    --muted: #7b8699;
    --accent: #5cc8ff;
    --accent-dim: #1d3b52;
    --good: #34d399;
    --warn: #fbbf24;
    --bad: #f87171;
    --data: #c084fc;
    --voice: #60a5fa;
  }
  * { box-sizing: border-box; }
  body {
    margin: 0; background: var(--bg); color: var(--text);
    font: 13px/1.5 ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  }
  header {
    position: sticky; top: 0; z-index: 10;
    display: flex; align-items: center; gap: 16px; padding: 12px 22px;
    background: rgba(10,13,20,.92); backdrop-filter: blur(8px);
    border-bottom: 1px solid var(--line);
  }
  header h1 { margin: 0; font-size: 15px; letter-spacing: 3px; color: var(--accent); font-weight: 600; }
  header .sub { color: var(--muted); font-size: 11px; }
  .pills { margin-left: auto; display: flex; gap: 8px; flex-wrap: wrap; }
  .pill {
    font-size: 11px; padding: 3px 10px; border-radius: 999px;
    border: 1px solid var(--line); color: var(--muted); background: var(--panel);
  }
  .pill b { color: var(--text); font-weight: 600; }
  .pill.live { color: var(--good); border-color: #14532d; }
  nav { display: flex; gap: 4px; padding: 0 22px; border-bottom: 1px solid var(--line); background: var(--panel); overflow-x: auto; }
  nav button {
    appearance: none; background: none; border: 0; border-bottom: 2px solid transparent;
    color: var(--muted); font: inherit; font-size: 12px; padding: 10px 14px; cursor: pointer; white-space: nowrap;
  }
  nav button:hover { color: var(--text); }
  nav button.active { color: var(--accent); border-bottom-color: var(--accent); }
  main { padding: 20px 22px 60px; max-width: 1280px; margin: 0 auto; }
  section { display: none; }
  section.active { display: block; }
  h2 { font-size: 12px; text-transform: uppercase; letter-spacing: 1.5px; color: var(--accent); margin: 0 0 12px; font-weight: 600; }
  h3 { font-size: 12px; color: var(--muted); margin: 22px 0 8px; font-weight: 500; }
  .panel { background: var(--panel); border: 1px solid var(--line); border-radius: 10px; padding: 14px 16px; }
  .grid { display: grid; gap: 16px; }
  .grid > * { min-width: 0; }
  .grid.two { grid-template-columns: 1fr; }
  @media (min-width: 1000px) { .grid.two { grid-template-columns: 420px 1fr; } }
  table { width: 100%; border-collapse: collapse; font-size: 12px; }
  th, td { text-align: left; padding: 7px 10px; border-bottom: 1px solid var(--line); white-space: nowrap; }
  th { color: var(--muted); font-weight: 500; position: sticky; top: 0; background: var(--panel); }
  tr:hover td { background: var(--panel-2); }
  a { color: var(--accent); text-decoration: none; }
  a:hover { text-decoration: underline; }
  .muted { color: var(--muted); }
  .good { color: var(--good); }
  .warn { color: var(--warn); }
  .bad { color: var(--bad); }
  .badge {
    display: inline-block; font-size: 10px; letter-spacing: .5px; text-transform: uppercase;
    padding: 2px 7px; border-radius: 4px; border: 1px solid var(--line); color: var(--muted);
  }
  .badge.voice { color: var(--voice); border-color: #1e3a8a; }
  .badge.data { color: var(--data); border-color: #4c1d95; }
  .badge.cw, .badge.carrier { color: var(--warn); border-color: #78350f; }
  .conf { font-size: 11px; padding: 1px 6px; border-radius: 8px; border: 1px solid var(--line); margin-left: 6px; }
  .conf.hi { color: #4ade80; border-color: #166534; }
  .conf.mid { color: #fbbf24; border-color: #78350f; }
  .conf.lo { color: #f87171; border-color: #7f1d1d; }
  .badge.noise { color: var(--muted); border-color: var(--line); }
  .badge.silence { color: var(--muted); border-color: var(--line); opacity: .6; }
  #spectrum { width: 100%; max-width: 100%; height: 260px; display: block; border-radius: 8px; background: #06090f; border: 1px solid var(--line); }
  .axis { display: flex; justify-content: space-between; color: var(--muted); font-size: 10px; margin-top: 6px; }
  .feed { display: flex; flex-direction: column; gap: 10px; max-height: 520px; overflow-y: auto; }
  .card-activity { background: var(--panel); border: 1px solid var(--line); border-left: 3px solid var(--muted); border-radius: 8px; padding: 10px 12px; }
  .card-activity.voice { border-left-color: var(--voice); }
  .card-activity.data { border-left-color: var(--data); }
  .card-activity.cw, .card-activity.carrier { border-left-color: var(--warn); }
  .card-activity .row { display: flex; gap: 10px; align-items: baseline; flex-wrap: wrap; }
  .card-activity .freq { color: var(--accent); font-weight: 600; }
  .card-activity .detail { color: var(--text); margin-top: 6px; font-size: 12px; word-break: break-word; }
  .card-activity .meta { color: var(--muted); font-size: 11px; margin-top: 4px; }
  .gallery { display: grid; grid-template-columns: repeat(auto-fill, minmax(230px, 1fr)); gap: 14px; }
  .gcard { background: var(--panel); border: 1px solid var(--line); border-radius: 8px; overflow: hidden; }
  .gcard img { display: block; width: 100%; aspect-ratio: 4/3; object-fit: contain; background: #000; }
  .gcard .body { padding: 8px 10px; font-size: 11px; color: var(--muted); }
  .gcard .body b { color: var(--text); display: block; font-weight: 600; }
  .gcard pre { margin: 6px 0 0; font-size: 10px; color: var(--muted); white-space: pre-wrap; max-height: 90px; overflow: hidden; }
  audio { height: 30px; width: 260px; }
  .toolbar { display: flex; gap: 10px; align-items: center; margin-bottom: 10px; flex-wrap: wrap; }
  button.pill { appearance: none; font: inherit; cursor: pointer; }
  button.pill:hover { color: var(--text); border-color: var(--accent-dim); }
  input[type=search], input[type=text] {
    background: var(--panel-2); border: 1px solid var(--line); border-radius: 6px;
    color: var(--text); font: inherit; font-size: 12px; padding: 6px 10px; min-width: 220px;
  }
  .scroll { max-height: 70vh; overflow: auto; }
  .scroll-x { overflow-x: auto; }
  table.wrap { white-space: normal; }
  :focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
  @media (max-width: 640px) {
    header { flex-wrap: wrap; gap: 8px; padding: 10px 14px; }
    header .sub { display: none; }
    .pills { margin-left: 0; gap: 6px; }
    .pill { padding: 2px 8px; font-size: 10px; }
    nav { padding: 0 14px; }
    nav button { padding: 12px 12px; min-height: 44px; }
    main { padding: 14px 14px 48px; }
    .grid.two { grid-template-columns: 1fr; }
  }
  .empty { color: var(--muted); padding: 14px 0; }
  #msg { color: var(--muted); font-size: 11px; margin-bottom: 12px; }
</style>
</head>
<body>
<header>
  <h1>SDRSTREAM</h1>
  <span class="sub">live receiver monitor</span>
  <div class="pills">
    <span class="pill">passes <b id="pass-count">0</b></span>
    <span class="pill">captures <b id="capture-count">0</b></span>
    <span class="pill">activity <b id="activity-count">0</b></span>
    <span class="pill">stations <b id="station-count">0</b></span>
    <span class="pill live" id="monitor-state">idle</span>
  </div>
</header>
<nav role="tablist" aria-label="Sections">
  <button role="tab" id="tab-live" data-tab="live" aria-controls="panel-live" aria-selected="true" class="active">Live</button>
  <button role="tab" id="tab-satellites" data-tab="satellites" aria-controls="panel-satellites" aria-selected="false">Satellites</button>
  <button role="tab" id="tab-passes" data-tab="passes" aria-controls="panel-passes" aria-selected="false">Passes</button>
  <button role="tab" id="tab-gallery" data-tab="gallery" aria-controls="panel-gallery" aria-selected="false">Gallery</button>
  <button role="tab" id="tab-captures" data-tab="captures" aria-controls="panel-captures" aria-selected="false">Captures</button>
  <button role="tab" id="tab-stations" data-tab="stations" aria-controls="panel-stations" aria-selected="false">Stations</button>
</nav>
<main>
  <div id="msg">loading…</div>

  <section id="panel-live" role="tabpanel" aria-labelledby="tab-live" class="active">
    <div class="grid two">
      <div class="panel">
        <h2 style="margin-top:0">Spectrum</h2>
        <canvas id="spectrum" width="320" height="260" role="img" aria-label="Live spectrum of the monitored receiver"></canvas>
        <div class="axis"><span id="spec-lo">—</span><span id="spec-mid">—</span><span id="spec-hi">—</span></div>
        <h3>Detected transmissions</h3>
        <div class="feed" id="feed" role="log" aria-live="polite" aria-label="Detected transmissions"><div class="empty">no traffic seen yet</div></div>
      </div>
      <div class="panel">
        <h2 style="margin-top:0">What the monitor can do</h2>
        <p class="muted" style="margin-top:0">
          Every transmission that rises above the noise floor is recorded, then classified:
          data is handed to the packet, paging and telemetry decoders (APRS/AX.25, RTTY, PSK31,
          POCSAG, DTMF, EAS, NAVTEX, SSTV) and voice is transcribed with whisper.cpp.
        </p>
        <div class="scroll-x"><table class="wrap">
          <thead><tr><th>kind</th><th>meaning</th><th>what is produced</th></tr></thead>
          <tbody>
            <tr><td><span class="badge voice">voice</span></td><td>speech-like audio</td><td>transcript, or speech segments when no backend is installed</td></tr>
            <tr><td><span class="badge data">data</span></td><td>packet or paging traffic</td><td>decoded payload, e.g. an AX.25 frame with its text</td></tr>
            <tr><td><span class="badge cw">cw</span></td><td>keyed carrier</td><td>Morse text</td></tr>
            <tr><td><span class="badge carrier">carrier</span></td><td>narrowband signal</td><td>frequency and width</td></tr>
          </tbody>
        </table></div>
        <h3>Latest captures</h3>
        <div id="live-captures" class="empty">none yet</div>
      </div>
    </div>
  </section>

  <section id="panel-satellites" role="tabpanel" aria-labelledby="tab-satellites">
    <div class="panel scroll">
      <table id="sats">
        <thead><tr><th>name</th><th>norad</th><th>downlink</th><th>mode</th><th>relay</th><th>status</th><th>receivers</th></tr></thead>
        <tbody></tbody>
      </table>
    </div>
  </section>

  <section id="panel-passes" role="tabpanel" aria-labelledby="tab-passes">
    <div class="panel scroll">
      <table id="passes">
        <thead><tr><th>satellite</th><th>receiver</th><th>rise</th><th>peak</th><th>set</th><th>max el</th></tr></thead>
        <tbody></tbody>
      </table>
    </div>
  </section>

  <section id="panel-gallery" role="tabpanel" aria-labelledby="tab-gallery">
    <div class="toolbar">
      <span class="muted" id="gallery-count"></span>
    </div>
    <div class="gallery" id="gallery"></div>
  </section>

  <section id="panel-captures" role="tabpanel" aria-labelledby="tab-captures">
    <div class="panel scroll">
      <table id="captures">
        <thead><tr><th>satellite</th><th>receiver</th><th>start</th><th>samples</th><th>rate</th><th>file</th><th>listen</th></tr></thead>
        <tbody></tbody>
      </table>
    </div>
  </section>

  <section id="panel-stations" role="tabpanel" aria-labelledby="tab-stations">
    <div class="toolbar">
      <label class="muted" for="station-filter">Filter</label>
      <input type="search" id="station-filter" placeholder="name, type or URL">
      <span class="muted" id="station-summary"></span>
      <button id="station-more" class="pill" hidden>show more</button>
    </div>
    <div class="panel scroll">
      <table id="stations">
        <thead><tr><th>type</th><th>name</th><th>url</th><th>latitude</th><th>longitude</th></tr></thead>
        <tbody></tbody>
      </table>
    </div>
  </section>
</main>
<script>
const el = id => document.getElementById(id);
const fetchJSON = async (url) => {
  const response = await fetch(url);
  if (!response.ok) throw new Error(url + ' → HTTP ' + response.status);
  const text = await response.text();
  try { return JSON.parse(text); } catch (err) { throw new Error(url + ' → not JSON'); }
};
const fmtTime = iso => new Date(iso).toLocaleTimeString([], {hour12:false});
const fmtDate = iso => new Date(iso).toLocaleString([], {hour12:false});
const mhz = hz => (hz / 1e6).toFixed(4) + ' MHz';
const row = cells => { const tr = document.createElement('tr'); cells.forEach(c => { const td = document.createElement('td'); if (c instanceof Node) td.appendChild(c); else td.textContent = c; tr.appendChild(td); }); return tr; };
const safeURL = value => { try { const u = new URL(value, location.href); return (u.protocol === 'http:' || u.protocol === 'https:') ? u.href : ''; } catch { return ''; } };
const link = (href, text) => {
  const a = document.createElement('a');
  const url = safeURL(href);
  if (url) { a.href = url; a.target = '_blank'; a.rel = 'noopener noreferrer'; }
  a.textContent = text;
  return a;
};
const shortURL = u => { try { return new URL(u).host; } catch { return u; } };

document.querySelectorAll('nav button').forEach(button => {
  button.addEventListener('click', () => {
    document.querySelectorAll('nav button').forEach(b => {
      b.classList.remove('active');
      b.setAttribute('aria-selected', 'false');
    });
    document.querySelectorAll('section').forEach(s => s.classList.remove('active'));
    button.classList.add('active');
    button.setAttribute('aria-selected', 'true');
    el('panel-' + button.dataset.tab).classList.add('active');
  });
});

async function refreshStatus() {
  try {
    const status = await fetchJSON('/api/status.json');
    el('pass-count').textContent = (status.passes || []).length;
    el('capture-count').textContent = (status.captures || []).length;
    el('msg').textContent = 'updated ' + fmtTime(status.updated_at) + ' — ' + status.receivers + ' receivers in the catalogue';

    const passes = el('passes').querySelector('tbody');
    passes.textContent = '';
    (status.passes || []).slice(0, 40).forEach(p => {
      const soon = new Date(p.rise) - Date.now() < 15 * 60 * 1000;
      const elevation = (p.max_elevation || 0).toFixed(1) + '°';
      const tr = row([p.satellite, shortURL(p.receiver), fmtTime(p.rise), fmtTime(p.peak), fmtTime(p.set), elevation]);
      if (soon) tr.className = 'warn';
      passes.appendChild(tr);
    });
    if (!(status.passes || []).length) passes.appendChild(row(['no passes predicted']));

    // The captures table is only rebuilt when the set of recordings
    // changes: rebuilding it every few seconds would replace the audio
    // element and stop whatever is playing.
    const captures = el('captures').querySelector('tbody');
    const captureKey = (status.captures || []).map(c => c.path + '|' + c.url).join(',');
    if (captures.dataset.key !== captureKey) {
      captures.dataset.key = captureKey;
      captures.textContent = '';
      (status.captures || []).slice().reverse().forEach(c => {
        const cell = document.createElement('td');
        if (c.url) cell.appendChild(link(c.url, c.path)); else cell.textContent = c.path;
        const audioCell = document.createElement('td');
        if (c.url) {
          const player = document.createElement('audio');
          player.controls = true; player.src = c.url;
          player.setAttribute('aria-label', 'listen to ' + (c.satellite || 'capture'));
          audioCell.appendChild(player);
        }
        const tr = row([c.satellite, shortURL(c.receiver), fmtDate(c.start), c.samples, c.rate + ' Hz']);
        tr.appendChild(cell); tr.appendChild(audioCell);
        captures.appendChild(tr);
      });
      if (!(status.captures || []).length) captures.appendChild(row(['none yet — run a capture']));
    }

    const live = el('live-captures');
    const recent = (status.captures || []).slice(-3).reverse();
    if (!recent.length) { live.className = 'empty'; live.textContent = 'none yet'; }
    else {
      live.className = '';
      live.textContent = '';
      recent.forEach(c => {
        const line = document.createElement('div');
        line.style.marginBottom = '8px';
        line.appendChild(link(c.url || '#', c.satellite + ' ' + fmtTime(c.start) + ' (' + c.rate + ' Hz)'));
        live.appendChild(line);
      });
    }
  } catch (err) { el('msg').textContent = 'status unavailable: ' + err; }
}

async function refreshSats() {
  try {
    const sats = await fetchJSON('/api/satellites.json');
    const body = el('sats').querySelector('tbody');
    body.textContent = '';
    (sats || []).forEach(s => {
      const tr = row([s.name, s.norad_id, (s.downlink_hz / 1e6).toFixed(4) + ' MHz', s.mode, s.relay || '—', s.status, s.receivers]);
      tr.className = 'sat-status-' + s.status;
      body.appendChild(tr);
    });
  } catch (err) { /* static list */ }
}

async function refreshGallery() {
  try {
    const items = await fetchJSON('/api/gallery.json');
    const box = el('gallery');
    box.textContent = '';
    el('gallery-count').textContent = items.length ? items.length + ' artefacts from SatNOGS observations' : '';
    items.slice(0, 36).forEach(it => {
      const card = document.createElement('div');
      card.className = 'gcard';
      if (it.kind === 'image') {
        const img = document.createElement('img');
        img.loading = 'lazy'; img.src = it.url; img.alt = it.satellite;
        card.appendChild(img);
      } else if (it.kind === 'text') {
        const pre = document.createElement('pre');
        pre.textContent = it.text || '';
        card.appendChild(pre);
      }
      const body = document.createElement('div');
      body.className = 'body';
      const name = document.createElement('b');
      name.textContent = it.satellite || ('#' + it.observation_id);
      body.appendChild(name);
      body.appendChild(document.createTextNode((it.station || '') + ' · ' + fmtDate(it.start)));
      card.appendChild(body);
      box.appendChild(card);
    });
    if (!items.length) {
      const note = document.createElement('div');
      note.className = 'empty';
      note.textContent = 'no SatNOGS artefacts downloaded yet';
      box.appendChild(note);
    }
  } catch (err) {
    const box = el('gallery');
    box.textContent = '';
    const note = document.createElement('div');
    note.className = 'empty';
    note.textContent = 'gallery unavailable: ' + err;
    box.appendChild(note);
  }
}

let stationRows = [];
let stationLimit = 400;
let stationError = '';
async function refreshStations() {
  try {
    const stations = await fetchJSON('/api/stations.json');
    stationRows = stations || [];
    stationError = '';
    el('station-count').textContent = stationRows.length;
  } catch (err) {
    // A failing directory is not an empty one: keep the last good list if
    // there is one and say so instead of showing "no stations".
    stationError = err.message || String(err);
  }
  renderStations();
}
function renderStations() {
  const needle = el('station-filter').value.toLowerCase();
  const filtered = stationRows.filter(s => !needle || (s.name + ' ' + s.type + ' ' + s.url).toLowerCase().includes(needle));
  const shown = filtered.slice(0, stationLimit);
  const counts = {};
  stationRows.forEach(s => counts[s.type] = (counts[s.type] || 0) + 1);
  const summary = Object.keys(counts).sort().map(k => counts[k] + ' ' + k).join(' · ');
  if (stationError) {
    el('station-summary').textContent = 'directory unavailable — ' + stationError;
  } else {
    el('station-summary').textContent = summary +
      (filtered.length > shown.length ? ' · showing ' + shown.length + ' of ' + filtered.length : '');
  }
  const more = el('station-more');
  more.hidden = filtered.length <= shown.length;
  if (!more.hidden) {
    more.textContent = 'show ' + Math.min(400, filtered.length - shown.length) + ' more of ' + filtered.length;
  }
  const body = el('stations').querySelector('tbody');
  body.textContent = '';
  shown.forEach(s => {
    body.appendChild(row([s.type, s.name, link(s.url, shortURL(s.url)), s.latitude.toFixed(4), s.longitude.toFixed(4)]));
  });
  if (!shown.length) {
    const tr = document.createElement('tr');
    const td = document.createElement('td');
    td.colSpan = 5; td.className = 'empty';
    td.textContent = stationError ? 'directory unavailable, retrying every five minutes'
      : (needle ? 'no station matches the filter' : 'no stations in the directory');
    tr.appendChild(td); body.appendChild(tr);
  }
}
el('station-filter').addEventListener('input', () => { stationLimit = 400; renderStations(); });
el('station-more').addEventListener('click', () => { stationLimit += 400; renderStations(); });

function drawSpectrum(bins, centre, span) {
  const canvas = el('spectrum');
  const ctx = canvas.getContext('2d');
  const ratio = window.devicePixelRatio || 1;
  const width = Math.max(320, Math.floor(canvas.clientWidth * ratio));
  const height = Math.max(160, Math.floor(canvas.clientHeight * ratio));
  if (canvas.width !== width || canvas.height !== height) {
    canvas.width = width;
    canvas.height = height;
  }
  const w = canvas.width, h = canvas.height;
  ctx.clearRect(0, 0, w, h);
  if (!bins || !bins.length) return;
  if (bins.length < 2) return;
  const min = Math.min.apply(null, bins), max = Math.max.apply(null, bins);
  const range = Math.max(1, max - min);
  // filled spectrum
  ctx.beginPath();
  ctx.moveTo(0, h);
  bins.forEach((v, i) => {
    const x = (i / (bins.length - 1)) * w;
    const y = h - ((v - min) / range) * (h - 12) - 6;
    ctx.lineTo(x, y);
  });
  ctx.lineTo(w, h);
  ctx.closePath();
  const gradient = ctx.createLinearGradient(0, 0, 0, h);
  gradient.addColorStop(0, 'rgba(92,200,255,.55)');
  gradient.addColorStop(1, 'rgba(92,200,255,.04)');
  ctx.fillStyle = gradient;
  ctx.fill();
  ctx.strokeStyle = '#5cc8ff';
  ctx.lineWidth = 1;
  ctx.beginPath();
  bins.forEach((v, i) => {
    const x = (i / (bins.length - 1)) * w;
    const y = h - ((v - min) / range) * (h - 12) - 6;
    i ? ctx.lineTo(x, y) : ctx.moveTo(x, y);
  });
  ctx.stroke();
  el('spec-lo').textContent = mhz(centre - span / 2);
  el('spec-mid').textContent = mhz(centre);
  el('spec-hi').textContent = mhz(centre + span / 2);
}

async function refreshSpectrum() {
  try {
    const spec = await fetchJSON('/api/spectrum.json');
    if (spec.available && spec.bins && spec.bins.length) {
      drawSpectrum(spec.bins, spec.centre_hz, spec.span_hz);
      el('monitor-state').textContent = 'monitoring';
      el('monitor-state').className = 'pill live';
    } else {
      el('monitor-state').textContent = 'no spectrum';
      el('monitor-state').className = 'pill';
    }
  } catch (err) {
    el('monitor-state').textContent = 'no monitor';
    el('monitor-state').className = 'pill';
  }
}

async function refreshActivity() {
  try {
    const items = await fetchJSON('/api/activity.json');
    el('activity-count').textContent = items.length;
    const feed = el('feed');
    feed.textContent = '';
    if (!items.length) { feed.innerHTML = '<div class="empty">no traffic seen yet</div>'; return; }
    items.slice(0, 40).forEach(a => {
      const card = document.createElement('div');
      card.className = 'card-activity ' + (a.kind || '');
      const rowEl = document.createElement('div');
      rowEl.className = 'row';
      const freq = document.createElement('span');
      freq.className = 'freq'; freq.textContent = mhz(a.frequency_hz);
      const badge = document.createElement('span');
      badge.className = 'badge ' + (a.kind || ''); badge.textContent = a.kind || 'unknown';
      const time = document.createElement('span');
      time.className = 'muted'; time.textContent = fmtTime(a.started);
      const level = document.createElement('span');
      level.className = 'muted';
      level.textContent = (a.level_db || 0).toFixed(0) + ' dB' + (a.bandwidth_hz ? ' · ' + (a.bandwidth_hz / 1000).toFixed(0) + ' kHz wide' : '');
      rowEl.appendChild(freq); rowEl.appendChild(badge); rowEl.appendChild(time); rowEl.appendChild(level);
      card.appendChild(rowEl);
      if (a.mode) {
        const meta = document.createElement('div');
        meta.className = 'meta'; meta.textContent = a.mode + (a.source ? ' · ' + shortURL(a.source) : '');
        card.appendChild(meta);
      }
      if (a.detail) {
        const detail = document.createElement('div');
        detail.className = 'detail'; detail.textContent = a.detail;
        if (a.confidence) {
          const pct = Math.round(a.confidence * 100);
          const chip = document.createElement('span');
          chip.className = 'conf ' + (pct >= 80 ? 'hi' : pct >= 60 ? 'mid' : 'lo');
          chip.textContent = pct + '%' + (a.language ? ' · ' + a.language : '') + (a.asr_fallback ? ' · fallback model' : '');
          chip.title = 'Mean whisper token probability' + (a.asr_model ? ' (' + a.asr_model + ')' : '') + '. Low values mean the transcript is a guess.';
          detail.appendChild(chip);
        }
        card.appendChild(detail);
      }
      feed.appendChild(card);
    });
  } catch (err) { /* monitor offline */ }
}

refreshStatus(); refreshSats(); refreshGallery(); refreshStations();
setInterval(refreshStatus, 5000);
setInterval(refreshActivity, 4000);
setInterval(refreshSpectrum, 1000);
setInterval(refreshGallery, 60000);
setInterval(refreshStations, 300000);
</script>
</body>
</html>
`
