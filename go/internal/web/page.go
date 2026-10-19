package web

// indexPage is the browser UI: passes table, satellites table, captures and
// SatNOGS observations, refreshed from the JSON API every few seconds.
const indexPage = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>sdrstream</title>
<style>
  :root { color-scheme: dark; }
  body { font-family: ui-monospace, Menlo, monospace; margin: 0; background: #0b0e14; color: #d7dee8; }
  header { padding: 14px 22px; background: #111827; border-bottom: 1px solid #1f2937; display: flex; gap: 18px; align-items: baseline; }
  header h1 { font-size: 16px; margin: 0; letter-spacing: 2px; color: #7dd3fc; }
  header .meta { font-size: 12px; color: #6b7280; }
  nav { margin-left: auto; display: flex; gap: 10px; font-size: 12px; }
  nav span { padding: 3px 8px; border: 1px solid #1f2937; border-radius: 12px; }
  nav .live { color: #34d399; border-color: #065f46; }
  main { padding: 18px 22px; max-width: 1100px; margin: 0 auto; }
  section { margin-bottom: 30px; }
  h2 { font-size: 13px; text-transform: uppercase; letter-spacing: 1.5px; color: #93c5fd; margin: 0 0 10px; }
  table { width: 100%; border-collapse: collapse; font-size: 12px; }
  th, td { text-align: left; padding: 5px 9px; border-bottom: 1px solid #1f2937; white-space: nowrap; }
  th { color: #6b7280; font-weight: 400; }
  tr:hover td { background: #111827; }
  .soon td { color: #fbbf24; }
  .miss { color: #6b7280; }
  .sat-status-active { color: #34d399; }
  .sat-status-decayed { color: #6b7280; }
  #msg { font-size: 12px; color: #6b7280; margin: 8px 0; min-height: 15px; }
  .bar { height: 3px; background: #1f2937; }
  .bar > i { display: block; height: 3px; background: #7dd3fc; }
</style>
</head>
<body>
<header>
  <h1>SDRSTREAM</h1>
  <span class="meta">satellite receiver fleet</span>
  <nav>
    <span id="pass-count">0 passes</span>
    <span id="capture-count">0 captures</span>
    <span class="live">LIVE</span>
  </nav>
</header>
<main>
  <div id="msg">loading…</div>
  <section>
    <h2>Upcoming passes</h2>
    <table id="passes">
      <thead><tr><th>satellite</th><th>receiver</th><th>rise</th><th>peak</th><th>set</th><th>max el</th></tr></thead>
      <tbody></tbody>
    </table>
  </section>
  <section>
    <h2>Satellites</h2>
    <table id="sats">
      <thead><tr><th>name</th><th>norad</th><th>downlink</th><th>mode</th><th>status</th><th>receivers</th></tr></thead>
      <tbody></tbody>
   </table>
  </section>
  <section>
    <h2>Captures</h2>
    <table id="captures">
      <thead><tr><th>satellite</th><th>receiver</th><th>start</th><th>samples</th><th>rate</th><th>file</th></tr></thead>
      <tbody></tbody>
    </table>
  </section>
</main>
<script>
const fmtTime = iso => new Date(iso).toLocaleTimeString([], {hour12:false});
const fmtDate = iso => new Date(iso).toLocaleString([], {hour12:false});
const el = id => document.getElementById(id);
const row = cells => { const tr = document.createElement('tr'); cells.forEach(c => { const td = document.createElement('td'); td.textContent = c; tr.appendChild(td); }); return tr; };

async function refreshStatus() {
  try {
    const status = await (await fetch('/api/status.json')).json();
    el('pass-count').textContent = status.passes.length + ' passes';
    el('capture-count').textContent = status.captures.length + ' captures';
    const passesBody = el('passes').querySelector('tbody');
    passesBody.textContent = '';
    status.passes.slice(0, 25).forEach(p => {
      const soon = new Date(p.rise) - Date.now() < 15 * 60 * 1000;
      const tr = row([p.satellite, shortURL(p.receiver), fmtTime(p.rise), fmtTime(p.peak), fmtTime(p.set), p.max_elevation.toFixed(1) + '°']);
      if (soon) tr.className = 'soon';
      passesBody.appendChild(tr);
    });
    if (!status.passes.length) passesBody.appendChild(row(['no passes predicted', '', '', '', '', '']));
    const capturesBody = el('captures').querySelector('tbody');
    capturesBody.textContent = '';
    status.captures.slice(-15).reverse().forEach(c => {
      capturesBody.appendChild(row([c.satellite, shortURL(c.receiver), fmtDate(c.start), c.samples, c.rate + ' Hz', c.path]));
    });
    if (!status.captures.length) capturesBody.appendChild(row(['none yet — run a capture', '', '', '', '', '']));
    el('msg').textContent = 'updated ' + fmtTime(status.updated_at) + ' — ' + status.receivers + ' receivers in catalogue';
  } catch (err) {
    el('msg').textContent = 'status unavailable: ' + err;
  }
}

function shortURL(u) { try { return new URL(u).host; } catch { return u; } }

async function refreshSats() {
  try {
    const sats = await (await fetch('/api/satellites.json')).json();
    const body = el('sats').querySelector('tbody');
    body.textContent = '';
    sats.forEach(s => {
      const tr = row([s.name, s.norad_id, (s.downlink_hz / 1e6).toFixed(4) + ' MHz', s.mode, s.status, s.receivers]);
      tr.className = 'sat-status-' + s.status;
      body.appendChild(tr);
    });
  } catch (err) { /* satellites list is static; leave as-is */ }
}

refreshStatus(); refreshSats();
setInterval(refreshStatus, 5000);
</script>
</body>
</html>
`
