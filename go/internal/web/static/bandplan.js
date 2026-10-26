/* Band plan overlay for the spectrum canvas.
 *
 * Idea and band-colour approach follow sgoudelis/ground-station (GPL-3.0-only)
 * frontend/src/components/waterfall/bandplan-overlay.jsx and bandplans.jsx at
 * 351631d3066b. The ranges below are public spectrum allocations (amateur,
 * broadcast, ISM, aviation, marine, weather, satellite); most were cross-checked
 * against that file's table, the aviation/marine/weather/NAVTEX rows were added
 * here. The code is a new dependency-free canvas implementation; no upstream
 * source text was copied. Allocations are approximate and regional plans
 * differ (for example NOAA weather channels are US), so the labels are a
 * guide, not a licence reference.
 */
(function () {
  'use strict';
  var KEY = 'sdrstream-bandplan';
  var BANDS = [
    [1800e3, 2000e3, '160m ham', '255,0,0'], [3500e3, 4000e3, '80m ham', '255,50,0'],
    [7000e3, 7300e3, '40m ham', '255,100,0'], [10100e3, 10150e3, '30m ham', '255,150,0'],
    [14000e3, 14350e3, '20m ham', '255,200,0'], [18068e3, 18168e3, '17m ham', '200,255,0'],
    [21000e3, 21450e3, '15m ham', '150,255,0'], [24890e3, 24990e3, '12m ham', '100,255,0'],
    [28000e3, 29700e3, '10m ham', '0,255,100'],
    [489e3, 491e3, 'NAVTEX 490', '0,200,255'], [517e3, 519e3, 'NAVTEX 518', '0,200,255'],
    [88e6, 108e6, 'FM broadcast', '233,30,99'],
    [118e6, 137e6, 'airband', '0,188,212'], [121.4e6, 121.6e6, '121.5 emergency', '255,87,34'],
    [137e6, 138e6, 'weather sat (APT)', '76,175,80'],
    [144e6, 146e6, '2m ham', '255,193,7'], [145.8e6, 146e6, 'amateur sat', '255,152,0'],
    [156e6, 162.025e6, 'marine VHF', '33,150,243'], [162.4e6, 162.55e6, 'NOAA weather', '103,58,183'],
    [430e6, 440e6, '70cm ham', '255,160,0'], [433e6, 434e6, '433 ISM', '158,158,158'],
    [435e6, 438e6, 'amateur sat', '255,152,0'], [446e6, 446.2e6, 'PMR446', '121,85,72'],
    [868e6, 868.6e6, '868 ISM', '158,158,158'], [1090e6 - 0.5e6, 1090e6 + 0.5e6, 'ADS-B 1090', '0,188,212']
  ];
  function on() { try { return localStorage.getItem(KEY) !== 'off'; } catch (e) { return true; } }
  function inView(lo, hi) {
    return BANDS.filter(function (b) { return b[1] >= lo && b[0] <= hi; });
  }
  function at(hz) {
    return BANDS.filter(function (b) { return hz >= b[0] && hz <= b[1]; }).map(function (b) { return b[2]; });
  }
  function draw(ctx, w, h, centre, span, ratio) {
    if (!on() || !span) return;
    var lo = centre - span / 2, hi = centre + span / 2;
    var bands = inView(lo, hi);
    var bandH = Math.round(11 * ratio), y0 = h - bandH - 1;
    ctx.save();
    ctx.font = Math.round(9 * ratio) + 'px ui-monospace, monospace';
    ctx.textBaseline = 'middle';
    // Draw wide bands first so narrow ones stay visible on top; stagger
    // overlapping rows (2m ham vs amateur sat) onto a second line.
    bands.sort(function (a, b) { return (b[1] - b[0]) - (a[1] - a[0]); });
    var rows = [];
    bands.forEach(function (b) {
      var x1 = Math.max(0, (b[0] - lo) / span * w), x2 = Math.min(w, (b[1] - lo) / span * w);
      var bw = Math.max(2 * ratio, x2 - x1);
      var row = 0;
      while (rows[row] && rows[row] > x1) row++;
      if (row > 1) return;
      rows[row] = x1 + bw;
      var y = y0 - row * (bandH + 1);
      ctx.fillStyle = 'rgba(' + b[3] + ',.38)';
      ctx.fillRect(x1, y, bw, bandH);
      ctx.fillStyle = 'rgba(' + b[3] + ',.9)';
      ctx.fillRect(x1, y, bw, Math.max(1, ratio));
      var tw = ctx.measureText(b[2]).width;
      if (bw > tw + 6 * ratio) {
        ctx.fillStyle = '#e8eef9';
        ctx.fillText(b[2], x1 + 3 * ratio, y + bandH / 2 + ratio * 0.5);
      }
    });
    ctx.restore();
  }
  function toggle() {
    try { localStorage.setItem(KEY, on() ? 'off' : 'on'); } catch (e) { /* ignore */ }
    var b = document.getElementById('bandplan-toggle');
    if (b) b.setAttribute('aria-pressed', on() ? 'true' : 'false');
    if (window.redrawSpectrum) window.redrawSpectrum();
  }
  document.addEventListener('DOMContentLoaded', function () {
    var b = document.getElementById('bandplan-toggle');
    if (!b) return;
    b.setAttribute('aria-pressed', on() ? 'true' : 'false');
    b.addEventListener('click', toggle);
  });
  window.SDRBandPlan = { draw: draw, at: at, inView: inView, bands: BANDS };
})();
