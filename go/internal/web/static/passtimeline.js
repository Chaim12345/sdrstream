/* Pass timeline: elevation-vs-time chart of predicted satellite passes.
 *
 * Adapted in layout and colour rules from sgoudelis/ground-station
 * (GPL-3.0-only), frontend/src/components/passes/timeline/ at 351631d3066b:
 * elevation on Y (90 deg top, 0 deg bottom), time on X, a "now" marker, pass
 * colour by peak elevation (<10 red, 10-45 grey, >45 green, in progress
 * green), zoom controls. Re-implemented as dependency-free SVG; no upstream
 * source was copied. Unlike upstream, sdrstream status passes carry only
 * rise/peak/set and max elevation, so each curve is a sine-shaped arc through
 * those points, not a propagated elevation track. The page says so.
 */
(function () {
  'use strict';
  var NS = 'http://www.w3.org/2000/svg';
  var HOURS = [2, 4, 8, 12, 24];
  var state = { hours: 8, passes: [], offsetH: 0 };
  var M = { l: 34, r: 10, t: 14, b: 26 };

  function svg(tag, attrs, text) {
    var e = document.createElementNS(NS, tag);
    for (var k in attrs) e.setAttribute(k, attrs[k]);
    if (text != null) e.textContent = text;
    return e;
  }
  function colour(p, now) {
    var rise = +new Date(p.rise), set = +new Date(p.set);
    if (rise <= now && set >= now) return '#3fb950';
    var el = p.max_elevation || 0;
    if (el < 10) return '#f85149';
    if (el <= 45) return '#8b949e';
    return '#3fb950';
  }
  function hhmm(ms) {
    var d = new Date(ms);
    return String(d.getHours()).padStart(2, '0') + ':' + String(d.getMinutes()).padStart(2, '0');
  }
  // Sine arc through rise (0 deg), peak time (max el), set (0 deg). Piecewise
  // half-sine each side so an asymmetric peak time is honoured.
  // Passes come from a prediction that starts at "now". A pass already under
  // way has its rise clamped to the scan time, and its peak can sit before
  // that clamped rise. Those are "clipped": the true start and, if the peak is
  // behind us, the true peak are not in the data. Draw only what is known:
  // from the clipped start, falling to set (peak <= rise) or rising to peak.
  function clipped(p) {
    var r = +new Date(p.rise), pk = p.peak ? +new Date(p.peak) : 0;
    return !!p.active || (pk && pk <= r) || (r <= Date.now() && +new Date(p.set) >= Date.now());
  }
  function peakMs(p) {
    var r = +new Date(p.rise), s = +new Date(p.set);
    var pk = p.peak ? +new Date(p.peak) : (r + s) / 2;
    return pk;
  }
  function arc(p, xOf, yOf) {
    var r = +new Date(p.rise), s = +new Date(p.set), pk = peakMs(p);
    var max = Math.max(0, Math.min(90, p.max_elevation || 0));
    var cl = clipped(p), peakBehind = pk <= r;
    if (!(pk >= r && pk < s) && !peakBehind) pk = (r + s) / 2;
    var pts = [], n = 36;
    for (var i = 0; i <= n; i++) {
      var t = r + (s - r) * i / n, el;
      if (peakBehind) el = max * Math.cos(Math.PI / 2 * (t - r) / (s - r));
      else if (t <= pk) el = max * Math.sin(Math.PI / 2 * (t - r) / (pk - r));
      else el = max * Math.cos(Math.PI / 2 * (t - pk) / (s - pk));
      if (cl && i === 0 && el > 1) pts.push('M' + xOf(t).toFixed(1) + ' ' + yOf(0).toFixed(1) + 'L' + xOf(t).toFixed(1) + ' ' + yOf(el).toFixed(1));
      else pts.push((i ? 'L' : 'M') + xOf(t).toFixed(1) + ' ' + yOf(el).toFixed(1));
    }
    return pts.join('');
  }
  function render() {
    var host = document.getElementById('pass-timeline');
    if (!host) return;
    var box = host.querySelector('.pt-chart');
    var w = Math.max(280, (host.clientWidth || 340) - 22), h = 220;
    var now = Date.now();
    var start = now - 0.1 * state.hours * 3600e3 + state.offsetH * 3600e3;
    var end = start + state.hours * 3600e3;
    var plotW = w - M.l - M.r, plotH = h - M.t - M.b;
    var xOf = function (t) { return M.l + (t - start) / (end - start) * plotW; };
    var yOf = function (el) { return M.t + (90 - el) / 90 * plotH; };
    var root = svg('svg', { viewBox: '0 0 ' + w + ' ' + h, width: w, height: h, role: 'img',
      'aria-label': 'Satellite pass elevation timeline, ' + state.hours + ' hour window' });
    [0, 30, 60, 90].forEach(function (el) {
      root.appendChild(svg('line', { x1: M.l, x2: w - M.r, y1: yOf(el), y2: yOf(el), stroke: '#30363d', 'stroke-width': 1 }));
      root.appendChild(svg('text', { x: M.l - 4, y: yOf(el) + 3, 'text-anchor': 'end', fill: '#8b949e', 'font-size': 10 }, el + '\u00b0'));
    });
    var step = state.hours <= 4 ? 0.5 : state.hours <= 8 ? 1 : state.hours <= 12 ? 2 : 4;
    var first = Math.ceil(start / (step * 3600e3)) * step * 3600e3;
    for (var t = first; t < end; t += step * 3600e3) {
      var x = xOf(t);
      root.appendChild(svg('line', { x1: x, x2: x, y1: M.t, y2: M.t + plotH, stroke: '#21262d', 'stroke-width': 1 }));
      root.appendChild(svg('text', { x: x, y: h - 8, 'text-anchor': 'middle', fill: '#8b949e', 'font-size': 10 }, hhmm(t)));
    }
    var seen = {}, shown = 0;
    state.passes.forEach(function (p) {
      var r = +new Date(p.rise), s = +new Date(p.set);
      if (s < start || r > end) return;
      var key = p.satellite + '|' + p.rise;
      if (seen[key]) return; // same pass listed for several receivers
      seen[key] = 1; shown++;
      var c = colour(p, now);
      var g = svg('g', { class: 'pt-pass', tabindex: 0 });
      var d = arc(p, xOf, yOf);
      g.appendChild(svg('path', { d: d + 'L' + xOf(s).toFixed(1) + ' ' + yOf(0) + 'L' + xOf(r).toFixed(1) + ' ' + yOf(0) + 'Z', fill: c, 'fill-opacity': 0.12, stroke: 'none' }));
      g.appendChild(svg('path', { d: d, fill: 'none', stroke: c, 'stroke-width': 2 }));
      var pk = Math.max(r, peakMs(p));
      var lx = Math.min(Math.max(xOf(pk), M.l + 18), w - M.r - 18);
      var lab = svg('text', { x: lx, y: yOf(p.max_elevation || 0) - 4, 'text-anchor': 'middle', fill: c, 'font-size': 10 }, p.satellite);
      if ((p.max_elevation || 0) >= 25 || state.hours <= 4) g.appendChild(lab);
      g.appendChild(svg('title', {}, p.satellite + ' ' + hhmm(r) + '-' + hhmm(s) + ' max ' + (p.max_elevation || 0).toFixed(1) + '\u00b0' + (clipped(p) ? ' (in progress: start clipped to prediction time)' : '')));
      root.appendChild(g);
    });
    if (now >= start && now <= end) {
      root.appendChild(svg('line', { x1: xOf(now), x2: xOf(now), y1: M.t, y2: M.t + plotH, stroke: '#58a6ff', 'stroke-width': 1.5, 'stroke-dasharray': '4 3' }));
      root.appendChild(svg('text', { x: xOf(now), y: M.t - 3, 'text-anchor': 'middle', fill: '#58a6ff', 'font-size': 10 }, 'now'));
    }
    box.textContent = '';
    box.appendChild(root);
    host.querySelector('.pt-note').textContent = shown
      ? shown + ' pass' + (shown === 1 ? '' : 'es') + ' in window. Curves are approximated from rise, peak, set and max elevation. Passes already under way start at the prediction time.'
      : 'No passes in this window.';
    host.dataset.shown = String(shown);
    host.querySelector('.pt-hours').textContent = state.hours + ' h';
  }
  function build() {
    var host = document.getElementById('pass-timeline');
    if (!host || host.dataset.built) return;
    host.dataset.built = '1';
    host.innerHTML = '<div class="pt-bar"><b>Pass timeline</b><span class="pt-ctl">' +
      '<button type="button" class="chip" data-pt="in" aria-label="Zoom in">+</button>' +
      '<span class="pt-hours muted"></span>' +
      '<button type="button" class="chip" data-pt="out" aria-label="Zoom out">\u2212</button>' +
      '<button type="button" class="chip" data-pt="back" aria-label="Earlier">\u25c0</button>' +
      '<button type="button" class="chip" data-pt="fwd" aria-label="Later">\u25b6</button>' +
      '<button type="button" class="chip" data-pt="reset">now</button></span></div>' +
      '<div class="pt-chart"></div><p class="pt-note muted"></p>';
    host.addEventListener('click', function (e) {
      var b = e.target.closest('[data-pt]'); if (!b) return;
      var i = HOURS.indexOf(state.hours), a = b.dataset.pt;
      if (a === 'in' && i > 0) state.hours = HOURS[i - 1];
      else if (a === 'out' && i < HOURS.length - 1) state.hours = HOURS[i + 1];
      else if (a === 'back') state.offsetH -= state.hours / 2;
      else if (a === 'fwd') state.offsetH += state.hours / 2;
      else if (a === 'reset') { state.offsetH = 0; }
      render();
    });
    var lastW = 0;
    if (window.ResizeObserver) new ResizeObserver(function () { if (host.clientWidth && host.clientWidth !== lastW) { lastW = host.clientWidth; render(); } }).observe(host);
    else window.addEventListener('resize', render);
  }
  window.renderPassTimeline = function (passes) {
    build();
    state.passes = passes || [];
    render();
  };
  window.__passTimelineState = state;
})();
