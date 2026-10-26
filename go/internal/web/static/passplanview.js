/* Persisted observation state is separate from orbital predictions and RF proof. */
(function () {
  'use strict';
  function describe(data) {
    if (!data || data.enabled !== true) return { note: 'Observation planner is not configured.', rows: [] };
    var info = data.info || {}, rows = Array.isArray(data.observations) ? data.observations : [];
    var note = 'Planning only. Scheduled is not a capture or verified reception. ';
    note += info.observer === 'receiver' ? 'Geometry uses the selected receiver location. ' : 'Geometry uses the configured station location. ';
    if (info.error) note += 'Planner refresh error: ' + info.error;
    else if (info.last_refresh && !String(info.last_refresh).startsWith('0001-')) note += 'Last refresh: ' + info.last_refresh;
    else note += 'Waiting for cached orbital elements.';
    return { note: note, rows: rows };
  }
  function render(data, error) {
    var host = document.getElementById('observation-plan');
    if (!host) return;
    host.textContent = '';
    var h = document.createElement('h2'); h.textContent = 'Observation plan'; host.appendChild(h);
    var d = describe(data), p = document.createElement('p'); p.className = 'muted';
    p.textContent = error ? 'Planner unavailable: ' + error + '. No current plan state verified.' : d.note;
    host.appendChild(p);
    if (error) return;
    if (!d.rows.length) { var e = document.createElement('div'); e.className = 'empty'; e.textContent = 'No planned observations.'; host.appendChild(e); }
    d.rows.forEach(function (o) {
      var card = document.createElement('div'); card.className = 'card';
      var title = document.createElement('b'); title.textContent = (o.satellite || 'Unnamed satellite') + ' - ' + (o.status || 'unknown'); card.appendChild(title);
      var meta = document.createElement('div'); meta.className = 'card-meta';
      meta.textContent = 'Task ' + (o.task_start || 'unknown') + ' to ' + (o.task_end || 'unknown') + ' | peak ' + (Number.isFinite(o.peak_elevation) ? o.peak_elevation.toFixed(1) + ' degrees' : 'unknown') + ' | resource: ' + (o.resource || 'not assigned');
      card.appendChild(meta);
      if (o.detail) { var detail = document.createElement('p'); detail.textContent = o.detail; card.appendChild(detail); }
      host.appendChild(card);
    });
  }
  window.SDRPassplan = { describe: describe, render: render };
})();
