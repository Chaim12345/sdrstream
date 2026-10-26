/* Category filter for the Activity feed: satellites, aircraft, ATC, marine,
 * weather, ham, broadcast.
 *
 * Every item gets one category AND a basis, and the two are never mixed up:
 *   decoder    - the decoder that produced it names the service (ACARS, HFDL,
 *                NAVTEX, WEFAX, APT, FT8...). Evidence from the data.
 *   target     - the scheduled target/label names the service. Evidence that
 *                the monitor was aimed there, not that the decode proves it.
 *   frequency  - inferred only from where the signal sits in a reference band
 *                plan. A guess: a signal at 121.5 MHz may be anything.
 * "Evidence only" hides frequency-inferred items. Items that match nothing are
 * "unclassified", not forced into a bucket.
 */
(function () {
  'use strict';
  var CATS = [
    ['satellite', 'Satellites'], ['aircraft', 'Aircraft'], ['atc', 'ATC / airband'],
    ['marine', 'Marine'], ['weather', 'Weather'], ['ham', 'Ham'], ['broadcast', 'Broadcast'],
    ['unclassified', 'Unclassified']
  ];
  var state = { cat: '', evidenceOnly: false };
  function inb(hz, lo, hi) { return hz >= lo && hz <= hi; }
  function classify(a) {
    var mode = String(a.mode || '').toLowerCase();
    var text = (String(a.label || '') + ' ' + String(a.target || '')).toLowerCase();
    var hz = +a.frequency_hz || 0;
    if (/acars|hfdl|vdl|adsb|ads-b|volmet/.test(mode)) return { id: 'aircraft', basis: 'decoder' };
    if (/navtex|ais/.test(mode)) return { id: 'marine', basis: 'decoder' };
    if (/wefax|apt/.test(mode)) return { id: /apt/.test(mode) ? 'satellite' : 'weather', basis: 'decoder' };
    if (/ft8|ft4|wspr|aprs|psk|js8/.test(mode)) return { id: 'ham', basis: 'decoder' };
    if (/volmet|acars|hfdl/.test(text)) return { id: 'aircraft', basis: 'target' };
    if (/tower|approach|atis|\batc\b|airband/.test(text)) return { id: 'atc', basis: 'target' };
    if (/navtex|marine|coast guard|\bais\b/.test(text)) return { id: 'marine', basis: 'target' };
    if (/noaa|weather|\bnws\b|dwd|wefax/.test(text)) return { id: 'weather', basis: 'target' };
    if (/\biss\b|noaa-?1[589]|meteor|\bao-\d|\bso-\d|\bfo-\d|cas-\d|rs-\d|satellite|\bsat\b/.test(text)) return { id: 'satellite', basis: 'target' };
    if (!hz) return { id: 'unclassified', basis: '' };
    if (inb(hz, 118e6, 137e6)) return { id: 'atc', basis: 'frequency' };
    if (inb(hz, 137e6, 138e6) || inb(hz, 145.8e6, 146e6) || inb(hz, 435e6, 438e6)) return { id: 'satellite', basis: 'frequency' };
    if (inb(hz, 162.4e6, 162.55e6)) return { id: 'weather', basis: 'frequency' };
    if (inb(hz, 156e6, 162.025e6) || inb(hz, 489e3, 491e3) || inb(hz, 517e3, 519e3)) return { id: 'marine', basis: 'frequency' };
    if (inb(hz, 88e6, 108e6)) return { id: 'broadcast', basis: 'frequency' };
    if ((window.SDRBandPlan ? window.SDRBandPlan.at(hz) : []).some(function (n) { return /ham/.test(n); })) return { id: 'ham', basis: 'frequency' };
    return { id: 'unclassified', basis: '' };
  }
  function match(a) {
    if (!state.cat && !state.evidenceOnly) return true;
    var c = classify(a);
    if (state.cat && c.id !== state.cat) return false;
    if (state.evidenceOnly && c.basis !== 'decoder' && c.basis !== 'target') return false;
    return true;
  }
  var bar, counts = {};
  function paint() {
    if (!bar) return;
    bar.querySelectorAll('[data-cat]').forEach(function (b) {
      var id = b.dataset.cat, n = id ? (counts[id] || 0) : (counts._all || 0);
      b.classList.toggle('on', id === state.cat);
      b.setAttribute('aria-pressed', id === state.cat ? 'true' : 'false');
      b.querySelector('.n').textContent = n;
    });
    var cb = bar.querySelector('input'); if (cb) cb.checked = state.evidenceOnly;
  }
  function update(items) {
    counts = { _all: (items || []).length };
    (items || []).forEach(function (a) { var id = classify(a).id; counts[id] = (counts[id] || 0) + 1; });
    paint();
  }
  function rerender() { paint(); if (window.renderActivity) window.renderActivity(); }
  function reset() { state.cat = ''; state.evidenceOnly = false; paint(); }
  function build() {
    var kinds = document.querySelector('#view-activity [data-kind]');
    if (!kinds || bar) return;
    bar = document.createElement('div');
    bar.className = 'toolbar cat-bar'; bar.setAttribute('role', 'group'); bar.setAttribute('aria-label', 'Filter by category');
    var all = [['', 'All categories']].concat(CATS);
    bar.innerHTML = all.map(function (c) {
      return '<button type="button" class="chip" data-cat="' + c[0] + '">' + c[1] + ' <span class="n muted">0</span></button>';
    }).join('') +
      '<label class="cat-ev" title="Hide items whose category is only guessed from frequency"><input type="checkbox"> evidence only</label>' +
      '<span class="muted cat-note">Category comes from the decoder or target when known; otherwise it is a guess from the frequency band (reference ranges vary by country).</span>';
    kinds.parentNode.after(bar);
    bar.addEventListener('click', function (e) {
      var b = e.target.closest('[data-cat]'); if (!b) return;
      state.cat = b.dataset.cat; rerender();
    });
    bar.querySelector('input').addEventListener('change', function (e) { state.evidenceOnly = e.target.checked; rerender(); });
    paint();
  }
  document.addEventListener('DOMContentLoaded', build);
  window.SDRCategory = { classify: classify, match: match, update: update, reset: reset, state: state };
})();
