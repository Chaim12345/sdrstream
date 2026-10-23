/* Read-only metadata search. No implicit polling; each page is a fresh snapshot. */
(function () {
  'use strict';
  const $ = id => document.getElementById(id);
  const labels = {
    'live-verified': 'live source - gate passed',
    'live-unverified': 'live source - no recorded gate pass',
    'live-redecode': 'live capture re-decode - not fresh reception',
    synthetic: 'test fixture / synthetic - not a reception',
    foreign: 'foreign origin - not this receiver',
    unknown: 'source unknown - not verified as live reception'
  };
  let request = 0, offset = 0, next = null, active = null;
  function filters() {
    return {q: $('history-query').value.trim(), 'source-class': $('history-class').value,
      mode: $('history-mode').value.trim(), redecode: $('history-redecode').value};
  }
  function card(item) {
    const node = document.createElement('article'); node.className = 'card-activity history-result';
    const heading = document.createElement('div'); heading.className = 'row';
    const badge = document.createElement('span'); badge.className = 'badge';
    badge.textContent = item.rejected ? 'recorded gate rejection: ' + item.rejected : labels[item.source_class] || labels.unknown;
    heading.appendChild(badge); node.appendChild(heading);
    const meta = document.createElement('div'); meta.className = 'muted';
    meta.textContent = item.store + ' · ' + (item.mode || 'mode unknown') + ' · ' + fmtDate(item.started);
    node.appendChild(meta);
    if (item.receiver) { const via = document.createElement('div'); via.className = 'muted'; via.textContent = 'via ' + item.receiver; node.appendChild(via); }
    if (item.name) { const name = document.createElement('div'); name.className = 'history-name'; name.textContent = item.name; node.appendChild(name); }
    if (item.text) {const text = document.createElement('pre');text.className = 'history-text';text.textContent = item.text;node.appendChild(text);}
    if (item.verification || item.rejected) { const gate = document.createElement('div'); gate.className = 'muted';
      gate.textContent = item.rejected ? 'recorded gate rejection: ' + item.rejected : 'recorded decoder verdict: ' + item.verification;
      node.appendChild(gate);
    }
    if (item.redecode) { const from = document.createElement('div');from.className='muted';from.textContent='re-decoded from '+item.decoded_from;node.appendChild(from); }
    // The API returns local store links; refuse schemes or unexpected paths.
    if (typeof item.url === 'string' && /^\/(audio|gallery)\/[^/]+$/.test(item.url)) {
      const a = document.createElement('a');a.href=item.url;a.textContent='Open saved file';a.className='chip';node.appendChild(a);
    }
    return node;
  }
  async function search(at, fresh) {
    const id = ++request;
    if (fresh || !active) active = filters();
    const query = new URLSearchParams({...active, limit:'50', offset:String(at)});
    $('history-state').textContent = 'Searching saved metadata…';
    $('history-results').setAttribute('aria-busy','true');
    $('history-prev').hidden = true; $('history-next').hidden = true;
    try {
      const page = await fetchJSON('/api/history/search?' + query);
      if (id !== request) return;
      offset = page.offset; next = page.next_offset;
      const box = $('history-results'); box.textContent = '';
      if (!page.items.length) setEmpty(box, page.available ? 'No saved record matches these filters. Change the search or reset filters.' : 'No capture, decode or gallery metadata is available yet.');
      page.items.forEach(item => box.appendChild(card(item)));
      $('history-state').textContent = 'Showing ' + (page.items.length ? offset + 1 : 0) + '-' + (offset + page.items.length) + ' of ' + page.total + ' matching records · ' + page.available + ' available';
      $('history-scope').textContent = page.scope + ' Paging reads a fresh snapshot; use Search again to refresh.';
      $('history-prev').hidden = !offset; $('history-next').hidden = next === null;
    } catch (err) {
      if (id !== request) return;
      $('history-results').textContent = '';
      $('history-state').textContent = 'History search unavailable: ' + (err.message || err) + '. Press Search history to retry.';
      $('history-scope').textContent = '';
    } finally { if (id === request) $('history-results').setAttribute('aria-busy','false'); }
  }
  $('history-form').addEventListener('submit', e => {e.preventDefault();search(0,true);});
  $('history-next').addEventListener('click', () => {if(next !== null) search(next,false);});
  $('history-prev').addEventListener('click', () => search(Math.max(0,offset-50),false));
  $('history-reset').addEventListener('click', () => {$('history-form').reset();search(0,true);});
})();
