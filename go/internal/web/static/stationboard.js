/* Station board: a read-only view of the server probe catalog
   (/api/station-catalog.json). The server sweeps public SDR directories and
   records liveness and capability; this page only reads that catalog and
   never contacts a station. Loads when the view opens, re-checks after 60s,
   and offers a manual refresh. */
(function () {
  'use strict';
  const $ = id => document.getElementById(id);
  // streamClass separates a mere transport answer from a usable IQ/audio
  // stream. A station that answered but delivered no frames is not a
  // working stream, and counting it as one would overstate the catalog.
  function streamClass(st) {
    if (!st.reachable) return 'silent';
    const v = String(st.verdict || '').toLowerCase();
    if (v.includes('-decoded') || v.includes('frames-received') || v.includes('samples-received') || v.includes('stream-received')) return 'streaming';
    return 'answered';
  }
  const KINDS = { kiwi: 'KiwiSDR', openwebrx: 'OpenWebRX', websdr: 'WebSDR' };
  const BANDS = [
    ['LF', 30e3, 300e3], ['MF', 300e3, 3e6], ['HF', 3e6, 30e6],
    ['VHF', 30e6, 300e6], ['UHF', 300e6, 3e9]
  ];
  const STALE_MS = 60000;
  let request = 0, inflight = false, loaded = null, error = '', lastFetch = 0;
  let filterKind = '', filterReach = '', limit = 60;
  // Network-reported text is shown as inert textContent; control characters
  // are the only ones that can still distort the layout.
  const clean = s => String(s || '').replace(/[\u0000-\u001f]/g, '').slice(0, 200);
  const mhzFmt = hz => (hz / 1e6).toFixed(2).replace(/\.?0+$/, '');
  function coverage(cap) {
    if (!cap.freq_min_hz || !cap.freq_max_hz) return '';
    const bands = BANDS.filter(b => cap.freq_min_hz < b[2] && cap.freq_max_hz > b[1]).map(b => b[0]);
    return mhzFmt(cap.freq_min_hz) + '–' + mhzFmt(cap.freq_max_hz) + ' MHz' + (bands.length ? ' · ' + bands.join('/') : '');
  }
  function card(st) {
    const cap = st.capability || {};
    const node = document.createElement('div'); node.className = 'card';
    const title = document.createElement('div'); title.className = 'card-title';
    const b = document.createElement('b'); b.textContent = clean(st.name) || clean(st.key) || 'unnamed station';
    title.appendChild(b);
    const kind = document.createElement('span'); kind.className = 'badge';
    kind.textContent = KINDS[st.kind] || clean(st.kind) || 'unknown type';
    title.appendChild(kind);
    const state = document.createElement('span');
    const cls = streamClass(st);
    state.className = 'badge ' + (cls === 'streaming' ? 'board-alive' : cls === 'answered' ? 'board-answered' : 'board-dead');
    state.textContent = (cls === 'streaming' ? 'streaming' : cls === 'answered' ? 'answered, no stream' : 'no response') + (st.verdict ? ' · ' + clean(st.verdict) : '');
    title.appendChild(state);
    node.appendChild(title);
    if (st.url) {
      const line = document.createElement('div'); line.className = 'card-meta';
      // Only absolute http(s) URLs become links; anything else stays inert
      // text. Property assignment keeps quotes inside the attribute.
      const raw = String(st.url);
      if (/^https?:\/\//i.test(raw)) {
        const a = document.createElement('a'); a.href = raw; a.target = '_blank'; a.rel = 'noopener noreferrer';
        a.textContent = raw; line.appendChild(a);
      } else {
        line.textContent = String(st.url); // inert text, never a link
      }
      node.appendChild(line);
    }
    const where = [clean(cap.location), clean(cap.antenna)].filter(Boolean).join(' · ');
    if (where) { const line = document.createElement('div'); line.className = 'card-meta'; line.textContent = where; node.appendChild(line); }
    const cov = coverage(cap);
    if (cov) { const line = document.createElement('div'); line.className = 'card-meta'; line.textContent = 'coverage ' + cov; node.appendChild(line); }
    const capacity = [];
    if (cap.users_max) capacity.push(cap.users_max + (cap.users_max === 1 ? ' slot' : ' slots'));
    if (cap.rx_channels) capacity.push(cap.rx_channels + (cap.rx_channels === 1 ? ' channel' : ' channels'));
    if (cap.passworded) capacity.push('password required');
    if (capacity.length) { const line = document.createElement('div'); line.className = 'card-meta'; line.textContent = capacity.join(' · '); node.appendChild(line); }
    if (cap.profiles && cap.profiles.length) {
      const line = document.createElement('div'); line.className = 'card-meta';
      line.textContent = 'profiles: ' + cap.profiles.map(clean).join(', ');
      node.appendChild(line);
    }
    const facts = [];
    facts.push(st.checked_at ? 'checked ' + fmtDate(st.checked_at) : 'never checked');
    if (clean(cap.version)) facts.push('server ' + clean(cap.version));
    if (st.consec_failures === 1) facts.push('1 consecutive failure');
    else if (st.consec_failures > 1) facts.push(st.consec_failures + ' consecutive failures');
    const meta = document.createElement('div'); meta.className = 'card-meta muted';
    meta.textContent = facts.join(' · ');
    node.appendChild(meta);
    const next = untilCheck(st.next_check_at);
    if (next) {
      const line = document.createElement('div'); line.className = 'card-meta muted';
      line.textContent = 'next check ' + next;
      node.appendChild(line);
    }
    return node;
  }
  // Relative time until the monitor's next planned probe: "~45m", "~3h",
  // or "due" when the scheduled moment has passed.
  function untilCheck(iso) {
    if (!iso) return '';
    const ms = new Date(iso).getTime() - Date.now();
    if (isNaN(ms)) return '';
    if (ms <= 60000) return 'due';
    const m = Math.round(ms / 60000);
    if (m < 60) return '~' + m + 'm';
    const h = Math.round(m / 60);
    if (h < 24) return '~' + h + 'h';
    return '~' + Math.round(h / 24) + 'd';
  }
  // One health-transition event: direction is a probe-outcome fact
  // (stopped/resumed answering probes), never a reception claim.
  function eventCard(ev, names) {
    const node = document.createElement('div'); node.className = 'card';
    const title = document.createElement('div'); title.className = 'card-title';
    const dir = document.createElement('span');
    const recovered = ev.kind === 'recovered';
    dir.className = 'badge ' + (recovered ? 'board-alive' : 'board-dead');
    dir.textContent = recovered ? 'resumed answering' : 'stopped answering';
    title.appendChild(dir);
    const b = document.createElement('b'); b.textContent = names[ev.key] || clean(ev.key) || 'unknown station';
    title.appendChild(b);
    node.appendChild(title);
    const meta = document.createElement('div'); meta.className = 'card-meta muted';
    meta.textContent = fmtDate(ev.at) + (clean(ev.verdict) ? ' · ' + clean(ev.verdict) : '');
    node.appendChild(meta);
    return node;
  }
  function emptyNote(box, text) {
    const note = document.createElement('div'); note.className = 'empty'; note.textContent = text;
    box.appendChild(note);
  }
  function render() {
    const data = loaded;
    const disabled = !!(data && data.enabled === false);
    $('board-disabled').hidden = !disabled;
    $('board-enabled').hidden = disabled;
    if (disabled) { $('board-state').textContent = ''; return; }
    if (!data && !error) return; // nothing fetched yet; static shell stays
    const stations = (data && data.stations) || [];
    if (data) {
      let streaming = 0, answered = 0;
      stations.forEach(s => { const c = streamClass(s); if (c === 'streaming') streaming++; else if (c === 'answered') answered++; });
      $('board-total').textContent = String(stations.length);
      $('board-streaming').textContent = String(streaming);
      $('board-answered').textContent = String(answered);
      $('board-silent').textContent = String(stations.length - streaming - answered);
      $('board-updated').textContent = data.updated_at ? 'catalog updated ' + fmtDate(data.updated_at) : '';
    }
    const filtered = stations.filter(s =>
      (!filterKind || s.kind === filterKind) &&
      (!filterReach || streamClass(s) === filterReach));
    const shown = filtered.slice(0, limit);
    const box = $('board-list'); box.textContent = '';
    shown.forEach(st => box.appendChild(card(st)));
    if (error && !stations.length) emptyNote(box, 'Station catalog unavailable: ' + error + '. Press refresh to retry.');
    else if (data && !stations.length) emptyNote(box, 'No stations in the catalog yet - the first sweep has not recorded anything.');
    else if (!filtered.length) emptyNote(box, 'No station matches these filters.');
    $('board-state').textContent = error ? 'Station catalog unavailable: ' + error + '. Press refresh to retry.'
      : (filtered.length < stations.length ? 'showing ' + filtered.length + ' of ' + stations.length + ' stations' : '');
    const more = $('board-more');
    more.hidden = filtered.length <= shown.length;
    if (!more.hidden) more.textContent = 'show ' + Math.min(200, filtered.length - shown.length) + ' more of ' + filtered.length;
    const eventsBox = $('board-events'); eventsBox.textContent = '';
    if (data) {
      const names = Object.create(null); // directory keys are untrusted: no prototype chain
      stations.forEach(st => { if (clean(st.name)) names[st.key] = clean(st.name); });
      const events = data.events || [];
      events.forEach(ev => eventsBox.appendChild(eventCard(ev, names)));
      if (!events.length) emptyNote(eventsBox, 'No reachability transitions recorded yet.');
    }
  }
  async function load() {
    const id = ++request;
    inflight = true;
    if (!loaded) $('board-state').textContent = 'Loading the station catalog…';
    try {
      const data = await fetchJSON('/api/station-catalog.json');
      if (id !== request) return;
      loaded = data; error = ''; lastFetch = Date.now();
      render();
    } catch (err) {
      if (id !== request) return;
      error = (err && err.message) || String(err);
      render();
    } finally {
      if (id === request) inflight = false;
    }
  }
  // app.js calls this from showView, like initMap for the map view.
  window.stationBoardRefresh = () => {
    if (inflight) return;
    if (!loaded || Date.now() - lastFetch > STALE_MS) load();
  };
  function chips(attr, apply) {
    document.querySelectorAll('[' + attr + ']').forEach(chip => chip.addEventListener('click', () => {
      apply(chip.dataset[attr.replace('data-', '')]);
      limit = 60;
      document.querySelectorAll('[' + attr + ']').forEach(c => c.classList.toggle('on', c === chip));
      render();
    }));
  }
  chips('data-bkind', v => { filterKind = v; });
  chips('data-breach', v => { filterReach = v; });
  $('board-refresh').addEventListener('click', () => load());
  $('board-more').addEventListener('click', () => { limit += 200; render(); });
  // Direct #stationboard load: app.js ran showView before this script existed.
  if (location.hash === '#stationboard') load();
})();
