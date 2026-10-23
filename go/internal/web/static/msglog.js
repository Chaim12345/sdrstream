/* Decoder history enhancement. Load after app.js. No network requests of its own. */
(function (root) {
  'use strict';
  const PAGE = 50;
  function select(items, mode, search) {
    const needle = search.trim().toLowerCase();
    return items.filter(a => (!mode || a.mode === mode) &&
      (!needle || String(a.detail || '').toLowerCase().includes(needle)));
  }
  function counts(items) {
    const result = new Map();
    items.forEach(a => result.set(a.mode, (result.get(a.mode) || 0) + 1));
    return result;
  }
  const api = { PAGE, select, counts };
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  if (typeof document === 'undefined') return;
  root.SDRMsglog = api;
  const box = document.getElementById('msglog');
  if (!box || typeof decodedItems !== 'function') return;
  let search = '';
  let limit = PAGE;
  let oldMode = msglogMode;
  let lastKey = '';
  let force = true;
  let loadingOlder = false;
  let frozen = null;
  let latestSnapshot = null;
  let currentShown = [];
  const tools = document.createElement('div');
  tools.className = 'toolbar msglog-tools';
  const label = document.createElement('label');
  label.htmlFor = 'msglog-search'; label.textContent = 'Search message text';
  const input = document.createElement('input');
  input.id = 'msglog-search'; input.type = 'search';
  input.placeholder = 'Find text in decoder messages';
  const reset = document.createElement('button');
  reset.type = 'button'; reset.className = 'chip'; reset.textContent = 'Reset filters';
  tools.append(label, input, reset, document.getElementById('msglog-state'));
  box.before(tools);
  const nav = document.createElement('div'); nav.className = 'toolbar msglog-history-nav';
  const older = document.createElement('button'); older.className = 'chip'; older.type = 'button';
  const resume = document.createElement('button'); resume.className = 'chip'; resume.type = 'button';
  resume.textContent = 'Resume latest';
  const note = document.createElement('span'); note.className = 'muted msglog-export-note';
  note.textContent = 'Decoder counts follow search. While paused, paging and copy/export use the frozen loaded snapshot. Resume latest to refresh.';
  nav.append(older, resume, note); box.after(nav);

  function resetPage() { limit = PAGE; force = true; msglogStuck = false; frozen = null; }
  input.addEventListener('input', () => { search = input.value; resetPage(); renderMsglog(); });
  reset.addEventListener('click', () => {
    input.value = ''; search = ''; msglogMode = ''; resetPage(); renderMsglog();
  });
  older.addEventListener('click', () => {
    limit += PAGE; loadingOlder = true; force = true; msglogStuck = true; renderMsglog();
  });
  resume.addEventListener('click', () => { resetPage(); renderMsglog(); });

  function state(total, matched, shown) {
    document.getElementById('msglog-state').textContent = 'Showing ' + shown + ' of ' + matched +
      ' matching messages · ' + total + ' total loaded' + (msglogStuck ? ' · frozen snapshot' : '');
    resume.hidden = !msglogStuck && limit === PAGE;
  }
  // Existing app.js copy/export handlers call this binding at click time.
  msglogText = function () {
    return select(frozen ? frozen.all : decodedItems(), msglogMode, search).map(a =>
      fmtTime(a.started) + ' ' + a.mode + ' ' + (a.frequency_hz ? mhz(a.frequency_hz) : '') + '  ' + a.detail).join('\n');
  };
  renderMsglog = function () {
    if (oldMode !== msglogMode) { oldMode = msglogMode; resetPage(); }
    // API polls recreate objects, and monitor IDs/gallery IDs are not a shared
    // stable namespace. Freeze the whole loaded snapshot, not inferred identity
    // across polls: duplicate rows and server eviction cannot change pagination.
    if (!msglogStuck) frozen = null;
    if (msglogStuck && !frozen && latestSnapshot) frozen = latestSnapshot;
    const all = frozen ? frozen.all : decodedItems();
    const matched = select(all, msglogMode, search);
    const keyed = matched.map((a, i) => ({ item: a, key: String(i) }));
    if (!frozen) latestSnapshot = { all: all.slice() };
    const windowRows = keyed.slice(-limit);
    currentShown = windowRows;
    const shown = windowRows.map(a => a.item);
    // Chips describe decoder totals AFTER detail search, before decoder selection.
    const searched = select(all, '', search);
    const modeCounts = counts(searched);
    // A decoder disappearing must not silently clear the operator's filter.
    if (msglogMode && !modeCounts.has(msglogMode)) modeCounts.set(msglogMode, 0);
    const bar = document.getElementById('msglog-modes');
    const barKey = JSON.stringify([Array.from(modeCounts), msglogMode, search]);
    if (bar.dataset.historyKey !== barKey) {
      bar.dataset.historyKey = barKey; bar.textContent = '';
      function chip(mode, count) {
        const b = document.createElement('button'); b.type = 'button';
        b.className = 'chip' + (mode === msglogMode ? ' on' : '');
        b.setAttribute('aria-pressed', String(mode === msglogMode));
        b.textContent = (mode || 'all') + ' (' + count + ')'; b.dataset.dmode = mode;
        b.addEventListener('click', () => { msglogMode = mode; renderMsglog(); });
        bar.appendChild(b);
      }
      chip('', searched.length);
      Array.from(modeCounts.keys()).sort().forEach(m => chip(m, modeCounts.get(m)));
    }
    const key = JSON.stringify([shown, msglogMode, search, limit]);
    if (key !== lastKey || force) {
      const top = box.scrollTop, height = box.scrollHeight;
      // Keep the visible message anchored when live messages arrive while paused.
      const firstVisible = Array.from(box.children).find(n => n.offsetTop + n.offsetHeight >= box.offsetTop + top);
      const anchorKey = firstVisible && firstVisible.dataset.messageKey;
      const anchorOffset = firstVisible ? firstVisible.offsetTop - top : 0;
      box.textContent = '';
      if (!shown.length) {
        const empty = document.createElement('div'); empty.className = 'empty';
        empty.textContent = !all.length ? 'No decoded messages in the loaded history yet.' :
          'No messages match these filters. Try another decoder or reset filters.';
        box.appendChild(empty);
      }
      windowRows.forEach(({ item: a, key: occurrenceKey }) => {
        const line = document.createElement('div'); line.className = 'msgline ' + (a.kind || '');
        line.dataset.messageKey = occurrenceKey;
        [['msg-time', fmtTime(a.started)], ['msg-mode', a.mode],
          ['msg-freq', a.frequency_hz ? mhz(a.frequency_hz) : ''], ['msg-detail', a.detail]].forEach(([cls, text]) => {
          const span = document.createElement('span'); span.className = cls; span.textContent = text;
          line.appendChild(span);
        });
        box.appendChild(line);
      });
      if (!msglogStuck) box.scrollTop = box.scrollHeight;
      else {
        const anchor = anchorKey && Array.from(box.children).find(n => n.dataset.messageKey === anchorKey);
        box.scrollTop = anchor ? anchor.offsetTop - anchorOffset : loadingOlder ? top + box.scrollHeight - height : top;
      }
      lastKey = key; force = false; loadingOlder = false;
    }
    const olderCount = windowRows.length ? keyed.findIndex(a => a.key === windowRows[0].key) : 0;
    older.hidden = olderCount <= 0;
    older.textContent = 'Load ' + Math.min(PAGE, Math.max(0, olderCount)) + ' older messages';
    reset.disabled = !search && !msglogMode;
    state(all.length, matched.length, shown.length);
  };
  box.addEventListener('scroll', () => {
    if (msglogStuck && !frozen && latestSnapshot) frozen = latestSnapshot;
    if (!msglogStuck) frozen = null;
    const all = frozen ? frozen.all : decodedItems();
    state(all.length, select(all, msglogMode, search).length, box.querySelectorAll('.msgline').length);
  });
  renderMsglog();
})(typeof window === 'undefined' ? globalThis : window);
