'use strict';
/* sdrstream UI - vanilla JS, no build step. Talks to the existing JSON API
   plus /api/map.json and /api/tiles/ (same-origin tile proxy). */

const el = id => document.getElementById(id);
/* Per-endpoint diagnostics: every API call records its outcome so the
   Health view can show which endpoint failed, when, and how fast - on the
   device itself, without ADB. A hung request must never leave a control at
   its placeholder forever, so every call has a timeout. */
const apiDiag = {};
const FETCH_TIMEOUT_MS = 10000;
const fetchJSON = async (url, options = {}) => {
  const key = url.split('?')[0];
  const started = Date.now();
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), options.timeout || FETCH_TIMEOUT_MS);
  const note = (ok, err) => { apiDiag[key] = { ok, ms: Date.now() - started, at: Date.now(), err: err || '' }; renderApiDiag(); };
  try {
    const response = await fetch(url, { credentials: 'same-origin', ...options, signal: ctrl.signal });
    if (response.status === 401) { toast('Login expired. Sign in again.', undefined, true); setTimeout(()=>{location.href='/login'},250); note(false, 'login required'); throw new Error('login required'); }
    if (!response.ok) { note(false, 'HTTP ' + response.status); throw new Error(url + ' → HTTP ' + response.status); }
    try { const data = await response.json(); note(true); return data; }
    catch { note(false, 'not JSON'); throw new Error(url + ' → not JSON'); }
  } catch (err) {
    if (err && err.name === 'AbortError') { note(false, 'timeout'); throw new Error(url + ' → timeout'); }
    if (!(key in apiDiag)) note(false, String(err && err.message || err));
    throw err;
  } finally { clearTimeout(timer); }
};
const fmtTime = iso => new Date(iso).toLocaleTimeString([], { hour12: false });
const fmtDate = iso => new Date(iso).toLocaleString([], { hour12: false });
const mhz = hz => (hz / 1e6).toFixed(4) + ' MHz';
const safeURL = value => { try { const u = new URL(value, location.href); return (u.protocol === 'http:' || u.protocol === 'https:') ? u.href : ''; } catch { return ''; } };
const link = (href, text) => {
  const a = document.createElement('a');
  const url = safeURL(href);
  if (url) { a.href = url; a.target = '_blank'; a.rel = 'noopener noreferrer'; }
  a.textContent = text;
  return a;
};
const shortURL = u => { try { return new URL(u).host || u; } catch { return u; } };
const relTime = iso => {
  const s = Math.max(0, Math.round((Date.now() - new Date(iso)) / 1000));
  if (s < 60) return s + 's ago';
  if (s < 3600) return Math.floor(s / 60) + 'm ago';
  if (s < 86400) return Math.floor(s / 3600) + 'h ago';
  return Math.floor(s / 86400) + 'd ago';
};
const countdownTo = iso => {
  let s = Math.max(0, Math.round((new Date(iso) - Date.now()) / 1000));
  const d = Math.floor(s / 86400); s -= d * 86400;
  const h = Math.floor(s / 3600); s -= h * 3600;
  const m = Math.floor(s / 60); const sec = s - m * 60;
  const pad = n => String(n).padStart(2, '0');
  return (d ? d + 'd ' : '') + pad(h) + ':' + pad(m) + ':' + pad(sec);
};

