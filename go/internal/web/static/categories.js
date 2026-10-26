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
 * "Hide frequency guesses" hides frequency-inferred items; target-based items stay and are tagged as a target, not a verified decode. Items that match nothing are
 * "unclassified", not forced into a bucket.
 */
(function () {
  'use strict';
  var CATS = [
    ['satellite', 'Satellites'], ['aircraft', 'Aircraft'], ['atc', 'ATC / airband'],
    ['marine', 'Marine'], ['weather', 'Weather'], ['ham', 'Ham'], ['broadcast', 'Broadcast'],
    ['unclassified', 'Unclassified']
  ];
  var state = { cat: '', hideGuesses: false };
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
    if (!state.cat && !state.hideGuesses) return true;
    var c = classify(a);
    if (state.cat && c.id !== state.cat) return false;
    if (state.hideGuesses && c.basis !== 'decoder' && c.basis !== 'target') return false;
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
    var cb = bar.querySelector('input'); if (cb) cb.checked = state.hideGuesses;
  }
  function update(items) {
    counts = { _all: (items || []).length };
    (items || []).forEach(function (a) { var id = classify(a).id; counts[id] = (counts[id] || 0) + 1; });
    paint();
  }
  function rerender() { paint(); if (window.renderActivity) window.renderActivity(); }
  function reset() { state.cat = ''; state.hideGuesses = false; paint(); }
  var LABEL = {}; CATS.forEach(function (c) { LABEL[c[0]] = c[1]; });
  var BASIS = { decoder: 'decoder', target: 'target, not a verified decode', frequency: 'frequency guess' };
  function tag(a) {
    var c = classify(a);
    if (c.id === 'unclassified') return null;
    var t = document.createElement('span');
    t.className = 'muted cat-tag';
    t.textContent = LABEL[c.id] + ' \u00b7 ' + BASIS[c.basis];
    t.title = c.basis === 'frequency' ? 'Guessed from the frequency band only' : c.basis === 'target' ? 'The monitor was aimed at this named service; the decode itself does not prove it' : 'The decoder that produced this item names the service';
    return t;
  }
  function build() {
    var kinds = document.querySelector('#view-activity [data-kind]');
    if (!kinds || bar) return;
    bar = document.createElement('div');
    bar.className = 'toolbar cat-bar'; bar.setAttribute('role', 'group'); bar.setAttribute('aria-label', 'Filter by category');
    var all = [['', 'All categories']].concat(CATS);
    bar.innerHTML = all.map(function (c) {
      return '<button type="button" class="chip" data-cat="' + c[0] + '">' + c[1] + ' <span class="n muted">0</span></button>';
    }).join('') +
      '<label class="cat-ev" title="Hide items whose category is only guessed from where the signal sits in a band plan"><input type="checkbox"> Hide frequency guesses</label>' +
      '<span class="muted cat-note">Each item is tagged by basis: decoder (the decoder named the service), target (the monitor was aimed at a named service; not a verified decode) or frequency guess (band position only; reference ranges vary by country).</span>';
    kinds.parentNode.after(bar);
    bar.addEventListener('click', function (e) {
      var b = e.target.closest('[data-cat]'); if (!b) return;
      state.cat = b.dataset.cat; rerender();
    });
    bar.querySelector('input').addEventListener('change', function (e) { state.hideGuesses = e.target.checked; rerender(); });
    paint();
  }
  document.addEventListener('DOMContentLoaded', build);
  window.SDRCategory = { tag: tag, classify: classify, match: match, update: update, reset: reset, state: state };
})();
