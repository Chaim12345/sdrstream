/* Stations view: receiver coverage filter for the SpyServer directory.
 *
 * Lists /api/spyservers.json entries (host, port, device_type,
 * min_frequency/max_frequency in Hz, clients, max_clients, online) and filters
 * them by whether the tuning range overlaps a category's reference band
 * (satellites, ATC/airband, marine, weather, ham, broadcast).
 *
 * What the labels mean: "covers" = the published tuning range overlaps the
 * band. It is NOT a verified service, the server may be offline or its antenna
 * may hear nothing there, and the directory listing is not verified reachable.
 * Entries without a published range are "coverage unknown" and are shown only
 * under "All" or "Unknown", never counted as covering. Stations from
 * /api/stations.json carry no range and are not part of this list.
 */
(function () {
  'use strict';
  var CATS = [['', 'All'], ['atc', 'ATC / airband'], ['satellite', 'Satellites'], ['marine', 'Marine'],
    ['weather', 'Weather'], ['ham', 'Ham'], ['broadcast', 'Broadcast'], ['unknown', 'Coverage unknown']];
  var state = { cat: '', items: null, error: '', limit: 40 };
  var root;
  function mhz(hz) { return hz <= 0 ? '0' : hz >= 1e6 ? (hz / 1e6).toFixed(hz % 1e6 ? 1 : 0) : (hz / 1e3).toFixed(0) + 'k'; }
  function rangeText(sv) {
    return sv.min_frequency > 0 || sv.max_frequency > 0 ? mhz(sv.min_frequency) + '-' + mhz(sv.max_frequency) + ' MHz' : 'range not published';
  }
  function matches(sv) {
    var C = window.SDRCoverage, r = C.fromSpy(sv);
    if (!state.cat) return true;
    if (state.cat === 'unknown') return C.coverage(r, 'ham') === 'unknown';
    return C.coverage(r, state.cat) === 'covers';
  }
  function counts() {
    var C = window.SDRCoverage, out = { '': state.items.length, unknown: 0 };
    state.items.forEach(function (sv) {
      var r = C.fromSpy(sv);
      if (C.coverage(r, 'ham') === 'unknown') { out.unknown++; return; }
      Object.keys(C.bands).forEach(function (c) { if (C.coverage(r, c) === 'covers') out[c] = (out[c] || 0) + 1; });
    });
    return out;
  }
  function render() {
    if (!root) return;
    var chips = root.querySelector('.cv-chips'), list = root.querySelector('.cv-list'), note = root.querySelector('.cv-count');
    list.textContent = '';
    if (state.error) { note.textContent = ''; list.innerHTML = '<div class="empty"></div>'; list.firstChild.textContent = state.error; chips.textContent = ''; return; }
    if (!state.items) { note.textContent = 'loading...'; return; }
    var n = counts();
    chips.innerHTML = CATS.map(function (c) {
      return '<button type="button" class="chip' + (c[0] === state.cat ? ' on' : '') + '" data-cv="' + c[0] + '" aria-pressed="' + (c[0] === state.cat) + '">' + c[1] + ' <span class="muted">' + (n[c[0]] || 0) + '</span></button>';
    }).join('');
    var shown = state.items.filter(matches);
    note.textContent = shown.length + ' of ' + state.items.length + ' directory entries';
    if (!shown.length) { list.innerHTML = '<div class="empty">no directory entry matches</div>'; return; }
    shown.slice(0, state.limit).forEach(function (sv) {
      var card = document.createElement('div'); card.className = 'card';
      var t = document.createElement('div'); t.className = 'card-title';
      var b = document.createElement('b'); b.textContent = (sv.device_type || 'SpyServer') + ' ' + sv.host + ':' + sv.port;
      var st = document.createElement('span'); st.className = 'muted';
      st.textContent = sv.online === true ? 'listed online' : sv.online === false ? 'listed offline' : 'status not listed';
      t.appendChild(b); t.appendChild(st);
      var m = document.createElement('div'); m.className = 'card-meta';
      var users = sv.max_clients ? ' \u00b7 ' + (sv.clients || 0) + '/' + sv.max_clients + ' users' : '';
      var cov = state.cat && state.cat !== 'unknown' ? window.SDRCoverage.describe(window.SDRCoverage.fromSpy(sv), state.cat) : window.SDRCoverage.describe(window.SDRCoverage.fromSpy(sv), 'ham').replace(/Can tune ham.*/, 'Tuning range published');
      m.textContent = rangeText(sv) + users + ' \u00b7 ' + cov;
      card.appendChild(t); card.appendChild(m); list.appendChild(card);
    });
    if (shown.length > state.limit) {
      var more = document.createElement('button'); more.type = 'button'; more.className = 'btn';
      more.textContent = 'show ' + Math.min(40, shown.length - state.limit) + ' more';
      more.addEventListener('click', function () { state.limit += 40; render(); });
      list.appendChild(more);
    }
  }
  function load() {
    if (state.items || state.loading) return;
    state.loading = true;
    fetch('/api/spyservers.json', { credentials: 'same-origin' }).then(function (r) {
      if (!r.ok) throw new Error('SpyServer directory not available (HTTP ' + r.status + ')');
      return r.json();
    }).then(function (j) { state.items = Array.isArray(j) ? j : []; render(); })
      .catch(function (e) { state.error = String(e.message || e); render(); });
  }
  function build() {
    var view = document.getElementById('view-stations');
    if (!view || root) return;
    root = document.createElement('div'); root.className = 'panel cv-panel'; root.id = 'coverage-panel';
    root.innerHTML = '<div class="panel-head"><h2>SpyServer coverage</h2><span class="muted cv-count"></span></div>' +
      '<p class="muted cv-note">Filter by whether a receiver\'s published tuning range overlaps a band. This is coverage, not a verified service: the server may be offline or hear nothing there. Reference bands vary by country. Stations below without a published range are coverage unknown.</p>' +
      '<div class="toolbar cv-chips" role="group" aria-label="Coverage category"></div><div class="cards cv-list"></div>';
    view.insertBefore(root, view.firstChild);
    root.addEventListener('click', function (e) {
      var b = e.target.closest('[data-cv]'); if (!b) return;
      state.cat = b.dataset.cv; state.limit = 40; render();
    });
    var nav = document.querySelector('[data-view=stations]');
    if (nav) nav.addEventListener('click', load);
    if (view.classList.contains('active')) load();
  }
  document.addEventListener('DOMContentLoaded', build);
  window.SDRCoverageView = { state: state, matches: matches };
})();
