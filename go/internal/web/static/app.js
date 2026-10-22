'use strict';
/* sdrstream UI - vanilla JS, no build step. Talks to the existing JSON API
   plus /api/map.json and /api/tiles/ (same-origin tile proxy). */

const el = id => document.getElementById(id);
const fetchJSON = async (url, options = {}) => {
  const response = await fetch(url, { credentials: 'same-origin', ...options });
  if (response.status === 401) { toast('Login expired. Sign in again.'); setTimeout(()=>{location.href='/login'},250); throw new Error('login required'); }
  if (!response.ok) throw new Error(url + ' → HTTP ' + response.status);
  try { return await response.json(); } catch { throw new Error(url + ' → not JSON'); }
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
const shortURL = u => { try { return new URL(u).host; } catch { return u; } };
const relTime = iso => {
  const s = Math.round((Date.now() - new Date(iso)) / 1000);
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

/* ---------- toasts ---------- */
function toast(text, tone) {
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
    { label: 'passes 12h', value: (status.passes || []).length, cls: '' },
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
    box.appendChild(card);
  });
}

/* ---------- captures ---------- */
function renderCaptures(status) {
  const captures = (status.captures || []).slice().reverse();
  const box = el('captures-list');
  const key = captures.map(c => c.path + '|' + c.url).join(',');
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
      meta.textContent = shortURL(c.receiver) + ' · ' + c.samples + ' samples · ' + c.rate + ' Hz';
      card.appendChild(meta);
      if (c.url) {
        const audio = document.createElement('audio');
        audio.controls = true; audio.preload = 'none'; audio.src = c.url;
        audio.setAttribute('aria-label', 'listen to ' + (c.satellite || 'capture'));
        card.appendChild(audio);
        const open = document.createElement('div'); open.style.marginTop = '6px';
        open.appendChild(link(c.url, c.path));
        card.appendChild(open);
      } else {
        const p = document.createElement('div'); p.className = 'card-meta'; p.textContent = c.path;
        card.appendChild(p);
      }
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
      [s.name, s.norad_id, (s.downlink_hz / 1e6).toFixed(4) + ' MHz', s.mode, s.relay || '—', s.status, s.receivers]
        .forEach(v => { const td = document.createElement('td'); td.textContent = v; tr.appendChild(td); });
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
  el('gallery-count').textContent = active === 'live' ? live.length + ' verified live decodes' : satnogs.length + ' SatNOGS artefacts';
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
    note.textContent = active === 'live' ? 'no verified live decodes yet' : 'no SatNOGS artefacts downloaded yet';
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
  (lastLive && lastLive.on_air || []).forEach(p => {
    if (!span) return;
    const x = ((p.frequency_hz - (centre - span / 2)) / span) * w;
    if (x < 0 || x > w) return;
    ctx.strokeStyle = 'rgba(52,211,153,.9)'; ctx.lineWidth = 2;
    ctx.beginPath(); ctx.moveTo(x, 0); ctx.lineTo(x, h); ctx.stroke();
    ctx.fillStyle = 'rgba(52,211,153,.9)'; ctx.font = (9 * ratio) + 'px ui-monospace, monospace';
    ctx.fillText('ON AIR', Math.min(x + 3, w - 40 * ratio), 10 * ratio);
  });
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
      setMonitor('monitoring');
    } else {
      setMonitor('no spectrum');
    }
  } catch {
    setMonitor('no monitor');
  }
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
  } catch { /* monitor offline */ }
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
function renderActivity() {
  const full = activityItems.filter(a => !activityKind || a.kind === activityKind);
  const feed = el('feed');
  feed.textContent = '';
  if (!full.length) { feed.innerHTML = '<div class="empty">No verified activity yet. Start the Scan view to check public receivers.</div>'; }
  full.slice(0, 80).forEach(a => feed.appendChild(activityCard(a, false)));
  const over = el('feed-overview');
  over.textContent = '';
  if (!activityItems.length) { over.innerHTML = '<div class="empty">no traffic seen yet</div>'; }
  activityItems.slice(0, 6).forEach(a => over.appendChild(activityCard(a, false)));
  renderDecodes();
}
document.querySelectorAll('[data-kind]').forEach(button => button.addEventListener('click', () => {
  document.querySelectorAll('[data-kind]').forEach(b => b.classList.toggle('on', b === button));
  activityKind = button.dataset.kind;
  renderActivity();
}));


