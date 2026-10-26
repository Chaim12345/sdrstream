/* Receiver coverage helper (not wired into any view yet).
 *
 * A receiver "covers" a category when its tunable range overlaps that
 * category's reference band. That is COVERAGE, not a verified service: a
 * receiver that can tune airband may be mis-configured, offline or have an
 * antenna that hears nothing there. Operator text (description/antenna) is a
 * label supplied by the receiver owner, never evidence of what it receives.
 * Reference bands vary by country; they come from SDRBandPlan-style ranges.
 *
 * Input is normalised with min/max/center in HERE: Hz. The caller must convert
 * from the source unit. Unknown or missing range = "coverage unknown", never
 * silently "covers" or "does not cover".
 */
(function () {
  'use strict';
  var CATEGORY_BANDS = {
    atc: [[118e6, 137e6]],
    satellite: [[137e6, 138e6], [145.8e6, 146e6], [435e6, 438e6]],
    weather: [[162.4e6, 162.55e6]],
    marine: [[156e6, 162.025e6], [489e3, 491e3], [517e3, 519e3]],
    broadcast: [[88e6, 108e6]],
    ham: [[1.8e6, 2e6], [3.5e6, 4e6], [7e6, 7.3e6], [10.1e6, 10.15e6], [14e6, 14.35e6], [18.068e6, 18.168e6], [21e6, 21.45e6], [24.89e6, 24.99e6], [28e6, 29.7e6], [144e6, 146e6], [430e6, 440e6]]
  };
  function range(r) {
    var lo = Number(r && r.minHz), hi = Number(r && r.maxHz);
    if (isFinite(lo) && isFinite(hi) && hi > lo && hi > 0) return [lo, hi];
    return null;
  }
  // "covers" | "no" | "unknown"
  function coverage(receiver, category) {
    var bands = CATEGORY_BANDS[category];
    var r = range(receiver);
    if (!bands || !r) return 'unknown';
    return bands.some(function (b) { return b[1] >= r[0] && b[0] <= r[1]; }) ? 'covers' : 'no';
  }
  var LABEL = { atc: 'airband (ATC)', satellite: 'satellite-band', weather: 'weather', marine: 'marine', broadcast: 'broadcast', ham: 'amateur' };
  function describe(receiver, category) {
    var c = coverage(receiver, category), name = LABEL[category] || category;
    if (c === 'covers') return 'Can tune ' + name + ' frequencies (coverage only, not a verified service)';
    if (c === 'no') return 'Tuning range does not reach ' + name + ' frequencies';
    return 'Tuning range not published (coverage unknown)';
  }
  // SpyServer directory entry (min_frequency/max_frequency in Hz) -> receiver.
  function fromSpy(sv) {
    return { minHz: sv && sv.min_frequency, maxHz: sv && sv.max_frequency };
  }
  window.SDRCoverage = { coverage: coverage, describe: describe, fromSpy: fromSpy, bands: CATEGORY_BANDS };
})();