/* ---------- settings (localStorage only) ---------- */
const DEFAULT_SETTINGS = {
  theme: 'dark',          // dark | light | auto
  compact: false,         // denser cards
  spectrumRate: 1000,     // ms, 0 disables the spectrum poller
  activityRate: 4000,     // ms
  toasts: true,           // toast on new activity
  pageSize: 50,           // decodes per page in the activity history
};
let settings = (() => {
  try { return { ...DEFAULT_SETTINGS, ...JSON.parse(localStorage.getItem('sdrstream-settings') || '{}') }; }
  catch { return { ...DEFAULT_SETTINGS }; }
})();
function saveSettings() {
  try { localStorage.setItem('sdrstream-settings', JSON.stringify(settings)); } catch { /* private mode */ }
}
function applyTheme() {
  const theme = settings.theme === 'auto'
    ? (window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark')
    : settings.theme;
  document.documentElement.dataset.theme = theme;
  const meta = document.querySelector('meta[name="theme-color"]');
  if (meta) meta.content = theme === 'light' ? '#eef2f8' : '#070a12';
  document.body.classList.toggle('compact', !!settings.compact);
}
window.matchMedia('(prefers-color-scheme: light)').addEventListener('change', () => {
  if (settings.theme === 'auto') applyTheme();
});

/* ---------- toasts ---------- */
function toast(text, tone, force) {
  if (!force && !settings.toasts) return;
  const box = el('toasts');
  while (box.children.length >= 3) box.firstChild.remove();
  const t = document.createElement('div');
  t.className = 'toast';
  if (tone) t.style.borderLeftColor = getComputedStyle(document.documentElement).getPropertyValue('--' + tone) || '';
  t.textContent = text;
  box.appendChild(t);
  setTimeout(() => { t.classList.add('out'); setTimeout(() => t.remove(), 450); }, 4200);
}

/* ---------- view router ---------- */
const views = document.querySelectorAll('.mainnav button');
function showView(name) {
  views.forEach(b => {
    const on = b.dataset.view === name;
    b.classList.toggle('active', on);
    if (on) b.setAttribute('aria-current', 'page'); else b.removeAttribute('aria-current');
  });
  document.querySelectorAll('.view').forEach(v => v.classList.toggle('active', v.id === 'view-' + name));
  if (name === 'map') initMap();
  if (location.hash !== '#'+name) history.replaceState(null,'','#'+name);
}
views.forEach(b => { if (b.dataset.view) b.addEventListener('click', () => showView(b.dataset.view)); });
/* The More button collapses the nav to its first five entries on narrow
   screens; it is not a view, so it must never reach showView (an undefined
   view blanks every panel). Start collapsed on mobile widths. */
const more=el('nav-more');
const collapseNav=collapse=>{if(!more)return;more.setAttribute('aria-expanded',String(!collapse));document.querySelectorAll('.mainnav button').forEach((b,i)=>{if(i>=5&&b!==more)b.hidden=collapse})};
if(more) more.onclick=()=>collapseNav(more.getAttribute('aria-expanded')==='true');
collapseNav(window.matchMedia('(max-width: 899px)').matches);
window.addEventListener('hashchange',()=>{const name=location.hash.slice(1);if(el('view-'+name)) showView(name)});
document.querySelectorAll('[data-goto]').forEach(b => b.addEventListener('click', () => showView(b.dataset.goto)));

/* ---------- status / stat cards / next pass ---------- */
let latestStatus = null;
let nextPass = null;
function renderStatCards(status) {
  const cards = [
    { label: 'monitor', value: monitorState === 'monitoring' ? 'LIVE' : monitorState, cls: monitorState === 'monitoring' ? 'good' : '' },
    { label: 'receivers', value: status.receivers ?? '–', cls: 'accent' },
    { label: 'passes 24h', value: (status.passes || []).length, cls: '' },
    { label: 'captures', value: (status.captures || []).length, cls: '' },
  ];
  const box = el('stat-cards');
  box.textContent = '';
  cards.forEach(c => {
    const d = document.createElement('div');
    d.className = 'stat ' + c.cls;
    const v = document.createElement('div'); v.className = 'stat-value'; v.textContent = c.value;
    const l = document.createElement('div'); l.className = 'stat-label'; l.textContent = c.label;
    d.appendChild(v); d.appendChild(l); box.appendChild(d);
  });
}
function renderNextPass(status) {
  const upcoming = (status.passes || []).filter(p => new Date(p.rise) > new Date());
  upcoming.sort((a, b) => new Date(a.rise) - new Date(b.rise));
  nextPass = upcoming[0] || null;
  const card = el('next-pass');
  if (!nextPass) { card.hidden = true; return; }
  card.hidden = false;
  el('np-sat').textContent = nextPass.satellite;
  el('np-receiver').textContent = shortURL(nextPass.receiver);
  el('np-meta').textContent = 'rise ' + fmtTime(nextPass.rise) + ' · peak ' + fmtTime(nextPass.peak) + ' · max el ' + (nextPass.max_elevation || 0).toFixed(1) + '°';
  let warn = el('np-warn');
  if (!warn) { warn = document.createElement('div'); warn.id = 'np-warn'; warn.className = 'pass-warn'; card.appendChild(warn); }
  if (nextPass.status && nextPass.status !== 'active') {
    warn.hidden = false;
    warn.textContent = nextPass.status + ' - orbital prediction only; transmitter unvalidated, no reception promised';
  } else { warn.hidden = true; }
}
setInterval(() => {
  if (nextPass) el('np-countdown').textContent = countdownTo(nextPass.rise);
  document.querySelectorAll('[data-countdown-to]').forEach(node => { node.textContent = countdownTo(node.dataset.countdownTo); });
}, 1000);

async function refreshStatus() {
  try {
    const status = await fetchJSON('/api/status.json');
    latestStatus = status;
    el('pass-count').textContent = (status.passes || []).length;
    el('capture-count').textContent = (status.captures || []).length;
    el('receiver-count').textContent = status.receivers ?? '–';
    el('msg').textContent = 'updated ' + fmtTime(status.updated_at) + ' — ' + status.receivers + ' receivers in the catalogue';
    renderStatCards(status);
    renderNextPass(status);
    renderPasses(status);
    renderCaptures(status);
    renderHealth();
  } catch (err) { el('msg').textContent = 'status unavailable: ' + err; }
}

/* ---------- passes view ---------- */
function renderPasses(status) {
  const box = el('passes-list');
  const passes = (status.passes || []).slice().sort((a, b) => new Date(a.rise) - new Date(b.rise));
  box.textContent = '';
  if (!passes.length) { box.innerHTML = '<div class="empty">no passes predicted</div>'; return; }
  const now = Date.now();
  passes.slice(0, 60).forEach(p => {
    const rise = new Date(p.rise), set = new Date(p.set);
    const card = document.createElement('div');
    card.className = 'card';
    if (rise <= now && set >= now) card.classList.add('active-now');
    else if (rise - now < 15 * 60 * 1000) card.classList.add('soon');
    const title = document.createElement('div'); title.className = 'card-title';
    const b = document.createElement('b'); b.textContent = p.satellite;
    const cd = document.createElement('span'); cd.className = 'countdown' + (rise - now < 15 * 60 * 1000 ? ' warn' : '');
    if (rise <= now && set >= now) { cd.textContent = 'in progress'; }
    else if (rise > now) { cd.dataset.countdownTo = p.rise; cd.textContent = countdownTo(p.rise); }
    else { cd.textContent = 'done'; cd.classList.add('muted'); }
    title.appendChild(b); title.appendChild(cd);
    const meta = document.createElement('div'); meta.className = 'card-meta';
    meta.textContent = shortURL(p.receiver) + ' · rise ' + fmtTime(p.rise) + ' · peak ' + fmtTime(p.peak) + ' · set ' + fmtTime(p.set) + ' · max el ' + (p.max_elevation || 0).toFixed(1) + '°';
    card.appendChild(title); card.appendChild(meta);
    if (p.status && p.status !== 'active') {
      const w = document.createElement('div'); w.className = 'pass-warn';
      w.textContent = p.status + ' - orbital prediction only; transmitter unvalidated';
      card.appendChild(w);
    }
    box.appendChild(card);
  });
}

/* ---------- captures ---------- */
/* Per-item proof for one archived receiver capture (PR #5 record-all
   archive): the saved audio, decode text, receiver ID and UTC time, read
   from the capture sidecar the scanner writes next to every clip. */
/* The Go live quality gate and an independent reference cross-check are
   separate proof facets. Both are shown explicitly; a legacy capture whose
   summary bool is true passed the gate (that was its documented meaning),
   while a cross-check is never inferred - "none recorded" when absent. */
function gateFacetOf(c) { return c.gate_verdict || (c.verified ? 'passed' : ''); }
function facetChips(c) {
  const gv = gateFacetOf(c);
  const g = document.createElement('span');
  g.className = 'badge ' + (gv === 'passed' ? 'verified' : (gv === 'failed' ? 'unverified' : 'blind'));
  g.textContent = 'gate: ' + (gv || 'not recorded');
  if (c.gate) g.title = 'quality gate: ' + c.gate;
  const cv = c.crosscheck || '';
  const x = document.createElement('span');
  x.className = 'badge ' + (cv === 'verified' ? 'verified' : (cv === 'mismatch' ? 'unverified' : 'blind'));
  x.textContent = 'cross-check: ' + (cv || 'none recorded');
  if (c.crosscheck_ref) x.title = 'reference: ' + c.crosscheck_ref + (c.crosscheck_detail ? ' - ' + c.crosscheck_detail : '');
  return [g, x];
}

/* Clip length from the parsed WAV header values (samples / rate), shown so a
   card carries the real duration even before a browser loads media metadata. */
const fmtDuration = sec => {
  if (!isFinite(sec) || sec <= 0) return '';
  const m = Math.floor(sec / 60), r = Math.round(sec - m * 60);
  return (m ? m + 'm ' : '') + r + 's';
};

function captureProof(c) {
  const wrap = document.createElement('div');
  const pb = document.createElement('button');
  pb.className = 'chip proof-toggle'; pb.type = 'button'; pb.textContent = 'proof';
  const pf = document.createElement('div');
  pf.className = 'capture-proof'; pf.hidden = true;
  const row = (label, valueNode) => {
    const r = document.createElement('div'); r.className = 'proof-row';
    const l = document.createElement('span'); l.className = 'proof-label'; l.textContent = label;
    const v = document.createElement('span'); v.className = 'proof-value';
    if (typeof valueNode === 'string') v.textContent = valueNode; else v.appendChild(valueNode);
    r.append(l, v); pf.appendChild(r);
  };
  row('saved audio', c.url ? link(c.url, c.path) : c.path);
  if (c.url) {
    const audio = document.createElement('audio');
    audio.className = 'proof-audio'; audio.controls = true; audio.preload = 'none'; audio.src = c.url;
    row('listen', audio);
    if (/\.wav$/i.test(c.url)) {
      const spec = document.createElement('img');
      spec.className = 'proof-spec'; spec.loading = 'lazy'; spec.alt = 'spectrogram of the archived clip';
      spec.src = c.url + '.spec.png';
      spec.onerror = () => { spec.hidden = true; };
      row('spectrogram', spec);
    }
  } else {
    const none = document.createElement('span'); none.className = 'muted';
    none.textContent = 'audio unavailable - only the decode text survives';
    row('listen', none);
  }
  row('receiver', c.receiver || 'unknown');
  const started = new Date(c.start);
  row('recorded (UTC)', isNaN(started) ? String(c.start || 'unknown') : started.toISOString().replace('T', ' ').replace(/\.\d+Z$/, 'Z'));
  if (c.frequency_hz) row('frequency', mhz(c.frequency_hz) + (c.mode ? ' ' + c.mode : ''));
  const raw = document.createElement('pre');
  raw.className = 'proof-decode';
  raw.textContent = c.decode || 'no decode text saved for this clip';
  row('raw decode', raw);
  row('live gate (Go)', (gateFacetOf(c) || 'not recorded') + (c.gate ? ' - ' + c.gate : ''));
  row('cross-check', (c.crosscheck || 'none recorded') + (c.crosscheck_ref ? ' via ' + c.crosscheck_ref : '') + (c.crosscheck_detail ? ' - ' + c.crosscheck_detail : ''));
  row('verdict', c.crosscheck === 'verified' && gateFacetOf(c) === 'passed'
    ? 'verified live - gate passed and independently confirmed'
    : (c.verified ? 'gate passed - no independent cross-check recorded' : 'unverified'));
  row('origin', c.kind === 'archive' ? 'archive import' + (c.target ? ' - original target "' + c.target + '"' : '') : (c.target ? 'scheduled target "' + c.target + '"' : 'blind scan (no named target)'));
  row('archive', c.samples + ' samples at ' + c.rate + ' Hz' + (c.samples && c.rate ? ' (' + fmtDuration(c.samples / c.rate) + ')' : ''));
  pb.onclick = () => { pf.hidden = !pf.hidden; };
  wrap.append(pb, pf);
  return wrap;
}

/* ---------- receiver health & capture success ----------
   Summarises /api/metrics.json (uptime, scan outcomes, feed sources), the
   proof ledger (/api/ledger.json) and the archived captures from
   status.json: gate-passed vs unverified, scheduled target vs blind scan. */
const SCAN_OK_STATES = new Set(['decoded', 'listening', 'recording', 'decoding', 'tuned', 'connecting', 'idle', 'waiting', 'ok']);
let healthSnapshot = { metrics: null, ledger: null, atMs: 0 };

const fmtUptime = total => {
  let s = Math.max(0, Math.round(total));
  const d = Math.floor(s / 86400); s -= d * 86400;
  const h = Math.floor(s / 3600); s -= h * 3600;
  const m = Math.floor(s / 60);
  return (d ? d + 'd ' : '') + (d || h ? h + 'h ' : '') + m + 'm';
};

function hbar(box, segs) {
  box.textContent = '';
  const total = segs.reduce((n, x) => n + x.count, 0);
  if (!total) { const e = document.createElement('div'); e.className = 'hbar-empty'; box.appendChild(e); return; }
  segs.forEach(x => {
    if (!x.count) return;
    const seg = document.createElement('div');
    seg.className = 'hbar-seg ' + x.tone;
    seg.style.width = Math.max(1.5, x.count / total * 100) + '%';
    seg.title = x.label + ': ' + x.count;
    box.appendChild(seg);
  });
}
function hlegend(box, segs, emptyText) {
  box.textContent = '';
  const total = segs.reduce((n, x) => n + x.count, 0);
  if (!total) { box.textContent = emptyText; return; }
  segs.forEach((x, i) => {
    if (!x.count) return;
    if (i) box.appendChild(document.createTextNode(' · '));
    const dot = document.createElement('span'); dot.className = 'hbar-dot ' + x.tone;
    const t = document.createElement('span'); t.textContent = x.label + ' ' + x.count;
    box.append(dot, t);
  });
}

async function refreshHealth() {
  try {
    const [metrics, ledger] = await Promise.all([
      fetchJSON('/api/metrics.json'),
      fetchJSON('/api/ledger.json?limit=5000'),
    ]);
    healthSnapshot = { metrics, ledger, atMs: Date.now() };
    renderHealth();
  } catch {
    el('health-updated').textContent = 'health data unavailable';
  }
}

function renderHealth() {
  const m = healthSnapshot.metrics;
  if (!m) return;
  el('health-updated').textContent = 'updated ' + relTime(new Date(healthSnapshot.atMs).toISOString());
  el('health-uptime').textContent = fmtUptime(m.uptime_seconds || 0);
  el('health-stations').textContent = m.scan_stations != null ? m.scan_stations : '–';
  el('health-captures').textContent = m.captures != null ? m.captures : '–';

  const outcomes = m.scan_outcomes || {};
  const scanSegs = Object.keys(outcomes).sort().map(k => ({
    label: k, count: outcomes[k], tone: SCAN_OK_STATES.has(k) ? 'good' : 'bad',
  }));
  hbar(el('health-scan-bar'), scanSegs);
  hlegend(el('health-scan-legend'), scanSegs, 'no scan outcomes recorded since boot');
  el('health-scan-note').textContent = m.scan_stations ? '' : 'scanner not running since boot';

  const caps = (latestStatus && latestStatus.captures) || [];
  const verified = caps.filter(c => c.verified).length;
  const unverified = caps.length - verified;
  el('health-success').textContent = caps.length ? Math.round(verified / caps.length * 100) + '%' : '–';
  const crossChecked = caps.filter(c => c.crosscheck === 'verified').length;
  hbar(el('health-success-bar'), [
    { label: 'gate passed', count: verified, tone: 'good' },
    { label: 'unverified', count: unverified, tone: 'warn' },
  ]);
  const gates = {};
  caps.forEach(c => { if (!c.verified && c.gate) gates[c.gate] = (gates[c.gate] || 0) + 1; });
  const gateText = Object.keys(gates).sort((a, b) => gates[b] - gates[a]).slice(0, 3).map(g => g + ' ×' + gates[g]).join(', ');
  hlegend(el('health-success-legend'), [
    { label: 'gate passed', count: verified, tone: 'good' },
    { label: 'unverified', count: unverified, tone: 'warn' },
  ], 'no archived captures yet');
  if (crossChecked) el('health-success-legend').appendChild(document.createTextNode(' · independently verified: ' + crossChecked));
  if (gateText) el('health-success-legend').appendChild(document.createTextNode(' · gates: ' + gateText));

  const archived = caps.filter(c => c.kind === 'archive').length;
  const scheduled = caps.filter(c => c.kind !== 'archive' && c.target).length;
  const blind = caps.length - scheduled - archived;
  hbar(el('health-origin-bar'), [
    { label: 'scheduled target', count: scheduled, tone: 'accent' },
    { label: 'archive import', count: archived, tone: 'dim' },
    { label: 'blind scan', count: blind, tone: 'dim' },
  ]);
  hlegend(el('health-origin-legend'), [
    { label: 'scheduled target', count: scheduled, tone: 'accent' },
    { label: 'archive import', count: archived, tone: 'dim' },
    { label: 'blind scan', count: blind, tone: 'dim' },
  ], 'no archived captures yet');

  const entries = (healthSnapshot.ledger && healthSnapshot.ledger.entries) || [];
  const ours = entries.filter(e => e.decoded_by === 'sdrstream').length;
  const feeds = m.feeds || {};
  let text = 'proof ledger: ' + (entries.length
    ? entries.length + (entries.length >= 5000 ? '+' : '') + ' first sightings (' + ours + ' decoded by sdrstream, ' + (entries.length - ours) + ' relayed from providers)'
    : 'no entries yet');
  if (feeds.sources && feeds.sources.length) text += ' · feeds: ' + (feeds.sources_live || 0) + '/' + feeds.sources.length + ' sources live';
  el('health-ledger').textContent = text + ' · ';
  const feedsLink = document.createElement('button');
  feedsLink.className = 'linklike'; feedsLink.textContent = 'open live data'; feedsLink.onclick = () => showView('feeds');
  el('health-ledger').appendChild(feedsLink);

  renderReceiverProof(entries);
}

function renderCaptures(status) {
  const captures = (status.captures || []).slice().reverse();
  const box = el('captures-list');
  const key = captures.map(c => c.path + '|' + c.url + '|' + c.decode + '|' + c.verified + '|' + c.target + '|' + c.gate).join(',');
  if (box.dataset.key !== key) {
    box.dataset.key = key;
    box.textContent = '';
    captures.forEach(c => {
      const card = document.createElement('div'); card.className = 'card';
      const title = document.createElement('div'); title.className = 'card-title';
      const b = document.createElement('b'); b.textContent = c.satellite || 'capture';
      title.appendChild(b);
      const when = document.createElement('span'); when.className = 'muted'; when.textContent = fmtDate(c.start);
      title.appendChild(when);
      card.appendChild(title);
      const meta = document.createElement('div'); meta.className = 'card-meta';
      meta.textContent = shortURL(c.receiver) + ' · ' + c.samples + ' samples · ' + c.rate + ' Hz' + (c.samples && c.rate ? ' · ' + fmtDuration(c.samples / c.rate) : '') + (c.frequency_hz ? ' · ' + mhz(c.frequency_hz) : '') + (c.mode ? ' · ' + c.mode : '');
      card.appendChild(meta);
      const badges = document.createElement('div'); badges.className = 'badge-row';
      const vb = document.createElement('span');
      vb.className = 'badge ' + (c.verified ? 'verified' : 'unverified');
      vb.textContent = c.verified ? (c.crosscheck === 'verified' ? 'verified live' : 'gate passed') : 'unverified';
      if (c.verified) vb.title = 'Go live quality gate passed' + (c.crosscheck === 'verified' ? ' and independently cross-checked' : ' - no independent cross-check recorded');
      else if (c.gate) vb.title = 'quality gate: ' + c.gate;
      badges.appendChild(vb);
      const ob = document.createElement('span');
      if (c.kind === 'archive') {
        ob.className = 'badge archive';
        ob.textContent = 'archive import' + (c.target ? ': ' + c.target : '');
      } else {
        ob.className = 'badge ' + (c.target ? 'scheduled' : 'blind');
        ob.textContent = c.target ? 'scheduled: ' + c.target : 'blind scan';
      }
      badges.appendChild(ob);
      facetChips(c).forEach(ch => badges.appendChild(ch));
      card.appendChild(badges);
      if (c.decode) { const d=document.createElement('div'); d.className='detail'; d.textContent=c.decode; card.appendChild(d); }
      if (c.url) {
        const audio = document.createElement('audio');
        audio.controls = true; audio.preload = 'none'; audio.src = c.url;
        audio.setAttribute('aria-label', 'listen to ' + (c.satellite || 'capture'));
        card.appendChild(audio);
        card.appendChild(recordButtonFor(audio));
        const open = document.createElement('div'); open.style.marginTop = '6px';
        open.appendChild(link(c.url, c.path));
        card.appendChild(open);
      } else {
        const p = document.createElement('div'); p.className = 'card-meta'; p.textContent = c.path;
        card.appendChild(p);
      }
      card.appendChild(captureProof(c));
      box.appendChild(card);
    });
    if (!captures.length) box.innerHTML = '<div class="empty">none yet — run a capture</div>';
  }
  const live = el('live-captures');
  const recent = captures.slice(0, 3);
  if (!recent.length) { live.className = 'empty'; live.textContent = 'none yet'; }
  else {
    live.className = ''; live.textContent = '';
    recent.forEach(c => {
      const line = document.createElement('div');
      line.style.marginBottom = '8px';
      line.appendChild(link(c.url || '#', (c.satellite || 'capture') + ' ' + fmtTime(c.start) + ' (' + c.rate + ' Hz)'));
      live.appendChild(line);
    });
  }
}

/* ---------- satellites ---------- */
async function refreshSats() {
  try {
    const sats = await fetchJSON('/api/satellites.json');
    const body = el('sats').querySelector('tbody');
    body.textContent = '';
    (sats || []).forEach(s => {
      const tr = document.createElement('tr');
      const dl = (s.downlink_hz / 1e6).toFixed(4) + ' MHz';
      [s.name, s.norad_id, dl, s.mode, s.relay || '—', s.status, s.receivers]
        .forEach(v => { const td = document.createElement('td'); td.textContent = v; tr.appendChild(td); });
      if (s.event) {
        const line = document.createElement('div');
        line.className = 'sat-note';
        line.textContent = 'event: ' + s.event + (s.event_downlink_hz ? ' on ' + (s.event_downlink_hz / 1e6).toFixed(4) + ' MHz' : '') +
          (s.event_mode ? ' · ' + s.event_mode : '') + (s.event_end_utc ? ' · ends ' + s.event_end_utc.replace('T', ' ').replace(':00Z', ' UTC') : '');
        tr.children[2].appendChild(line);
      }
      if (s.notes) {
        const td = tr.children[5];
        td.title = s.notes;
        const line = document.createElement('div');
        line.className = 'sat-note';
        line.textContent = s.notes;
        td.appendChild(line);
      }
      tr.className = 'sat-status-' + s.status;
      body.appendChild(tr);
    });
  } catch { /* static list */ }
}

/* ---------- gallery + lightbox ---------- */
let galleryItems = [];
async function refreshGallery() {
  const box = el('gallery');
  try {
    galleryItems = await fetchJSON('/api/gallery.json');
  } catch (err) {
    box.textContent = '';
    const note = document.createElement('div'); note.className = 'empty';
    note.textContent = 'gallery unavailable: ' + err; box.appendChild(note);
    return;
  }
  renderGallery();
}
function renderGallery() {
  const box = el('gallery');
  const items = galleryItems || [];
  const live = items.filter(it => (it.kind === 'image' && it.observation_id === 0) || it.satellite === 'Live reception');
  const satnogs = items.filter(it => !live.includes(it));
  const active = document.querySelector('[data-gallery-kind][aria-selected="true"]')?.dataset.galleryKind || 'live';
  const shown = active === 'live' ? live : satnogs;
  el('gallery-count').textContent = active === 'live' ? live.length + ' gate-passed live decodes' : satnogs.length + ' SatNOGS artefacts';
  box.textContent = '';
  shown.slice(0, 60).forEach(it => {
    const card = document.createElement('div'); card.className = 'gcard';
    const caption = (it.satellite || ('#' + it.observation_id)) + ' · ' + (it.station || 'unknown receiver') + ' · ' + (it.frequency_hz ? mhz(it.frequency_hz) : 'frequency unknown') + ' · ' + fmtDate(it.start) + (it.mode ? ' · ' + it.mode : '');
    if (it.kind === 'image') {
      const img = document.createElement('img'); img.loading = 'lazy'; img.src = it.url; img.alt = it.satellite || 'decoded image';
      card.appendChild(img);
      card.addEventListener('click', () => openLightbox(it.url, caption));
    } else if (it.kind === 'text') {
      const pre = document.createElement('pre'); pre.textContent = it.text || ''; card.appendChild(pre);
    }
    const body = document.createElement('div'); body.className = 'body';
    const name = document.createElement('b'); name.textContent = it.satellite || ('#' + it.observation_id); body.appendChild(name);
    const detail = document.createElement('div'); detail.textContent = caption.slice(caption.indexOf('·') + 1).trim(); body.appendChild(detail);
    card.appendChild(body); box.appendChild(card);
  });
  if (!shown.length) {
    const note = document.createElement('div'); note.className = 'empty';
    note.textContent = active === 'live' ? 'no gate-passed live decodes yet' : 'no SatNOGS artefacts downloaded yet';
    box.appendChild(note);
  }
}
function openLightbox(url, caption) {
  el('lightbox-img').src = url;
  el('lightbox-img').alt = caption;
  el('lightbox-caption').textContent = caption;
  el('lightbox').hidden = false;
  el('lightbox-close').focus();
}
function closeLightbox() { el('lightbox').hidden = true; el('lightbox-img').src = ''; }
el('lightbox-close').addEventListener('click', closeLightbox);
el('lightbox').addEventListener('click', e => { if (e.target === el('lightbox')) closeLightbox(); });
document.addEventListener('keydown', e => { if (e.key === 'Escape') closeLightbox(); });

async function refreshTestFixtures() {
  const box = el('test-fixtures');
  try {
    const files = await fetchJSON('/api/test-fixtures.json');
    box.textContent = '';
    if (!files.length) { box.textContent = 'No test fixtures found.'; return; }
    const table = document.createElement('table');
    table.innerHTML = '<thead><tr><th>fixture</th><th>type</th><th>bytes</th></tr></thead>';
    const body = document.createElement('tbody');
    files.forEach(f => {
      const tr = document.createElement('tr');
      [f.name, f.kind, f.size].forEach(v => { const td = document.createElement('td'); td.textContent = v; tr.appendChild(td); });
      body.appendChild(tr);
    });
    table.appendChild(body); box.appendChild(table);
  } catch (err) { box.textContent = 'Fixture list unavailable: ' + err; }
}
document.querySelectorAll('[data-gallery-kind]').forEach(button => button.addEventListener('click', () => {
  document.querySelectorAll('[data-gallery-kind]').forEach(b => {
    b.setAttribute('aria-selected', String(b === button));
    b.classList.toggle('on', b === button);
  });
  const test = button.dataset.galleryKind === 'test';
  el('gallery').hidden = test; el('test-fixtures').hidden = !test;
  if (test) refreshTestFixtures(); else {el('test-fixtures').textContent='';renderGallery()}
}));

/* ---------- stations ---------- */
let stationRows = [];
let stationLimit = 60;
let stationError = '';
async function refreshStations() {
  try {
    stationRows = await fetchJSON('/api/stations.json') || [];
    stationError = '';
    el('station-count') && (el('station-count').textContent = stationRows.length);
  } catch (err) {
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
  el('station-summary').textContent = stationError
    ? 'directory unavailable — ' + stationError
    : summary + (filtered.length > shown.length ? ' · showing ' + shown.length + ' of ' + filtered.length : '');
  const more = el('station-more');
  more.hidden = filtered.length <= shown.length;
  if (!more.hidden) more.textContent = 'show ' + Math.min(200, filtered.length - shown.length) + ' more of ' + filtered.length;
  const box = el('stations-list');
  box.textContent = '';
  shown.forEach(s => {
    const card = document.createElement('div'); card.className = 'card';
    const title = document.createElement('div'); title.className = 'card-title';
    const b = document.createElement('b'); b.textContent = s.name || shortURL(s.url);
    const kind = document.createElement('span'); kind.className = 'badge'; kind.textContent = s.type;
    title.appendChild(b); title.appendChild(kind);
    card.appendChild(title);
    const meta = document.createElement('div'); meta.className = 'card-meta';
    meta.appendChild(link(s.url, shortURL(s.url)));
    meta.appendChild(document.createTextNode(' · ' + s.latitude.toFixed(3) + ', ' + s.longitude.toFixed(3)));
    card.appendChild(meta);
    const actions = document.createElement('div'); actions.style.marginTop = '6px';
    const onMap = document.createElement('button'); onMap.className = 'linklike'; onMap.textContent = 'show on map';
    onMap.addEventListener('click', () => { showView('map'); focusMapOn(s.latitude, s.longitude, s.name || shortURL(s.url)); });
    actions.appendChild(onMap);
    card.appendChild(actions);
    box.appendChild(card);
  });
  if (!shown.length) {
    const note = document.createElement('div'); note.className = 'empty';
    note.textContent = stationError ? 'directory unavailable, retrying every five minutes'
      : (needle ? 'no station matches the filter' : 'no stations in the directory');
    box.appendChild(note);
  }
}
el('station-filter').addEventListener('input', () => { stationLimit = 60; renderStations(); });
el('station-more').addEventListener('click', () => { stationLimit += 200; renderStations(); });

/* ---------- spectrum + waterfall ---------- */
let monitorState = 'idle';
let lastFrame = null;
function drawSpectrum(bins, centre, span) {
  const canvas = el('spectrum');
  const ctx = canvas.getContext('2d');
  const ratio = window.devicePixelRatio || 1;
  const width = Math.max(320, Math.floor(canvas.clientWidth * ratio));
  const height = Math.max(90, Math.floor(canvas.clientHeight * ratio));
  if (canvas.width !== width || canvas.height !== height) { canvas.width = width; canvas.height = height; }
  const w = canvas.width, h = canvas.height;
  ctx.clearRect(0, 0, w, h);
  if (!bins || bins.length < 2) return;
  const min = Math.min.apply(null, bins), max = Math.max.apply(null, bins);
  const range = Math.max(1, max - min);
  ctx.beginPath();
  ctx.moveTo(0, h);
  bins.forEach((v, i) => ctx.lineTo((i / (bins.length - 1)) * w, h - ((v - min) / range) * (h - 12) - 6));
  ctx.lineTo(w, h); ctx.closePath();
  const gradient = ctx.createLinearGradient(0, 0, 0, h);
  gradient.addColorStop(0, 'rgba(92,200,255,.55)');
  gradient.addColorStop(1, 'rgba(92,200,255,.04)');
  ctx.fillStyle = gradient; ctx.fill();
  ctx.strokeStyle = '#5cc8ff'; ctx.lineWidth = 1;
  ctx.beginPath();
  bins.forEach((v, i) => { const x = (i / (bins.length - 1)) * w, y = h - ((v - min) / range) * (h - 12) - 6; i ? ctx.lineTo(x, y) : ctx.moveTo(x, y); });
  ctx.stroke();
  // Mark the peaks the monitor reports as on air right now.
  (lastLive && lastLive.on_air || []).forEach((p, pi) => {
    if (!span) return;
    const x = ((p.frequency_hz - (centre - span / 2)) / span) * w;
    if (x < 0 || x > w) return;
    ctx.strokeStyle = 'rgba(52,211,153,.9)'; ctx.lineWidth = 2;
    ctx.beginPath(); ctx.moveTo(x, 0); ctx.lineTo(x, h); ctx.stroke();
    ctx.fillStyle = 'rgba(52,211,153,.9)'; ctx.font = (9 * ratio) + 'px ui-monospace, monospace';
    // Stagger labels so close peaks do not print over each other.
    ctx.fillText('ON AIR', Math.min(x + 3, w - 40 * ratio), (10 + (pi % 3) * 9) * ratio);
  });
  // The red VFO cursor: where the user clicked to listen.
  if (lastLive && lastLive.tuned_hz && span) {
    const tx = ((lastLive.tuned_hz - (centre - span / 2)) / span) * w;
    if (tx >= 0 && tx <= w) {
      ctx.strokeStyle = '#f87171'; ctx.lineWidth = Math.max(1, ratio);
      ctx.setLineDash([4 * ratio, 3 * ratio]);
      ctx.beginPath(); ctx.moveTo(tx, 0); ctx.lineTo(tx, h); ctx.stroke();
      ctx.setLineDash([]);
      ctx.fillStyle = '#f87171'; ctx.font = (9 * ratio) + 'px ui-monospace, monospace';
      ctx.fillText(mhz(lastLive.tuned_hz), Math.min(tx + 4, w - 70 * ratio), h - 4 * ratio);
    }
  }
  drawBookmarkFlags(ctx, w, centre, span, ratio);
  el('spec-lo').textContent = mhz(centre - span / 2);
  el('spec-mid').textContent = mhz(centre);
  el('spec-hi').textContent = mhz(centre + span / 2);
}
function waterfallColor(t) {
  // dark blue → blue → cyan → yellow → red
  const stops = [[5,8,14],[15,40,90],[30,120,180],[92,200,255],[251,191,36],[248,113,113]];
  const x = Math.max(0, Math.min(0.999, t)) * (stops.length - 1);
  const i = Math.floor(x), f = x - i;
  const a = stops[i], b = stops[i + 1];
  return [a[0] + (b[0]-a[0])*f | 0, a[1] + (b[1]-a[1])*f | 0, a[2] + (b[2]-a[2])*f | 0];
}
function pushWaterfall(bins) {
  const canvas = el('waterfall');
  const ctx = canvas.getContext('2d');
  const ratio = window.devicePixelRatio || 1;
  const width = Math.max(320, Math.floor(canvas.clientWidth * ratio));
  const height = Math.max(120, Math.floor(canvas.clientHeight * ratio));
  if (canvas.width !== width || canvas.height !== height) {
    canvas.width = width; canvas.height = height;
    ctx.fillStyle = '#05080e'; ctx.fillRect(0, 0, width, height);
  }
  if (!bins || bins.length < 2) return;
  const min = Math.min.apply(null, bins), max = Math.max.apply(null, bins);
  const range = Math.max(1, max - min);
  // scroll down one pixel, draw the new frame at the top
  ctx.drawImage(canvas, 0, 1, width, height - 1, 0, 2, width, height - 1);
  const img = ctx.createImageData(width, 1);
  for (let x = 0; x < width; x++) {
    const v = bins[Math.floor((x / width) * (bins.length - 1))];
    const [r, g, b] = waterfallColor((v - min) / range);
    img.data[x * 4] = r; img.data[x * 4 + 1] = g; img.data[x * 4 + 2] = b; img.data[x * 4 + 3] = 255;
  }
  ctx.putImageData(img, 0, 0);
}
async function refreshSpectrum() {
  try {
    const spec = await fetchJSON('/api/spectrum.json');
    if (spec.available && spec.bins && spec.bins.length) {
      lastFrame = spec;
      drawSpectrum(spec.bins, spec.centre_hz, spec.span_hz);
      pushWaterfall(spec.bins);
      renderBookmarkBar();
      setMonitor('monitoring');
    }
    // The monitor pill is driven by refreshLive (live.json), not by this
    // poller: spectrum can be disabled or slow while the monitor runs fine.
  } catch { /* spectrum unavailable - pill state comes from live.json */ }
}
function setMonitor(state) {
  monitorState = state;
  const pill = el('pill-monitor');
  el('monitor-state').textContent = state;
  pill.classList.toggle('live', state === 'monitoring');
  pill.classList.toggle('idle', state !== 'monitoring');
}

/* ---------- activity ---------- */
let activityItems = [];
let activitySeenKey = null;
let activityKind = '';
function activityKey(a) { return a.started + '|' + a.frequency_hz + '|' + a.kind; }
async function refreshActivity() {
  try {
    const items = await fetchJSON('/api/activity.json');
    activityItems = items || [];
    el('activity-count').textContent = activityItems.length;
    if (activitySeenKey === null && activityItems.length) {
      activitySeenKey = activityKey(activityItems[0]); // no toast storm on first load
    } else {
      for (const a of activityItems) {
        if (activityKey(a) === activitySeenKey) break;
        toast('new ' + (a.kind || 'signal') + ' at ' + mhz(a.frequency_hz), a.kind === 'voice' ? 'voice' : a.kind === 'data' ? 'data' : undefined);
      }
      if (activityItems.length) activitySeenKey = activityKey(activityItems[0]);
    }
    renderActivity();
  } catch (err) {
    const pill = el('activity-count');
    pill.textContent = '!';
    pill.title = 'activity feed unavailable: ' + (err && err.message || err);
  }
}
function activityCard(a, fresh) {
  const card = document.createElement('div');
  card.className = 'card-activity ' + (a.kind || '') + (fresh ? ' fresh' : '');
  const rowEl = document.createElement('div'); rowEl.className = 'row';
  const freq = document.createElement('span'); freq.className = 'freq'; freq.textContent = mhz(a.frequency_hz);
  const badge = document.createElement('span'); badge.className = 'badge ' + (a.kind || ''); badge.textContent = a.kind || 'unknown';
  const time = document.createElement('span'); time.className = 'muted'; time.textContent = relTime(a.started); time.title = fmtDate(a.started);
  const level = document.createElement('span'); level.className = 'muted';
  level.textContent = (a.level_db || 0).toFixed(0) + ' dB' + (a.bandwidth_hz ? ' · ' + (a.bandwidth_hz / 1000).toFixed(0) + ' kHz' : '');
  rowEl.appendChild(freq); rowEl.appendChild(badge); rowEl.appendChild(time); rowEl.appendChild(level);
  card.appendChild(rowEl);
  if (a.mode) {
    const meta = document.createElement('div'); meta.className = 'meta';
    meta.textContent = a.mode + (a.source ? ' · ' + shortURL(a.source) : '');
    card.appendChild(meta);
  }
  if (a.detail) {
    const detail = document.createElement('div'); detail.className = 'detail'; detail.textContent = a.detail;
    card.appendChild(detail);
  }
  return card;
}
let activitySearch = '';
let activityMode = '';
let activityWindowSec = 0;
let activityLimit = 0; // 0 = fall back to settings.pageSize
function activityHaystack(a) {
  return ((a.detail || '') + ' ' + (a.mode || '') + ' ' + (a.kind || '') + ' ' +
    mhz(a.frequency_hz) + ' ' + (a.source || '')).toLowerCase();
}
function filteredActivity() {
  const needle = activitySearch.trim().toLowerCase();
  const cutoff = activityWindowSec ? Date.now() - activityWindowSec * 1000 : 0;
  return activityItems.filter(a => {
    if (activityKind && a.kind !== activityKind) return false;
    if (activityMode && (a.mode || '') !== activityMode) return false;
    if (cutoff && new Date(a.started).getTime() < cutoff) return false;
    if (needle && !activityHaystack(a).includes(needle)) return false;
    return true;
  });
}
function renderModeOptions() {
  const select = el('activity-mode');
  const modes = [...new Set(activityItems.map(a => a.mode).filter(Boolean))].sort();
  const key = modes.join('|');
  if (select.dataset.key === key) return;
  select.dataset.key = key;
  const keep = activityMode;
  select.textContent = '';
  const all = document.createElement('option');
  all.value = ''; all.textContent = 'all decoders';
  select.appendChild(all);
  modes.forEach(m => {
    const opt = document.createElement('option');
    opt.value = m; opt.textContent = m;
    select.appendChild(opt);
  });
  select.value = modes.includes(keep) ? keep : '';
  activityMode = select.value;
}
function renderActivity() {
  renderModeOptions();
  const full = filteredActivity();
  const limit = activityLimit || +settings.pageSize || DEFAULT_SETTINGS.pageSize;
  const feed = el('feed');
  feed.textContent = '';
  if (!full.length) {
    feed.innerHTML = activityItems.length
      ? '<div class="empty">no decode matches the current filters</div>'
      : '<div class="empty">No gate-passed activity yet. Start the Scan view to check public receivers.</div>';
  }
  full.slice(0, limit).forEach(a => feed.appendChild(activityCard(a, false)));
  const more = el('activity-more');
  more.hidden = full.length <= limit;
  if (!more.hidden) more.textContent = 'show ' + Math.min(limit, full.length - limit) + ' more of ' + full.length;
  const active = [];
  if (activityKind) active.push('kind ' + activityKind);
  if (activityMode) active.push('decoder ' + activityMode);
  if (activityWindowSec) active.push('time window');
  if (activitySearch.trim()) active.push('search');
  el('activity-summary').textContent =
    full.length + ' of ' + activityItems.length + ' decodes' + (active.length ? ' · filtered by ' + active.join(', ') : '');
  const over = el('feed-overview');
  over.textContent = '';
  if (!activityItems.length) { over.innerHTML = '<div class="empty">no traffic seen yet</div>'; }
  activityItems.slice(0, 6).forEach(a => over.appendChild(activityCard(a, false)));
  renderDecodes();
}
document.querySelectorAll('[data-kind]').forEach(button => button.addEventListener('click', () => {
  document.querySelectorAll('[data-kind]').forEach(b => b.classList.toggle('on', b === button));
  activityKind = button.dataset.kind;
  activityLimit = 0;
  renderActivity();
}));
el('activity-search').addEventListener('input', () => {
  activitySearch = el('activity-search').value;
  activityLimit = 0;
  renderActivity();
});
el('activity-mode').addEventListener('change', () => {
  activityMode = el('activity-mode').value;
  activityLimit = 0;
  renderActivity();
});
el('activity-window').addEventListener('change', () => {
  activityWindowSec = +el('activity-window').value || 0;
  activityLimit = 0;
  renderActivity();
});
el('activity-more').addEventListener('click', () => {
  activityLimit = (activityLimit || +settings.pageSize || DEFAULT_SETTINGS.pageSize) * 2;
  renderActivity();
});

/* ---------- export ---------- */
function downloadFile(name, text, type) {
  const url = URL.createObjectURL(new Blob([text], { type }));
  const a = document.createElement('a');
  a.href = url; a.download = name;
  document.body.appendChild(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 5000);
}
function exportName(ext) {
  const stamp = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19);
  return 'sdrstream-decodes-' + stamp + '.' + ext;
}
el('activity-export-json').addEventListener('click', () => {
  const rows = filteredActivity();
  downloadFile(exportName('json'), JSON.stringify(rows, null, 2), 'application/json');
  toast('exported ' + rows.length + ' decodes as JSON', undefined, true);
});
const csvCell = v => {
  const s = v === null || v === undefined ? '' : String(v);
  return /[",\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s;
};
el('activity-export-csv').addEventListener('click', () => {
  const rows = filteredActivity();
  const header = ['started', 'ended', 'frequency_hz', 'bandwidth_hz', 'level_db', 'kind', 'mode', 'source', 'duration_sec', 'detail'];
  const lines = [header.join(',')];
  rows.forEach(a => lines.push([
    a.started, a.ended || '', a.frequency_hz, a.bandwidth_hz || '', a.level_db,
    a.kind || '', a.mode || '', a.source || '', a.duration_sec || '', a.detail || '',
  ].map(csvCell).join(',')));
  downloadFile(exportName('csv'), lines.join('\n'), 'text/csv');
  toast('exported ' + rows.length + ' decodes as CSV', undefined, true);
});


/* ---------- live pulse: signal meter, on-air pill, receivers, decodes ---------- */
let lastLive = null;
let scansData = null;
const FAILED_STATE = /refus|error|timeout|block|fail|skip|unreach/i;

async function refreshLive() {
  try {
    const live = await fetchJSON('/api/live.json');
    lastLive = live;
    setMonitor(live && live.available ? 'monitoring' : 'no monitor');
    renderSignal(live);
    renderOnAirPill(live);
    renderReceivers(live);
  } catch { setMonitor('monitor status unavailable'); /* endpoint older than the UI or server busy */ }
}

function renderSignal(live) {
  const meter = el('signal-meter');
  syncMonitorButtons(live);
  if (!live || !live.available) {
    el('signal-receiver').textContent = 'no monitor running';
    el('meter-fill').style.width = '0%';
    meter.setAttribute('aria-valuenow', 0);
    el('signal-level').textContent = '--';
    el('onair-list').innerHTML = '<span class="muted">monitor offline</span>';
    return;
  }
  el('signal-receiver').textContent = (live.label || live.name || 'monitored receiver') + ' · ' + (live.strategy || '');
  const overFloor = Math.max(0, (live.peak_db || 0) - (live.noise_floor_db || 0));
  const pct = Math.min(100, overFloor / 60 * 100);
  const fill = el('meter-fill');
  fill.style.width = pct + '%';
  const hot = live.on_air && live.on_air.length > 0;
  fill.classList.toggle('hot', hot);
  meter.setAttribute('aria-valuenow', Math.round(overFloor));
  el('signal-level').textContent = overFloor.toFixed(1);
  el('signal-floor').textContent = (live.noise_floor_db || 0).toFixed(0);
  el('signal-peak').textContent = (live.peak_db || 0).toFixed(0);
  el('signal-sunit').textContent = sUnit(live.peak_db || -130, live.centre_hz || 0);
  el('tuned-freq').textContent = live.tuned_hz ? mhz(live.tuned_hz) : '--';
  // Peak hold: decays half a dB per one-second poll.
  peakHold = Math.max(overFloor, peakHold - 0.5);
  el('meter-peakhold').style.left = Math.min(100, peakHold / 60 * 100) + '%';
  const thrPct = Math.min(100, (live.threshold_db || 8) / 60 * 100);
  el('meter-threshold').style.left = thrPct + '%';
  el('meter-threshold-label').textContent = '+' + (live.threshold_db || 8) + ' dB';
  const list = el('onair-list');
  list.textContent = '';
  if (!live.spectrum_ok) {
    list.innerHTML = '<span class="muted">this receiver sends no spectrum - listening blind</span>';
  } else if (!hot) {
    list.innerHTML = '<span class="muted">no signal over the threshold right now</span>';
  } else {
    live.on_air.slice(0, 8).forEach(p => {
      const chip = document.createElement('span');
      chip.className = 'onair-chip';
      chip.textContent = mhz(p.frequency_hz) + ' · ' + p.level_db.toFixed(0) + ' dB';
      list.appendChild(chip);
    });
  }
}

function renderOnAirPill(live) {
  const pill = el('pill-onair');
  const hot = live && live.available && live.on_air && live.on_air.length > 0;
  pill.hidden = !hot;
  if (hot) {
    const strongest = live.on_air.slice().sort((a, b) => b.level_db - a.level_db)[0];
    el('onair-text').textContent = 'ON AIR ' + mhz(strongest.frequency_hz);
  }
}

function renderReceivers(live) {
  const grid = el('rx-grid');
  grid.textContent = '';
  // The monitored receiver: a live card with a pulsing dot.
  const card = document.createElement('div');
  card.className = 'rx-card';
  const hot = live && live.available && live.on_air && live.on_air.length > 0;
  const online = live && live.available;
  card.classList.add(hot ? 'onair' : online ? 'online' : 'offline');
  const dot = document.createElement('span'); dot.className = 'rx-dot' + (hot ? ' pulse' : '');
  const name = document.createElement('b');
  name.textContent = online ? (live.label || live.name || 'monitored receiver') : 'monitored receiver';
  const state = document.createElement('span'); state.className = 'rx-state';
  state.textContent = !online ? 'offline' : hot ? 'ON AIR' : 'listening';
  const meta = document.createElement('div'); meta.className = 'card-meta';
  meta.textContent = online
    ? (live.strategy || '') + (live.spectrum_ok ? ' · spectrum live' : ' · no spectrum') + (hot ? ' · ' + live.on_air.length + ' signal' + (live.on_air.length > 1 ? 's' : '') : '')
    : 'no monitor attached';
  const head = document.createElement('div'); head.className = 'card-title';
  head.appendChild(dot); head.appendChild(name); head.appendChild(state);
  card.appendChild(head); card.appendChild(meta);
  grid.appendChild(card);
  // The scanner fleet, aggregated by state, from the same data the scan view polls.
  if (scansData) {
    const items = scansData.stations || [];
    const counts = {};
    items.forEach(x => { const s = x.state || 'idle'; counts[s] = (counts[s] || 0) + 1; });
    const scanning = items.length > 0 || (scansData.active_connections || 0) > 0;
    const agg = document.createElement('div');
    agg.className = 'rx-card' + (scanning ? ' online' : ' offline');
    const adot = document.createElement('span'); adot.className = 'rx-dot' + ((scansData.active_connections || 0) > 0 ? ' pulse' : '');
    const aname = document.createElement('b'); aname.textContent = 'scanner fleet';
    const astate = document.createElement('span'); astate.className = 'rx-state';
    astate.textContent = !scanning ? 'offline' : scansData.paused ? 'paused' : (scansData.active_connections || 0) + ' active';
    const ahead = document.createElement('div'); ahead.className = 'card-title';
    ahead.appendChild(adot); ahead.appendChild(aname); ahead.appendChild(astate);
    agg.appendChild(ahead);
    const ameta = document.createElement('div'); ameta.className = 'card-meta';
    const parts = Object.keys(counts).sort().map(s => s + ' ' + counts[s]);
    ameta.textContent = scanning ? items.length + ' stations' + (parts.length ? ' · ' + parts.join(' · ') : '') : 'no scan state reported';
    agg.appendChild(ameta);
    grid.appendChild(agg);
  }
}

function renderDecodes() {
  const box = el('decode-feed');
  const decodes = (activityItems || []).filter(a => a.detail && a.mode);
  box.textContent = '';
  if (!decodes.length) { box.innerHTML = '<div class="empty">nothing decoded yet</div>'; return; }
  decodes.slice(0, 40).forEach(a => {
    const line = document.createElement('div');
    line.className = 'decode-line ' + (a.kind || '');
    const t = document.createElement('span'); t.className = 'decode-time'; t.textContent = fmtTime(a.started);
    const m = document.createElement('span'); m.className = 'decode-mode'; m.textContent = a.mode;
    const f = document.createElement('span'); f.className = 'decode-freq'; f.textContent = mhz(a.frequency_hz);
    const d = document.createElement('span'); d.className = 'decode-text'; d.textContent = a.detail;
    line.appendChild(t); line.appendChild(m); line.appendChild(f); line.appendChild(d);
    box.appendChild(line);
  });
}


/* ---------- click-to-tune, S-units, bookmarks (HTML5 SDR client parity) ---------- */
let peakHold = 0;
// S-unit estimate: S9 is -73 dBm below 30 MHz and -93 dBm above, 6 dB per
// S-point. Honest estimate: the receiver sends calibrated dB, not dBm at
// the antenna, so the UI labels it "est.".
function sUnit(db, hz) {
  const ref = hz && hz < 30e6 ? -73 : -93;
  const over = db - ref;
  if (over <= -54) return 'S0';
  if (over < 0) return 'S' + Math.max(1, Math.round(9 + over / 6));
  return 'S9+' + Math.round(over);
}
async function tuneAt(hz) {
  if (!hz || hz <= 0) return;
  try {
    const body = new URLSearchParams({ frequency_hz: String(Math.round(hz)) });
    const r = await fetchJSON('/api/tune.json', { method: 'POST', headers: { 'Content-Type': 'application/x-www-form-urlencoded' }, body });
    toast('listening at ' + mhz(r.tuned_hz) + (r.queued ? '' : ' (worker busy)'));
  } catch (e) { toast('tune failed: ' + e); }
}
function canvasFreq(ev, canvas) {
  if (!lastFrame || !lastFrame.span_hz) return 0;
  const rect = canvas.getBoundingClientRect();
  const frac = (ev.clientX - rect.left) / rect.width;
  if (frac < 0 || frac > 1) return 0;
  return lastFrame.centre_hz - lastFrame.span_hz / 2 + frac * lastFrame.span_hz;
}
['spectrum', 'waterfall'].forEach(id => {
  const c = el(id);
  if (!c) return;
  c.style.cursor = 'crosshair';
  c.addEventListener('click', ev => { const hz = canvasFreq(ev, c); if (hz > 0) tuneAt(hz); });
});
// Bookmarks: satellites from the catalogue plus the user's own, kept in
// localStorage. Flags draw along the top of the spectrum; chips under the
// axis tune on tap.
let userBookmarks = [];
try { userBookmarks = JSON.parse(localStorage.getItem('sdrstream-bookmarks') || '[]'); } catch { userBookmarks = []; }
function allBookmarks() {
  const marks = [];
  (latestStatus && latestStatus.satellites || []).forEach(s => { if (s.downlink_hz > 0) marks.push({ name: s.name, hz: s.downlink_hz, kind: 'sat' }); });
  userBookmarks.forEach(b => marks.push({ name: b.name, hz: b.hz, kind: 'user' }));
  return marks;
}
function drawBookmarkFlags(ctx, w, centre, span, ratio) {
  if (!span) return;
  const lo = centre - span / 2;
  ctx.font = (9 * ratio) + 'px ui-monospace, monospace';
  allBookmarks().forEach(b => {
    const x = ((b.hz - lo) / span) * w;
    if (x < 0 || x > w) return;
    ctx.fillStyle = b.kind === 'user' ? '#fbbf24' : 'rgba(125,138,160,.85)';
    ctx.beginPath();
    ctx.moveTo(x, 0); ctx.lineTo(x + 4 * ratio, 6 * ratio); ctx.lineTo(x - 4 * ratio, 6 * ratio);
    ctx.closePath(); ctx.fill();
    const label = String(b.name).slice(0, 12);
    ctx.fillText(label, Math.min(Math.max(x + 5 * ratio, 0), Math.max(0, w - 60 * ratio)), 8 * ratio);
  });
}
function renderBookmarkBar() {
  const bar = el('bookmark-bar');
  if (!bar) return;
  bar.textContent = '';
  if (!lastFrame || !lastFrame.span_hz) return;
  const lo = lastFrame.centre_hz - lastFrame.span_hz / 2, hi = lastFrame.centre_hz + lastFrame.span_hz / 2;
  const inSpan = allBookmarks().filter(b => b.hz >= lo && b.hz <= hi);
  if (!inSpan.length) return;
  inSpan.slice(0, 12).forEach(b => {
    const chip = document.createElement('button');
    chip.className = 'chip bookmark-chip ' + b.kind;
    chip.textContent = String(b.name).slice(0, 16) + ' ' + mhz(b.hz);
    chip.title = 'listen at ' + mhz(b.hz);
    chip.addEventListener('click', () => tuneAt(b.hz));
    bar.appendChild(chip);
  });
}
const bookmarkAdd = el('bookmark-add');
if (bookmarkAdd) bookmarkAdd.addEventListener('click', () => {
  const hz = (lastLive && lastLive.tuned_hz) || (lastFrame && lastFrame.centre_hz) || 0;
  if (!hz) return;
  const name = prompt('Name for bookmark at ' + mhz(hz), '');
  if (name === null) return;
  userBookmarks.push({ name: name.slice(0, 24) || mhz(hz), hz: Math.round(hz) });
  try { localStorage.setItem('sdrstream-bookmarks', JSON.stringify(userBookmarks.slice(-50))); } catch {}
  renderBookmarkBar();
});

/* ---------- scan board ---------- */
let scanLogSince = 0;
let scanPaused = false;
const concurrencyControl = el('scan-concurrency');
let scanCards=40; el('scan-minus').onclick=()=>{concurrencyControl.value=Math.max(1,+concurrencyControl.value-10);concurrencyControl.dispatchEvent(new Event('change'))};el('scan-plus').onclick=()=>{concurrencyControl.value=Math.min(+concurrencyControl.max,+concurrencyControl.value+10);concurrencyControl.dispatchEvent(new Event('change'))};el('scan-more').onclick=()=>{scanCards+=40;refreshScans()};
let scanLogFilter=''; document.querySelectorAll('[data-scan-state]').forEach(b=>b.onclick=()=>{scanLogFilter=b.dataset.scanState;document.querySelectorAll('[data-scan-state]').forEach(x=>x.classList.toggle('on',x===b));});
concurrencyControl.addEventListener('input', () => { el('scan-concurrency-value').textContent = concurrencyControl.value; });
concurrencyControl.addEventListener('change', async () => {
  try { const body = new URLSearchParams({ concurrency: concurrencyControl.value }); await fetchJSON('/api/scans/control', { method: 'POST', headers: { 'Content-Type': 'application/x-www-form-urlencoded' }, body }); el('scan-control-status').textContent = 'applied'; }
  catch { el('scan-control-status').textContent = 'control failed'; }
});
el('scan-pause').addEventListener('click', async () => {
  scanPaused = !scanPaused;
  try { const body = new URLSearchParams({ paused: String(scanPaused) }); await fetchJSON('/api/scans/control', { method: 'POST', headers: { 'Content-Type': 'application/x-www-form-urlencoded' }, body }); el('scan-pause').textContent = scanPaused ? 'Resume' : 'Pause'; }
  catch { scanPaused = !scanPaused; el('scan-control-status').textContent = 'control failed'; }
});
async function refreshScanLog() {
  try {
    const data = await fetchJSON('/api/scanlog.json?since=' + scanLogSince);
    const box = el('scan-log');
    const items = data.items || [];
    if (!items.length && scanLogSince === 0) { box.innerHTML = '<div class="empty">waiting for scan events</div>'; return; }
    items.filter((x, i) => (i === 0 || x.state !== items[i - 1].state || x.reason !== items[i - 1].reason) && (!scanLogFilter || x.state===scanLogFilter)).forEach(x => {
      scanLogSince = Math.max(scanLogSince, x.id || 0);
      const row = document.createElement('div'); row.className = 'card';
      const title = document.createElement('div'); title.className = 'card-title';
      const tm = document.createElement('span'); tm.textContent = fmtDate(x.updated_at);
      const station = document.createElement('b'); station.textContent = String(x.station||'').replace(/[<>\u0000-\u001f]/g,'').slice(0,200);
      const state = document.createElement('span'); state.className = 'scan-state ' + x.state; state.textContent = x.state;
      title.append(tm, station, state); row.appendChild(title);
      const meta = document.createElement('div'); meta.className = 'card-meta'; meta.textContent = mhz(x.frequency_hz) + ' · ' + (x.mode || '—') + ' · ' + ({decoded:'verified decode',timeout:'no response (timeout)',error:'connection issue',connecting:'connecting',listening:'listening',tuning:'tuning'}[x.state]||x.state); row.appendChild(meta);
      if(x.reason){const details=document.createElement('details');const summary=document.createElement('summary');summary.textContent='technical detail';const raw=document.createElement('pre');raw.textContent=x.reason;details.append(summary,raw);row.append(details)}
      box.prepend(row);
    });
    while (box.children.length > 100) box.lastElementChild.remove();
    scanLogSince = Math.max(scanLogSince, data.next_since || 0);
  } catch { /* auth or scan endpoint temporarily unavailable */ }
}
async function refreshScans() {
  refreshScanLog();
  try {
    const data = await fetchJSON('/api/scans.json');
    scansData = data;
    if (data.target_concurrency) { concurrencyControl.value = data.target_concurrency; el('scan-concurrency-value').textContent = data.target_concurrency; }
    el('scan-ramp-value').textContent=(data.ramp_step||0)+'/'+(data.max_connections||0);
    scanPaused = !!data.paused; el('scan-pause').textContent = scanPaused ? 'Resume' : 'Pause';
    // Failing, refused and blocked stations are dropped from the board: the
    // board shows what works, the dropped section below explains the rest.
    const items = (data.stations || []).filter(x => !FAILED_STATE.test(x.state || ''));
    const dropped = (data.stations || []).filter(x => FAILED_STATE.test(x.state || ''));
    (data.dropped || []).forEach(x => dropped.push({ station: x.station, state: 'blocked', result: x.reason, blocked_until: x.until }));
    const box = el('scan-board');
    box.textContent = '';
    const summary = document.createElement('div'); summary.className = 'card';
    summary.textContent = 'ramp '+(data.ramp_step||0)+'/'+(data.max_connections||0)+' · active '+(data.active_connections||0)+' · refusals '+(data.refusals||0)+' · timeouts '+(data.timeouts||0)+' · load '+Number(data.load_average||0).toFixed(2)+'/'+(data.core_count||0)+' cores · est. '+Number(data.estimated_bandwidth_mbps||0).toFixed(1)+'/'+(data.bandwidth_limit_mbps||0)+' Mbps';
    box.appendChild(summary);
    if (!items.length) { box.appendChild(Object.assign(document.createElement('div'), {className:'empty', textContent:'no healthy stations right now'})); }
    items.slice(-scanCards).forEach(x => {
      const card = document.createElement('div'); card.className = 'card';
      const title = document.createElement('div'); title.className = 'card-title';
      const b = document.createElement('b'); b.textContent = String(x.station||'').replace(/[<>\u0000-\u001f]/g,'').slice(0,200);
      const state = document.createElement('span'); state.className = 'scan-state ' + (x.state || 'idle'); state.textContent = x.state || 'idle';
      title.appendChild(b); title.appendChild(state);
      card.appendChild(title);
      const meta = document.createElement('div'); meta.className = 'card-meta';
      meta.textContent = mhz(x.frequency_hz) + ' · ' + (x.mode || '—') + (x.band_condition ? ' · ' + x.band_condition + ' (advisory)' : '') + (x.result ? ' · ' + x.result : '');
      card.appendChild(meta);
      box.appendChild(card);
    });
    renderDropped(dropped);
  } catch {
    el('scan-board').innerHTML = '<div class="empty">scanner offline</div>';
  }
}

// Dropped stations: collapsed by default with count and reasons visible;
// the full list expands on tap. blocked_until comes from the scanner's
// retry-after blocklist (integrator contract, feature/ui-live).
let droppedOpen = false;
function renderDropped(dropped) {
  const bar = el('dropped-bar'), list = el('scan-dropped');
  bar.hidden = dropped.length === 0;
  el('dropped-count').textContent = dropped.length;
  const reasons = {};
  dropped.forEach(x => {
    const r = String(x.state || 'failed').replace(/\/.+$/, '');
    reasons[r] = (reasons[r] || 0) + 1;
  });
  el('dropped-summary').textContent = Object.keys(reasons).sort().map(r => r + ' ' + reasons[r]).join(' · ');
  if (!droppedOpen) { list.hidden = true; return; }
  list.hidden = false;
  list.textContent = '';
  dropped.slice(-80).forEach(x => {
    const card = document.createElement('div'); card.className = 'card dropped';
    const title = document.createElement('div'); title.className = 'card-title';
    const b = document.createElement('b'); b.textContent = String(x.station||'').replace(/[<>\u0000-\u001f]/g,'').slice(0,200);
    const state = document.createElement('span'); state.className = 'scan-state dropped'; state.textContent = x.state || 'failed';
    title.appendChild(b); title.appendChild(state);
    card.appendChild(title);
    if (x.result) { const meta = document.createElement('div'); meta.className = 'card-meta'; meta.textContent = String(x.result).slice(0, 300); card.appendChild(meta); }
    if (x.blocked_until) { const retry = document.createElement('div'); retry.className = 'card-meta muted'; retry.textContent = 'retry after ' + fmtDate(x.blocked_until); card.appendChild(retry); }
    list.appendChild(card);
  });
}
document.getElementById('dropped-toggle').addEventListener('click', () => {
  droppedOpen = !droppedOpen;
  const t = document.getElementById('dropped-toggle');
  t.setAttribute('aria-expanded', String(droppedOpen));
  t.innerHTML = (droppedOpen ? '&#9662; dropped stations (' : '&#9656; dropped stations (') + el('dropped-count').textContent + ')';
  if (scansData) {
    const dropped = (scansData.stations || []).filter(x => FAILED_STATE.test(x.state || ''));
    (scansData.dropped || []).forEach(x => dropped.push({ station: x.station, state: 'blocked', result: x.reason, blocked_until: x.until }));
    renderDropped(dropped);
  }
});

/* ---------- map ---------- */
let map = null;
let mapLayers = {};
let mapDataTimer = null;
const QO100 = { name: 'QO-100', lat: 0, lon: 25.9, note: 'Geostationary at 25.9°E — always-on public WebSDR' };
function tileLayerWithFallback() {
  const Grid = L.GridLayer.extend({
    createTile: function (coords) {
      const tile = document.createElement('canvas');
      const size = this.getTileSize();
      tile.width = size.x; tile.height = size.y;
      const ctx = tile.getContext('2d');
      ctx.fillStyle = '#05080e'; ctx.fillRect(0, 0, size.x, size.y);
      const img = new Image();
      img.onload = () => ctx.drawImage(img, 0, 0, size.x, size.y);
      img.onerror = () => drawGraticule(ctx, coords, size);
      img.src = '/api/tiles/' + coords.z + '/' + coords.x + '/' + coords.y + '.png';
      return tile;
    }
  });
  return new Grid({ maxZoom: 12, minZoom: 2, attribution: '© OpenStreetMap contributors' });
}
function drawGraticule(ctx, coords, size) {
  // offline fallback: a dark tile with a lat/lon graticule
  const n = Math.pow(2, coords.z);
  const lonW = coords.x / n * 360 - 180, lonE = (coords.x + 1) / n * 360 - 180;
  const latN = Math.atan(Math.sinh(Math.PI * (1 - 2 * coords.y / n))) * 180 / Math.PI;
  const latS = Math.atan(Math.sinh(Math.PI * (1 - 2 * (coords.y + 1) / n))) * 180 / Math.PI;
  ctx.strokeStyle = 'rgba(92,200,255,.14)'; ctx.lineWidth = 1;
  ctx.fillStyle = 'rgba(125,138,160,.7)'; ctx.font = '10px ui-monospace, monospace';
  const step = Math.max(5, Math.round((lonE - lonW) / 4 / 5) * 5);
  for (let lon = Math.ceil(lonW / step) * step; lon < lonE; lon += step) {
    const x = (lon - lonW) / (lonE - lonW) * size.x;
    ctx.beginPath(); ctx.moveTo(x, 0); ctx.lineTo(x, size.y); ctx.stroke();
    ctx.fillText(lon + '°', x + 3, size.y - 5);
  }
  for (let lat = Math.ceil(latS / step) * step; lat < latN; lat += step) {
    const y = (1 - (lat - latS) / (latN - latS)) * size.y;
    ctx.beginPath(); ctx.moveTo(0, y); ctx.lineTo(size.x, y); ctx.stroke();
    ctx.fillText(lat + '°', 3, y - 3);
  }
}
function splitTrack(track) {
  // split a ground track at the antimeridian so polylines do not wrap the
  // long way around the world
  const parts = [[]];
  track.forEach(pt => {
    const last = parts[parts.length - 1];
    if (last.length && Math.abs(pt[1] - last[last.length - 1][1]) > 180) parts.push([]);
    parts[parts.length - 1].push(pt);
  });
  return parts.filter(p => p.length > 1);
}
function focusMapOn(lat, lon, label) {
  if (!map) { pendingFocus = { lat, lon, label }; return; }
  map.setView([lat, lon], 6);
  L.popup().setLatLng([lat, lon]).setContent(label).openOn(map);
}
let pendingFocus = null;
function initMap() {
  if (map) { setTimeout(() => map.invalidateSize(), 60); return; }
  map = L.map('map-canvas', { center: [30, 10], zoom: 2, minZoom: 2, worldCopyJump: true, zoomControl: true });
  map.addLayer(tileLayerWithFallback());
  mapLayers = {
    receivers: L.layerGroup().addTo(map),
    stations: L.layerGroup().addTo(map),
    satellites: L.layerGroup().addTo(map),
    activity: L.layerGroup().addTo(map),
  };
  if (pendingFocus) { const f = pendingFocus; pendingFocus = null; focusMapOn(f.lat, f.lon, f.label); }
  refreshMapData();
  refreshMapStations();
  mapDataTimer = setInterval(refreshMapData, 60000);
}
document.querySelectorAll('[data-layer]').forEach(button => button.addEventListener('click', () => {
  if (!map) return;
  const layer = mapLayers[button.dataset.layer];
  const on = !button.classList.contains('on');
  button.classList.toggle('on', on);
  if (on) layer.addTo(map); else map.removeLayer(layer);
}));
async function refreshMapStations() {
  try {
    const stations = await fetchJSON('/api/stations.json');
    const layer = mapLayers.stations;
    layer.clearLayers();
    const renderer = L.canvas({ padding: 0.5 });
    (stations || []).slice(0, 1500).forEach(s => {
      if (!s.latitude && !s.longitude) return;
      L.circleMarker([s.latitude, s.longitude], {
        renderer, radius: 3, color: '#7d8aa0', weight: 1, fillColor: '#7d8aa0', fillOpacity: 0.5,
      }).bindPopup('<b>' + escapeHTML(s.name || s.url) + '</b><br>' + escapeHTML(s.type) + '<br><a href="' + safeURL(s.url) + '" target="_blank" rel="noopener noreferrer">' + escapeHTML(shortURL(s.url)) + '</a>')
        .addTo(layer);
    });
  } catch { /* directory offline */ }
}
function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
async function refreshMapData() {
  try {
    const data = await fetchJSON('/api/map.json');
    el('map-note').textContent = data.satellites_available
      ? (data.satellites || []).length + ' satellites tracked · updated ' + fmtTime(data.generated_at)
      : 'satellite positions unavailable (no TLE) — showing receivers only';
    const rl = mapLayers.receivers, sl = mapLayers.satellites, al = mapLayers.activity;
    rl.clearLayers(); sl.clearLayers(); al.clearLayers();
    (data.receivers || []).forEach(r => {
      L.circleMarker([r.latitude, r.longitude], {
        radius: 6, color: '#5cc8ff', weight: 2, fillColor: '#5cc8ff', fillOpacity: 0.55,
      }).bindPopup('<b>' + escapeHTML(shortURL(r.url)) + '</b><br>' + escapeHTML(r.kind) +
        (r.satellite_bands && r.satellite_bands.length ? '<br>bands: ' + escapeHTML(r.satellite_bands.join(', ')) : '') +
        '<br><a href="' + safeURL(r.url) + '" target="_blank" rel="noopener noreferrer">open receiver</a>')
        .addTo(rl);
    });
    const satPos = {};
    (data.satellites || []).forEach(sat => {
      satPos[sat.name] = [sat.latitude, sat.longitude];
      const isISS = sat.norad_id === 25544;
      const icon = L.divIcon({
        className: '', iconSize: [60, 26], iconAnchor: [30, 8],
        html: '<div class="sat-marker' + (isISS ? ' iss' : '') + '"><div class="sat-dot"></div><div class="sat-name">' + escapeHTML(sat.name) + '</div></div>',
      });
      L.marker([sat.latitude, sat.longitude], { icon })
        .bindPopup('<b>' + escapeHTML(sat.name) + '</b><br>' + (sat.downlink_hz / 1e6).toFixed(3) + ' MHz ' + escapeHTML(sat.mode || '') +
          '<br>alt ' + (sat.altitude_km || 0).toFixed(0) + ' km')
        .addTo(sl);
      splitTrack(sat.track || []).forEach(part => {
        L.polyline(part, { color: isISS ? '#5cc8ff' : '#fbbf24', weight: 1.2, opacity: 0.5 }).addTo(sl);
      });
    });
    // QO-100: geostationary, no TLE needed - fixed marker with its public WebSDR
    const qo = L.divIcon({
      className: '', iconSize: [60, 26], iconAnchor: [30, 8],
      html: '<div class="sat-marker"><div class="sat-dot" style="background:#34d399;box-shadow:0 0 8px rgba(52,211,153,.8)"></div><div class="sat-name">QO-100</div></div>',
    });
    L.marker([QO100.lat, QO100.lon], { icon: qo })
      .bindPopup('<b>QO-100</b><br>' + QO100.note + '<br><a href="https://eshail.batc.org.uk/nb/" target="_blank" rel="noopener noreferrer">Es\'hail-2 WebSDR</a>')
      .addTo(sl);
    // passes in progress: satellite-to-receiver path
    (data.passes || []).filter(p => p.active).forEach(p => {
      const pos = satPos[p.satellite];
      if (!pos || (!p.receiver_latitude && !p.receiver_longitude)) return;
      L.polyline([pos, [p.receiver_latitude, p.receiver_longitude]], { color: '#34d399', weight: 1.6, opacity: 0.8, dashArray: '6 5' }).addTo(sl);
    });
    // live activity at the source receiver's coordinates
    const coordsByURL = {};
    (data.receivers || []).forEach(r => coordsByURL[r.url] = [r.latitude, r.longitude]);
    const cutoff = Date.now() - 15 * 60 * 1000;
    (activityItems || []).filter(a => a.source && coordsByURL[a.source] && new Date(a.started) >= cutoff).slice(0, 20).forEach(a => {
      const icon = L.divIcon({
        className: '', iconSize: [70, 24], iconAnchor: [35, 8],
        html: '<div class="sat-marker"><div class="sat-dot" style="background:#c084fc;box-shadow:0 0 8px rgba(192,132,252,.8)"></div><div class="sat-name">' + escapeHTML(a.kind || 'sig') + ' ' + (a.frequency_hz / 1e6).toFixed(2) + '</div></div>',
      });
      L.marker(coordsByURL[a.source], { icon })
        .bindPopup('<b>' + escapeHTML(a.kind || 'signal') + '</b> ' + mhz(a.frequency_hz) + '<br>' + escapeHTML(fmtTime(a.started)) + (a.detail ? '<br>' + escapeHTML(a.detail.slice(0, 200)) : ''))
        .addTo(al);
    });
  } catch (err) {
    el('map-note').textContent = 'map data unavailable: ' + err;
  }
}

/* ---------- live public feeds ---------- */
let feedsSnapshot = null;
let feedsSourceFilter = '';
async function refreshFeeds() { try { feedsSnapshot = await fetchJSON('/api/feeds.json'); renderFeeds(); } catch {} }
function renderFeeds() {
 if (!feedsSnapshot) return;
 const sources = feedsSnapshot.sources || [], items = feedsSnapshot.items || [], box = el('feeds-sources'); box.textContent='';
 renderPublicPanels(sources);
 const chip=(label,value,state,count)=>{const b=document.createElement('button');b.className='chip'+(feedsSourceFilter===value?' on':'');b.textContent=label+(count?' '+count:'');if(state)b.dataset.state=state;b.onclick=()=>{feedsSourceFilter=value;renderFeeds()};box.appendChild(b)};
 chip('all','', '',items.length); sources.forEach(s=>chip(s.name,s.name,s.state,s.items));
 const staleSrc=sources.filter(s=>s.state==='live'&&s.refresh_seconds&&s.last_ok&&!s.last_ok.startsWith('0001')&&Date.now()-new Date(s.last_ok).getTime()>3*s.refresh_seconds*1000);
 const note=el('feeds-note'); note.textContent='updated '+relTime(feedsSnapshot.updated_at)+staleSrc.map(s=>' · '+s.name+': STALE, last fetch '+relTime(s.last_ok)).join('')+sources.filter(s=>s.state==='disabled'||s.state==='error').map(s=>' · '+s.name+': '+(s.last_error||s.detail||s.state)).join('');
 const list=el('feeds-list'); list.textContent=''; const shown=items.filter(i=>!feedsSourceFilter||i.source===feedsSourceFilter);
 if(!shown.length){list.innerHTML='<div class="empty">no live data yet - feeds are connecting</div>';return}
 shown.slice(0,120).forEach(it=>{const c=document.createElement('div');c.className='card-activity feed-'+(it.kind||'data');const r=document.createElement('div');r.className='row';const label=document.createElement('span');label.className='freq';label.textContent=it.label;const badge=document.createElement('span');badge.className='badge feed-src';badge.textContent=it.source;const t=document.createElement('span');t.className='muted';t.textContent=relTime(it.time);r.append(label,badge,t);if(it.ref){const a=document.createElement('a');a.href=safeURL(it.ref);a.target='_blank';a.rel='noopener noreferrer';a.textContent='source';r.append(a)}const pb=document.createElement('button');pb.className='chip';pb.textContent='proof';const pf=document.createElement('div');pf.className='meta';pf.hidden=true;pf.textContent='provider time '+fmtDate(it.time)+' · fetched '+(it.fetched_at?fmtDate(it.fetched_at):'unknown')+' · decoded by '+(it.decoded_by||'provider')+' · source '+it.source+(((feedsSnapshot.sources||[]).find(x=>x.name===it.source)||{}).license?' · licence '+(feedsSnapshot.sources.find(x=>x.name===it.source).license):'');if(it.receiver)pf.textContent+=' · receiver '+it.receiver;if(it.ref&&it.ref.indexOf('/audio/')===0){const au=document.createElement('audio');au.className='proof-audio';au.controls=true;au.preload='none';au.src=it.ref;pf.appendChild(au)};pb.onclick=()=>{pf.hidden=!pf.hidden};r.append(pb);c.appendChild(r);c.appendChild(pf);if(it.detail){const d=document.createElement('div');d.className='detail';d.textContent=it.detail;c.appendChild(d)}if(it.has_pos){const m=document.createElement('div');m.className='meta';m.textContent=it.lat.toFixed(3)+', '+it.lon.toFixed(3);c.appendChild(m)}list.appendChild(c)});
}

function renderPublicPanels(sources) {
 const container=el('public-feed-panels'); container.textContent='';
 const names={'noaa-swpc':'Space weather','wspr.live':'WSPR spots','sondehub':'Radiosondes','digitraffic':'Baltic vessels','adsb.lol':'Nearby aircraft'};
 const maxAge={'noaa-swpc':12*3600,'wspr.live':30*60,'sondehub':2*3600,'digitraffic':30*60,'adsb.lol':120};
 Object.entries(names).forEach(([id,title])=>{
  const source=sources.find(s=>s.name===id), card=document.createElement('article');card.className='panel public-feed-panel';card.dataset.source=id;
  const head=document.createElement('div');head.className='panel-head';const h=document.createElement('h2');h.textContent=title;
  const badge=document.createElement('span');badge.className='badge';
  const samples=(source?.samples||[]).filter(i=>Date.now()-new Date(i.time).getTime()<=maxAge[id]*1000);
  badge.textContent=!source?'not started':source.state==='live'&&!samples.length?'no recent observations':source.state;badge.dataset.state=source?.state||'disabled';head.append(h,badge);card.append(head);
  const meta=document.createElement('p');meta.className='muted';meta.textContent=source ? (source.last_ok&&!source.last_ok.startsWith('0001')?'Fetched '+relTime(source.last_ok):'Waiting for first fetch')+(source.refresh_seconds?' · cache '+source.refresh_seconds+'s':' · streaming')+' · '+source.items+' cached observations' : 'Feed is disabled or not attached to this server';card.append(meta);
  if(source?.detail){const d=document.createElement('p');d.className='muted';d.textContent=source.detail;card.append(d)}
  if(source?.last_error){const err=document.createElement('p');err.className='feed-error';err.textContent=source.last_error+' · showing last good observations where available';card.append(err)}
  samples.slice(0,3).forEach(it=>{const row=document.createElement('div');row.className='public-feed-sample';const name=document.createElement('b');name.textContent=it.label;const detail=document.createElement('div');detail.textContent=it.detail||'';const time=document.createElement('small');time.className='muted';time.textContent='Observed '+fmtDate(it.time)+' · '+relTime(it.time)+(it.has_pos?' · '+it.lat.toFixed(3)+', '+it.lon.toFixed(3):'');row.append(name,detail,time);card.append(row)});
  if(!samples.length){const empty=document.createElement('p');empty.className='muted';empty.textContent='No recent observations in the cache. Empty results do not mean there is no activity.';card.append(empty)}
  if(source?.detail_url){const credit=document.createElement('div');credit.className='public-feed-credit';credit.append(link(source.detail_url,source.attribution||id));card.append(credit)}
  if(source){const tm=document.createElement('p');tm.className='muted terms';tm.textContent='Licence: '+(source.license||'not recorded')+(source.max_age_seconds?' · kept '+Math.round(source.max_age_seconds/60)+' min':'')+(source.terms_notes?' · '+source.terms_notes:'');card.append(tm)}
  container.append(card);
 });
}

/* ---------- data source health (on demand) ---------- */
let sourcesPollTimer = null;
async function refreshSources(force) {
  try {
    const data = await fetchJSON('/api/sources.json' + (force ? '?refresh=1' : ''));
    renderSources(data);
    if (sourcesPollTimer) { clearTimeout(sourcesPollTimer); sourcesPollTimer = null; }
    if (data.running) sourcesPollTimer = setTimeout(() => refreshSources(false), 3000);
  } catch {}
}
function renderSources(data) {
  const note = el('sources-note');
  note.textContent = (data.running ? 'checking... ' : '') + (data.updated_at ? 'last check ' + relTime(data.updated_at) : '');
  const list = el('sources-list'); list.textContent = '';
  const results = data.results || [];
  if (!results.length) { list.innerHTML = '<div class="empty">no health check yet - press check now</div>'; return; }
  results.forEach(r => {
    const card = document.createElement('div'); card.className = 'card';
    const title = document.createElement('div'); title.className = 'card-title';
    const name = document.createElement('b'); name.textContent = r.name;
    const status = document.createElement('span'); status.className = 'src-status src-' + r.status; status.textContent = r.status;
    const kind = document.createElement('span'); kind.className = 'muted'; kind.textContent = r.kind + ' · ' + (r.latency_ms || 0) + 'ms';
    title.append(name, status, kind); card.appendChild(title);
    const detail = [r.detail, r.note].filter(Boolean).join(' - ');
    if (detail) { const d = document.createElement('div'); d.className = 'src-note'; d.textContent = detail; card.appendChild(d); }
    list.appendChild(card);
  });
}

/* ---------- advisory HF band conditions ---------- */
async function refreshBandCond() {
  try {
    const data = await fetchJSON('/api/bandconditions.json');
    const box = el('band-conditions'); box.textContent = '';
    if (data.error && !(data.bands || []).length) { box.textContent = 'band conditions unavailable: ' + data.error; return; }
    const head = document.createElement('span');
    head.textContent = 'HF band conditions (advisory, from NOAA K-index): Kp ' + Number(data.kp).toFixed(1) + ' ' + (data.geomag || '') + (data.stale ? ' - stale data' : '') + ' ';
    box.appendChild(head);
    (data.bands || []).forEach(b => {
      const chip = document.createElement('span');
      chip.className = 'bc-chip bc-' + b.label;
      chip.textContent = b.band + ' ' + b.label;
      chip.title = b.note;
      box.appendChild(chip);
    });
  } catch {}
}

/* ---------- receiver panel: mode / step / squelch ---------- */
const RX_MODES = ['auto', 'nfm', 'fm', 'wfm', 'am', 'usb', 'lsb', 'cw'];
const RX_STEPS = [1000, 2500, 5000, 10000, 12500, 25000, 50000, 100000];
const fmtStep = hz => hz >= 1000 ? (hz / 1000) + 'k' : hz + '';
let rxState = { mode: '', step_hz: 0, threshold_db: 0 };
let rxPostTimer = null;

function buildRxControls() {
  const modes = el('rx-modes');
  RX_MODES.forEach(m => {
    const b = document.createElement('button');
    b.type = 'button'; b.className = 'seg-btn'; b.textContent = m; b.dataset.mode = m;
    b.addEventListener('click', () => postMonitorControl({ mode: m }));
    modes.appendChild(b);
  });
  const steps = el('rx-steps');
  RX_STEPS.forEach(s => {
    const b = document.createElement('button');
    b.type = 'button'; b.className = 'seg-btn'; b.textContent = fmtStep(s); b.dataset.step = s;
    b.title = s / 1000 + ' kHz dial step';
    b.addEventListener('click', () => postMonitorControl({ step_hz: s }));
    steps.appendChild(b);
  });
  const squelch = el('squelch');
  squelch.addEventListener('input', () => {
    el('squelch-value').textContent = '+' + squelch.value + ' dB';
    // Debounce: a dragged slider would otherwise POST dozens of times.
    clearTimeout(rxPostTimer);
    rxPostTimer = setTimeout(() => postMonitorControl({ threshold_db: +squelch.value }), 250);
  });
}
async function postMonitorControl(fields) {
  try {
    const body = new URLSearchParams(fields);
    const info = await fetchJSON('/api/monitor/control', {
      method: 'POST', headers: { 'Content-Type': 'application/x-www-form-urlencoded' }, body,
    });
    applyRxState(info);
    toast('receiver updated', undefined, false);
  } catch (err) {
    toast('receiver control failed: ' + err, undefined, true);
    refreshReceiverPanel();
  }
}

/* ---------- monitor start / stop ---------- */
// Starts and stops the live monitor the panels above read from. The
// server answers construction failures (bad address, refused
// connection) with an error we surface in place, so an empty spectrum
// never reads as success.
async function postMonitorLifecycle(fields) {
  const body = new URLSearchParams(fields);
  const response = await fetch('/api/monitor/control', {
    method: 'POST', credentials: 'same-origin',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' }, body,
  });
  if (!response.ok) {
    let detail = '';
    try { detail = (await response.text()).trim(); } catch { /* keep status */ }
    throw new Error(detail || ('HTTP ' + response.status));
  }
  return response.json();
}

function initMonitorLifecycle() {
  const host = el('monitor-host');
  const kindSel = el('monitor-kind');
  const profile = el('monitor-profile');
  const startBtn = el('monitor-start');
  const stopBtn = el('monitor-stop');
  const msg = el('monitor-msg');
  if (!host || !startBtn || !stopBtn) return;
  startBtn.addEventListener('click', async () => {
    const server = host.value.trim();
    if (!server) { msg.textContent = 'enter a receiver host[:port]'; return; }
    msg.textContent = 'starting...';
    startBtn.disabled = true;
    try {
      const startMHz = parseFloat(el('monitor-start-mhz').value);
      const endMHz = parseFloat(el('monitor-end-mhz').value);
      const fields = { action: 'start', server, kind: kindSel.value, profile: profile.value.trim() };
      if (startMHz > 0) fields.start_hz = Math.round(startMHz * 1e6);
      if (endMHz > 0) fields.end_hz = Math.round(endMHz * 1e6);
      await postMonitorLifecycle(fields);
      msg.textContent = 'monitor starting';
    } catch (err) {
      msg.textContent = 'start failed: ' + err.message;
    } finally {
      startBtn.disabled = false;
      refreshLive();
    }
  });
  stopBtn.addEventListener('click', async () => {
    msg.textContent = 'stopping...';
    try {
      await postMonitorLifecycle({ action: 'stop' });
      msg.textContent = 'monitor stopped';
    } catch (err) {
      msg.textContent = 'stop failed: ' + err.message;
    }
    refreshLive();
  });
  // Offer known public receivers from the station directory.
  fetchJSON('/api/stations.json').then(stations => {
    const list = el('monitor-stations');
    if (!list) return;
    (stations || []).slice(0, 300).forEach(st => {
      if (!st.url) return;
      const opt = document.createElement('option');
      try {
        const u = new URL(/^https?:\/\//.test(st.url) ? st.url : 'http://' + st.url);
        opt.value = u.host;
      } catch { opt.value = st.url; }
      opt.label = (st.name || '') + (st.type ? ' (' + st.type + ')' : '');
      list.appendChild(opt);
    });
  }).catch(() => { /* directory offline: free text still works */ });
}

function syncMonitorButtons(live) {
  const startBtn = el('monitor-start');
  const stopBtn = el('monitor-stop');
  if (!startBtn || !stopBtn) return;
  const running = !!(live && live.available);
  startBtn.hidden = running;
  stopBtn.hidden = !running;
}

function applyRxState(info) {
  if (!info) return;
  if (info.mode) rxState.mode = info.mode;
  if (info.step_hz) rxState.step_hz = info.step_hz;
  if (info.threshold_db) rxState.threshold_db = info.threshold_db;
  document.querySelectorAll('#rx-modes .seg-btn').forEach(b => b.classList.toggle('on', b.dataset.mode === rxState.mode));
  document.querySelectorAll('#rx-steps .seg-btn').forEach(b => b.classList.toggle('on', +b.dataset.step === rxState.step_hz));
  if (rxState.threshold_db) {
    el('squelch').value = rxState.threshold_db;
    el('squelch-value').textContent = '+' + rxState.threshold_db + ' dB';
  }
}
async function refreshReceiverPanel() {
  const panel = el('panel-receiver');
  try {
    const live = await fetchJSON('/api/live.json');
    if (!live || !live.available) {
      el('rx-offline').textContent = 'monitor offline';
      el('freq-display').textContent = '—';
      el('rx-sub').textContent = '';
      el('rx-bw').textContent = '';
      panel.classList.add('offline');
      return;
    }
    panel.classList.remove('offline');
    el('rx-offline').textContent = (live.label || live.name || 'monitored receiver');
    const fd = el('freq-display');
    fd.textContent = '';
    if (live.spectrum_ok && live.centre_hz) {
      const mhzStr = (live.centre_hz / 1e6).toFixed(5);
      mhzStr.split('').forEach(ch => {
        const d = document.createElement('span');
        d.className = 'digit' + (ch === '.' ? ' dot' : '');
        d.textContent = ch;
        fd.appendChild(d);
      });
      const unit = document.createElement('span');
      unit.className = 'freq-unit'; unit.textContent = 'MHz';
      fd.appendChild(unit);
      el('rx-sub').textContent = (live.strategy || '') + ' · span ' + ((live.span_hz || 0) / 1e6).toFixed(3) + ' MHz';
    } else {
      fd.textContent = live.strategy || 'listening';
      el('rx-sub').textContent = 'this receiver sends no spectrum - centre frequency unknown';
    }
    applyRxState(live);
    const peaks = (live.on_air || []).slice().sort((a, b) => b.level_db - a.level_db);
    el('rx-bw').textContent = peaks.length
      ? 'strongest signal ' + mhz(peaks[0].frequency_hz) + ' · ' + (peaks[0].bandwidth_hz / 1000).toFixed(1) + ' kHz wide'
      : 'no signal over the squelch right now';
  } catch {
    el('rx-offline').textContent = 'monitor offline';
    panel.classList.add('offline');
  }
}

/* ---------- decoder message panel ---------- */
let msglogMode = '';
let msglogStuck = false; // user scrolled up: do not yank the log to the bottom
function decodedItems() {
  return (activityItems || []).filter(a => a.detail && a.mode)
    .slice().sort((a, b) => new Date(a.started) - new Date(b.started));
}
function renderMsglog() {
  const box = el('msglog');
  const items = decodedItems().filter(a => !msglogMode || a.mode === msglogMode);
  // Rebuild only when the content changed; a rebuilt log loses the scroll spot.
  const key = items.map(a => a.started + a.mode + a.detail).join('|') + '#' + msglogMode;
  if (box.dataset.key === key) return;
  box.dataset.key = key;
  box.textContent = '';
  if (!items.length) {
    box.innerHTML = '<div class="empty">' + (msglogMode ? 'no ' + msglogMode + ' messages' : 'nothing decoded yet') + '</div>';
  }
  items.slice(-300).forEach(a => {
    const line = document.createElement('div');
    line.className = 'msgline ' + (a.kind || '');
    const t = document.createElement('span');
    t.className = 'msg-time'; t.textContent = fmtTime(a.started);
    const m = document.createElement('span');
    m.className = 'msg-mode'; m.textContent = a.mode;
    const f = document.createElement('span');
    f.className = 'msg-freq'; f.textContent = mhz(a.frequency_hz);
    const d = document.createElement('span');
    d.className = 'msg-detail'; d.textContent = a.detail;
    line.append(t, m, f, d);
    box.appendChild(line);
  });
  if (!msglogStuck) box.scrollTop = box.scrollHeight;
  el('msglog-state').textContent = items.length + ' messages' + (msglogMode ? ' · ' + msglogMode : '') + (msglogStuck ? ' · scroll paused' : '');
  // Rebuild the decoder filter chips from the modes present in the data.
  const modes = [...new Set(decodedItems().map(a => a.mode))].sort();
  const bar = el('msglog-modes');
  const barKey = modes.join('|') + '#' + msglogMode;
  if (bar.dataset.key !== barKey) {
    bar.dataset.key = barKey;
    bar.textContent = '';
    const mk = (label, value) => {
      const b = document.createElement('button');
      b.className = 'chip' + (value === msglogMode ? ' on' : '');
      b.textContent = label; b.dataset.dmode = value;
      b.addEventListener('click', () => { msglogMode = value; renderMsglog(); });
      bar.appendChild(b);
    };
    mk('all', '');
    modes.forEach(m => mk(m, m));
  }
}
function msglogText() {
  return decodedItems().filter(a => !msglogMode || a.mode === msglogMode)
    .map(a => fmtTime(a.started) + ' ' + a.mode + ' ' + mhz(a.frequency_hz) + '  ' + a.detail).join('\n');
}
el('msglog-copy').addEventListener('click', async () => {
  const text = msglogText();
  if (!text) { toast('nothing to copy', undefined, true); return; }
  try { await navigator.clipboard.writeText(text); toast('messages copied', undefined, true); }
  catch { toast('clipboard blocked by the browser', undefined, true); }
});
el('msglog-export').addEventListener('click', () => {
  const text = msglogText();
  if (!text) { toast('nothing to export', undefined, true); return; }
  const stamp = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19);
  downloadFile('sdrstream-messages-' + stamp + '.txt', text, 'text/plain');
});
el('msglog').addEventListener('scroll', () => {
  const box = el('msglog');
  msglogStuck = box.scrollTop + box.clientHeight < box.scrollHeight - 40;
  el('msglog-state').textContent = el('msglog-state').textContent.replace(/ · scroll paused$/, '') + (msglogStuck ? ' · scroll paused' : '');
});

/* ---------- in-browser audio recording ---------- */
let recorder = null;
function recordButtonFor(audio) {
  const btn = document.createElement('button');
  btn.type = 'button';
  btn.className = 'rec-btn';
  btn.title = 'Record this playback to a file';
  btn.setAttribute('aria-label', 'record this playback to a file');
  btn.textContent = '\u25CF';
  btn.addEventListener('click', () => toggleRecording(btn, audio));
  return btn;
}
function toggleRecording(btn, audio) {
  if (recorder) {
    recorder.stop();
    return;
  }
  const capture = audio.captureStream || audio.mozCaptureStream;
  if (!capture || typeof MediaRecorder === 'undefined') {
    toast('recording is not supported in this browser', undefined, true);
    return;
  }
  let rec;
  try { rec = new MediaRecorder(capture.call(audio)); }
  catch { toast('recording is not supported in this browser', undefined, true); return; }
  const chunks = [];
  rec.ondataavailable = e => { if (e.data && e.data.size) chunks.push(e.data); };
  rec.onstop = () => {
    document.querySelectorAll('.rec-btn.recording').forEach(b => b.classList.remove('recording'));
    recorder = null;
    if (!chunks.length) { toast('nothing was recorded - press play first', undefined, true); return; }
    const type = rec.mimeType || 'audio/webm';
    const ext = type.includes('ogg') ? 'ogg' : 'webm';
    const stamp = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19);
    downloadFile('sdrstream-recording-' + stamp + '.' + ext, new Blob(chunks, { type }), type);
    toast('recording saved', undefined, true);
  };
  recorder = rec;
  btn.classList.add('recording');
  if (audio.paused) audio.play();
  rec.start();
  audio.addEventListener('ended', () => { if (recorder === rec) rec.stop(); }, { once: true });
}

/* ---------- keyboard shortcuts ---------- */
const viewOrder = () => [...document.querySelectorAll('.mainnav button[data-view]')].map(b => b.dataset.view);
function kbdToggleHelp(force) {
  const box = el('kbd-help');
  box.hidden = force !== undefined ? !force : !box.hidden;
  if (!box.hidden) el('kbd-close').focus();
}
el('kbd-close').addEventListener('click', () => kbdToggleHelp(false));
el('kbd-help').addEventListener('click', e => { if (e.target === el('kbd-help')) kbdToggleHelp(false); });
let allMuted = false;
document.addEventListener('keydown', e => {
  const tag = (e.target.tagName || '').toLowerCase();
  if (tag === 'input' || tag === 'select' || tag === 'textarea' || e.target.isContentEditable) return;
  if (e.metaKey || e.ctrlKey || e.altKey) return;
  const views = viewOrder();
  const current = document.querySelector('.mainnav button.active')?.dataset.view;
  const idx = views.indexOf(current);
  if (e.key === '?') { kbdToggleHelp(); e.preventDefault(); return; }
  if (e.key === 'Escape') { kbdToggleHelp(false); return; }
  if (!el('kbd-help').hidden) return;
  if (e.key >= '0' && e.key <= '9') {
    const n = e.key === '0' ? 9 : +e.key - 1;
    if (views[n]) { showView(views[n]); e.preventDefault(); }
    return;
  }
  switch (e.key) {
    case 'ArrowRight': if (views[idx + 1]) { showView(views[idx + 1]); e.preventDefault(); } break;
    case 'ArrowLeft': if (views[idx - 1]) { showView(views[idx - 1]); e.preventDefault(); } break;
    case '/':
      showView('activity');
      setTimeout(() => el('activity-search').focus(), 60);
      e.preventDefault();
      break;
    case 'e':
      el('activity-export-json').click();
      break;
    case 't': {
      const order = ['dark', 'light', 'auto'];
      settings.theme = order[(order.indexOf(settings.theme) + 1) % order.length];
      saveSettings(); applyTheme(); syncSettingsView();
      toast('theme: ' + settings.theme, undefined, true);
      break;
    }
    case 'm':
      allMuted = !allMuted;
      document.querySelectorAll('audio').forEach(a => { a.muted = allMuted; });
      toast(allMuted ? 'all players muted' : 'players unmuted', undefined, true);
      break;
  }
});

/* ---------- mobile touch: swipe between views ---------- */
(function swipeNav() {
  const main = el('main');
  let startX = 0, startY = 0, tracking = false;
  main.addEventListener('touchstart', e => {
    const t = e.target;
    // Do not hijack gestures that belong to a control or the map.
    if (t.closest('canvas, audio, input, select, .leaflet-container, .msglog, .feed, .gallery, a, button')) { tracking = false; return; }
    tracking = true;
    startX = e.changedTouches[0].clientX;
    startY = e.changedTouches[0].clientY;
  }, { passive: true });
  main.addEventListener('touchend', e => {
    if (!tracking) return;
    tracking = false;
    const dx = e.changedTouches[0].clientX - startX;
    const dy = e.changedTouches[0].clientY - startY;
    if (Math.abs(dx) < 72 || Math.abs(dy) > 56) return;
    const views = viewOrder();
    const current = document.querySelector('.mainnav button.active')?.dataset.view;
    const idx = views.indexOf(current);
    const next = dx < 0 ? views[idx + 1] : views[idx - 1];
    if (next) showView(next);
  }, { passive: true });
})();

/* ---------- settings view ---------- */
let spectrumTimer = null;
let activityTimer = null;
function applyTimers() {
  if (spectrumTimer) { clearInterval(spectrumTimer); spectrumTimer = null; }
  if (+settings.spectrumRate > 0) spectrumTimer = setInterval(refreshSpectrum, +settings.spectrumRate);
  if (activityTimer) clearInterval(activityTimer);
  activityTimer = setInterval(refreshActivity, +settings.activityRate > 0 ? +settings.activityRate : 4000);
}
function syncSettingsView() {
  el('set-theme').value = settings.theme;
  el('set-compact').checked = !!settings.compact;
  el('set-spectrum-rate').value = String(settings.spectrumRate);
  el('set-activity-rate').value = String(settings.activityRate);
  el('set-toasts').checked = settings.toasts !== false;
  el('set-page-size').value = String(settings.pageSize);
}
function initSettingsView() {
  syncSettingsView();
  el('set-theme').addEventListener('change', () => { settings.theme = el('set-theme').value; saveSettings(); applyTheme(); });
  el('set-compact').addEventListener('change', () => { settings.compact = el('set-compact').checked; saveSettings(); applyTheme(); });
  el('set-spectrum-rate').addEventListener('change', () => { settings.spectrumRate = +el('set-spectrum-rate').value; saveSettings(); applyTimers(); });
  el('set-activity-rate').addEventListener('change', () => { settings.activityRate = +el('set-activity-rate').value; saveSettings(); applyTimers(); });
  el('set-toasts').addEventListener('change', () => { settings.toasts = el('set-toasts').checked; saveSettings(); });
  el('set-page-size').addEventListener('change', () => { settings.pageSize = +el('set-page-size').value; activityLimit = 0; saveSettings(); renderActivity(); });
  el('set-reset').addEventListener('click', () => {
    settings = { ...DEFAULT_SETTINGS };
    saveSettings(); syncSettingsView(); applyTheme(); applyTimers();
    activityLimit = 0; renderActivity();
    toast('settings reset', undefined, true);
  });
}

/* ---------- API diagnostics ---------- */
function renderApiDiag() {
  const box = el('diag-list');
  if (!box) return;
  const keys = Object.keys(apiDiag).sort();
  if (!keys.length) { box.innerHTML = '<div class="empty">no API calls recorded yet</div>'; return; }
  box.textContent = '';
  keys.forEach(k => {
    const d = apiDiag[k];
    const row = document.createElement('div');
    row.className = 'diag-row ' + (d.ok ? 'diag-ok' : 'diag-bad');
    const name = document.createElement('b'); name.textContent = k;
    const state = document.createElement('span');
    state.textContent = (d.ok ? 'ok' : 'FAILED') + ' · ' + d.ms + ' ms · ' + relTime(new Date(d.at).toISOString()) + (d.ok ? '' : ' · ' + d.err);
    row.append(name, state);
    box.appendChild(row);
  });
}
el('diag-refresh') && el('diag-refresh').addEventListener('click', renderApiDiag);
el('diag-logtail') && el('diag-logtail').addEventListener('click', async () => {
  const pre = el('diag-log');
  pre.hidden = false;
  pre.textContent = 'loading…';
  try {
    const resp = await fetch('/api/logs/tail?lines=80', { credentials: 'same-origin' });
    pre.textContent = resp.ok ? (await resp.text()) || '(log buffer empty)' : 'log tail failed: HTTP ' + resp.status;
  } catch (err) { pre.textContent = 'log tail failed: ' + (err && err.message || err); }
});

/* ---------- boot ---------- */
// Route the initial hash only now: the map module's state is declared below
// the router, so an early #map route would hit the temporal dead zone.
applyTheme();
initSettingsView();
buildRxControls();
initMonitorLifecycle();
if (location.hash && el('view-' + location.hash.slice(1))) showView(location.hash.slice(1)); else showView('overview');
refreshReceiverPanel(); renderMsglog();
setInterval(refreshReceiverPanel, 3000);
setInterval(renderMsglog, 4000);
refreshStatus(); refreshSats(); refreshGallery(); refreshStations(); refreshScans(); refreshActivity(); refreshFeeds(); refreshSources(false); refreshBandCond(); refreshLive(); refreshHealth();
el('sources-refresh').onclick = () => refreshSources(true);
setInterval(refreshStatus, 5000);
applyTimers();
setInterval(refreshFeeds, 15000);
setInterval(refreshHealth, 30000);
setInterval(refreshScans, 2000);
setInterval(refreshScanLog, 1500);
setInterval(refreshLive, 1000);
setInterval(refreshGallery, 60000);
setInterval(refreshStations, 300000);
setInterval(refreshBandCond, 900000);

/* ---------- receiver proof ledger (own captures, honest verdicts) ---------- */
/* Every archived receiver capture lands one proof-ledger entry
   (source="receiver"). Each entry keeps the live quality gate's verdict as
   an explicit evidence class - "verified live" only when the gate passed,
   otherwise "unverified" with the gate reason - plus the playable clip and
   the raw decode, so the archive can never show a rejected clip as more
   than it is. */
function renderReceiverProof(entries) {
  const box = el('rxproof-list');
  if (!box) return;
  const mine = (entries || []).filter(e => e.source === 'receiver')
    .slice().sort((a, b) => String(b.provider_time).localeCompare(String(a.provider_time)));
  box.textContent = '';
  if (!mine.length) {
    box.innerHTML = '<div class="empty">no receiver captures archived yet - scheduled captures land here with their audio, decode and verdict</div>';
    return;
  }
  mine.slice(0, 8).forEach(e => {
    const card = document.createElement('div'); card.className = 'card rxproof-card';
    const title = document.createElement('div'); title.className = 'card-title';
    const b = document.createElement('b'); b.textContent = e.label || 'capture';
    const when = document.createElement('span'); when.className = 'muted';
    when.textContent = ' ' + fmtDate(e.provider_time);
    title.append(b, when); card.appendChild(title);
    const badges = document.createElement('div'); badges.className = 'badge-row';
    const ev = document.createElement('span');
    if (e.evidence === 'verified') {
      ev.className = 'badge verified';
      ev.textContent = e.crosscheck === 'verified' ? 'verified live' : 'gate passed';
      ev.title = 'Go live quality gate passed' + (e.crosscheck === 'verified' ? ' and independently cross-checked' : ' - no independent cross-check recorded');
    } else {
      ev.className = 'badge unverified';
      ev.textContent = e.evidence === 'unverified' ? 'unverified' : 'not gated';
      if (e.gate) ev.title = 'quality gate: ' + e.gate;
    }
    badges.appendChild(ev);
    const src = document.createElement('span');
    src.className = 'badge scheduled'; src.textContent = 'decoded by sdrstream';
    badges.appendChild(src);
    if (e.receiver) {
      const rx = document.createElement('span'); rx.className = 'badge blind'; rx.textContent = shortURL(e.receiver);
      badges.appendChild(rx);
    }
    facetChips({ gate_verdict: e.gate_verdict || (e.evidence === 'verified' ? 'passed' : ''),
      crosscheck: e.crosscheck, crosscheck_ref: e.crosscheck_ref, crosscheck_detail: e.crosscheck_detail,
      gate: e.gate, verified: e.evidence === 'verified' }).forEach(ch => badges.appendChild(ch));
    card.appendChild(badges);
    if (e.gate) {
      const g = document.createElement('div'); g.className = 'card-meta';
      g.textContent = 'quality gate: ' + e.gate;
      card.appendChild(g);
    }
    if (e.detail) {
      const d = document.createElement('pre'); d.className = 'proof-decode'; d.textContent = e.detail;
      card.appendChild(d);
    }
    if (e.ref && e.ref.indexOf('/audio/') === 0) {
      const au = document.createElement('audio');
      au.className = 'proof-audio'; au.controls = true; au.preload = 'none'; au.src = e.ref;
      au.setAttribute('aria-label', 'listen to ' + (e.label || 'capture'));
      card.appendChild(au);
      card.appendChild(recordButtonFor(au));
      if (/\.wav$/i.test(e.ref)) {
        const spec = document.createElement('img');
        spec.className = 'proof-spec'; spec.loading = 'lazy'; spec.alt = 'spectrogram of the archived clip';
        spec.src = e.ref + '.spec.png';
        spec.onerror = () => { spec.hidden = true; };
        card.appendChild(spec);
      }
    } else {
      const none = document.createElement('div'); none.className = 'card-meta';
      none.textContent = 'audio unavailable - only the decode text survives';
      card.appendChild(none);
    }
    card.appendChild(Object.assign(document.createElement('div'), { className: 'card-meta', textContent: 'archived ' + e.id }));
    box.appendChild(card);
  });
  if (mine.length > 8) {
    const more = document.createElement('div'); more.className = 'muted';
    more.textContent = '+ ' + (mine.length - 8) + ' older receiver entries in the full proof ledger (live data view)';
    box.appendChild(more);
  }

}

// Installable-app support: register the service worker that caches the
// static shell. Live data (/api, /audio) always bypasses it.
if ('serviceWorker' in navigator) {
  window.addEventListener('load', () => {
    navigator.serviceWorker.register('/sw.js').catch((err) => {
      console.warn('service worker registration failed:', err);
    });
  });
}