/* ---------- live pulse: signal meter, on-air pill, receivers, decodes ---------- */
let lastLive = null;
let scansData = null;
const FAILED_STATE = /refus|error|timeout|block|fail|skip|unreach/i;

async function refreshLive() {
  try {
    const live = await fetchJSON('/api/live.json');
    lastLive = live;
    renderSignal(live);
    renderOnAirPill(live);
    renderReceivers(live);
  } catch { /* endpoint older than the UI or server busy */ }
}

function renderSignal(live) {
  const meter = el('signal-meter');
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
      meta.textContent = mhz(x.frequency_hz) + ' · ' + (x.mode || '—') + (x.result ? ' · ' + x.result : '');
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
 const chip=(label,value,state,count)=>{const b=document.createElement('button');b.className='chip'+(feedsSourceFilter===value?' on':'');b.textContent=label+(count?' '+count:'');if(state)b.dataset.state=state;b.onclick=()=>{feedsSourceFilter=value;renderFeeds()};box.appendChild(b)};
 chip('all','', '',items.length); sources.forEach(s=>chip(s.name,s.name,s.state,s.items));
 const note=el('feeds-note'); note.textContent='updated '+relTime(feedsSnapshot.updated_at)+sources.filter(s=>s.state==='disabled'||s.state==='error').map(s=>' · '+s.name+': '+(s.detail||s.state)).join('');
 const list=el('feeds-list'); list.textContent=''; const shown=items.filter(i=>!feedsSourceFilter||i.source===feedsSourceFilter);
 if(!shown.length){list.innerHTML='<div class="empty">no live data yet - feeds are connecting</div>';return}
 shown.slice(0,120).forEach(it=>{const c=document.createElement('div');c.className='card-activity feed-'+(it.kind||'data');const r=document.createElement('div');r.className='row';const label=document.createElement('span');label.className='freq';label.textContent=it.label;const badge=document.createElement('span');badge.className='badge feed-src';badge.textContent=it.source;const t=document.createElement('span');t.className='muted';t.textContent=relTime(it.time);r.append(label,badge,t);if(it.ref){const a=document.createElement('a');a.href=it.ref;a.target='_blank';a.rel='noopener';a.textContent='source';r.append(a)}c.appendChild(r);if(it.detail){const d=document.createElement('div');d.className='detail';d.textContent=it.detail;c.appendChild(d)}if(it.has_pos){const m=document.createElement('div');m.className='meta';m.textContent=it.lat.toFixed(3)+', '+it.lon.toFixed(3);c.appendChild(m)}list.appendChild(c)});
}

/* ---------- boot ---------- */
// Route the initial hash only now: the map module's state is declared below
// the router, so an early #map route would hit the temporal dead zone.
if (location.hash && el('view-' + location.hash.slice(1))) showView(location.hash.slice(1)); else showView('overview');
refreshStatus(); refreshSats(); refreshGallery(); refreshStations(); refreshScans(); refreshActivity(); refreshFeeds(); refreshLive();
setInterval(refreshStatus, 5000);
setInterval(refreshActivity, 4000);
setInterval(refreshFeeds, 15000);
setInterval(refreshScans, 2000);
setInterval(refreshScanLog, 1500);
setInterval(refreshSpectrum, 1000);
setInterval(refreshLive, 1000);
setInterval(refreshGallery, 60000);
setInterval(refreshStations, 300000);
