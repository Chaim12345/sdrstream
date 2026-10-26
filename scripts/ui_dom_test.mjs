// DOM-state tests for the activity/health/proof renderers in
// go/internal/web/static/app.js. Runs the real app.js in a vm context
// with a minimal DOM and asserts on the rendered element tree: text
// content, escaping (markup in source data must stay text), and
// timestamp handling (zero-value times must read "no successful fetch
// yet", never a bogus age). Run: node scripts/ui_dom_test.mjs
import { readFileSync } from 'node:fs';
import vm from 'node:vm';

function makeEl(tag = 'div') {
  const el = {
    tagName: String(tag).toUpperCase(), children: [], dataset: {}, style: {},
    _text: '', className: '', id: '', hidden: false, value: '',
    classList: { add() {}, remove() {}, toggle() {}, contains() { return false; } },
    appendChild(c) { this.children.push(c); if (c) { c.parentElement = this; if (c.id) byId[c.id] = c; } return c; },
    append(...cs) { cs.forEach(c => { if (c && typeof c === 'object') this.appendChild(c); }); },
    insertBefore(c, ref) {
      const i = ref ? this.children.indexOf(ref) : -1;
      if (i < 0) this.children.push(c); else this.children.splice(i, 0, c);
      if (c) { c.parentElement = this; if (c.id) byId[c.id] = c; } return c;
    },
    after(...cs) { const p = this.parentElement; if (!p) return; const i = p.children.indexOf(this); cs.forEach((c, k) => { if (c && typeof c === 'object') p.insertBefore(c, p.children[i + 1 + k] || null); }); },
    before(...cs) { const p = this.parentElement; if (!p) return; cs.forEach(c => { if (c && typeof c === 'object') p.insertBefore(c, this); }); },
    addEventListener() {}, removeEventListener() {},
    setAttribute(k, v) { (this.attrs ||= {})[k] = v; },
    getAttribute(k) { return (this.attrs || {})[k] ?? null; },
    removeAttribute() {}, click() {}, focus() {},
    remove() { const p = this.parentElement; if (!p) return; const i = p.children.indexOf(this); if (i >= 0) p.children.splice(i, 1); this.parentElement = null; },
    querySelector() { return makeEl(); },
    querySelectorAll() { return []; },
    set textContent(v) { this._text = String(v); this.children = []; },
    get textContent() { return this._text + this.children.map(c => c.textContent).join(''); },
  };
  return el;
}

const byId = {};
const nullIds = new Set(['health-sources']); // created by renderSourceFreshness, not present in index.html
const document = {
  getElementById(id) {
    if (byId[id]) return byId[id];
    if (nullIds.has(id)) return null;
    // Auto-created stubs stand in for real index.html elements: give
    // them a parent so parentElement.after/insertBefore work.
    const e = makeEl();
    document.body.appendChild(e);
    return (byId[id] = e);
  },
  createElement(t) { return makeEl(t); },
  querySelectorAll() { return []; },
  querySelector() { return makeEl('meta'); },
  addEventListener() {},
  body: makeEl('body'),
  documentElement: makeEl('html'),
  head: makeEl('head'),
};
const sandbox = {
  document, console, URL, Date, Math, JSON, Promise, Set, Map, Array, Object, String, Number, Boolean, RegExp, Error, Intl,
  setTimeout, clearTimeout, setInterval, clearInterval,
  location: { href: 'http://localhost/', hash: '' },
  history: { replaceState() {} },
  localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
  navigator: {},
  fetch: async () => { throw new Error('network disabled in dom harness'); },
};
sandbox.window = { matchMedia: () => ({ addEventListener() {} }), addEventListener() {} };
sandbox.globalThis = sandbox;
vm.createContext(sandbox);
vm.runInContext(readFileSync(new URL('../go/internal/web/static/app.js', import.meta.url), 'utf8'), sandbox);
process.on('unhandledRejection', () => {}); // top-level refreshes fail without network; irrelevant here

function texts(node, out = []) { if (node._text) out.push(node._text); node.children.forEach(c => texts(c, out)); return out; }
function tags(node, out = []) { out.push(node.tagName); node.children.forEach(c => tags(c, out)); return out; }
let failures = 0;
function check(name, cond) { console.log((cond ? 'ok   ' : 'FAIL ') + name); if (!cond) failures++; }

// --- activityCard: provenance framing ---
let card = sandbox.activityCard({ started: new Date().toISOString(), frequency_hz: 14491234, level_db: 12, kind: 'sstv', mode: 'sstv', source: 'http://kiwi.example.com:8073' }, false);
check('card with source names the receiver', texts(card).some(t => t === 'sstv · via kiwi.example.com:8073'));
check('card keeps measured freq/level', texts(card).some(t => t.includes('14.491')) && texts(card).some(t => t.includes('12 dB')));

card = sandbox.activityCard({ started: new Date().toISOString(), kind: 'voice' }, false);
check('card without source says source not recorded', texts(card).includes('source not recorded'));
check('card without freq renders no invented frequency', !texts(card).some(t => /MHz|GHz/.test(t)));

card = sandbox.activityCard({ started: new Date().toISOString(), kind: 'data', mode: 'aprs', source: 'http://rx.example', verification: 'gate: decode matches expected structure' }, false);
check('verified card shows recorded gate verdict verbatim', texts(card).includes('live gate passed: gate: decode matches expected structure'));

card = sandbox.activityCard({ started: new Date().toISOString(), kind: 'data', historical: true, source: 'somestation' }, false);
check('historical card keeps verification-unknown line', texts(card).includes('historical - verification unknown (no gate proof recorded)'));

card = sandbox.activityCard({ started: new Date().toISOString(), kind: 'cw', rejected: 'text failed plausibility' }, false);
check('rejected card states the gate failure', texts(card).includes('failed the live quality gate: text failed plausibility'));

// Precedence must match ui-features' activityGateVerdict: rejected >
// historical > passed. A repeated RecordGate can leave both fields set.
card = sandbox.activityCard({ started: new Date().toISOString(), kind: 'cw', rejected: 'late gate failure', historical: true }, false);
check('rejected+historical shows the rejection, not historical', texts(card).includes('failed the live quality gate: late gate failure') && !texts(card).some(t => t.startsWith('historical -')));
card = sandbox.activityCard({ started: new Date().toISOString(), kind: 'cw', rejected: 'late gate failure', verification: 'earlier pass note' }, false);
check('rejected+verification shows the rejection, not the pass', texts(card).includes('failed the live quality gate: late gate failure') && !texts(card).some(t => t.startsWith('live gate passed')));
if (typeof sandbox.activityGateVerdict === 'function') {
  check('card verdict agrees with filter helper on both-fields item',
    sandbox.activityGateVerdict({ rejected: 'x', historical: true }) === 'rejected' &&
    sandbox.activityGateVerdict({ rejected: 'x', verification: 'y' }) === 'rejected' &&
    sandbox.activityGateVerdict({ historical: true }) === 'historical' &&
    sandbox.activityGateVerdict({ verification: 'y' }) === 'passed');
}

card = sandbox.activityCard({ started: new Date().toISOString(), kind: 'data', source: 'http://<img src=x onerror=alert(1)>/' }, false);
check('markup in source stays text (no IMG node)', !tags(card).includes('IMG'));
check('markup in source appears as literal text', texts(card).some(t => t.includes('<img')));

// --- renderSourceFreshness: honest per-source state from metrics.json ---
delete byId['health-sources'];
sandbox.renderSourceFreshness({ feeds: { sources: [
  { name: 'wspr.live', state: 'live', last_ok: new Date(Date.now() - 5 * 60 * 1000).toISOString() },
  { name: 'sondehub', state: 'error', last_ok: '0001-01-01T00:00:00Z', last_error: 'upstream 503: <img src=x onerror=alert(1)>' },
  { name: 'adsb.lol', state: 'disabled' },
] } });
const box = document.getElementById('health-sources');
check('health container created inside panel', !!box && byId['panel-health'].children.includes(box));
check('live source shows truthful age', texts(box).includes('last successful fetch 5m ago'));
check('zero-time reads no successful fetch yet', texts(box).filter(t => t === 'no successful fetch yet').length === 2);
check('zero-time never renders a bogus age', !texts(box).some(t => /^\d{5,}d ago|last successful fetch \d{4,}/.test(t)));
check('source error shown verbatim as text', texts(box).includes('upstream 503: <img src=x onerror=alert(1)>') && !tags(box).includes('IMG'));
check('states rendered verbatim', ['live', 'error', 'disabled'].every(st => texts(box).includes(st)));

sandbox.renderSourceFreshness({ feeds: { sources: [] } });
check('no sources stated plainly', texts(box).includes('no live data sources recorded since boot'));

// --- renderReceiverProof: receiver badge never silently omitted ---
sandbox.renderReceiverProof([
  { source: 'receiver', label: 'NOAA 19', provider_time: new Date().toISOString(), evidence: 'verified', capture_source: 'live', receiver: 'http://kiwi.example:8073' },
  { source: 'receiver', label: 'APT clip', provider_time: new Date().toISOString(), evidence: 'unverified', capture_source: '', receiver: '' },
]);
const rx = byId['rxproof-list'];
check('proof with receiver names it', texts(rx).includes('kiwi.example:8073'));
check('proof without receiver says not recorded', texts(rx).includes('receiver not recorded'));
check('proof with unknown capture source says so', texts(rx).includes('provenance: source not recorded'));
check('proof with live capture source names sdrstream recorder and external receiver', texts(rx).includes('provenance: live capture recorded by this sdrstream (from the receiver named above)'));
check('proof does not claim receiver is recorder', !texts(rx).includes('provenance: recorded by this receiver'));


// --- renderActivity summary: ui-features' verdict filter surfaces when present ---
// Pre-integration state: no activityVerdict declared anywhere.
delete byId['activity-summary'];
sandbox.renderActivity();
let summary = byId['activity-summary'].textContent;
check('summary without verdict filter omits gate wording', !/gate (passed|rejected|verdict)/.test(summary));
// Post-integration state: the variable exists (declared by msglog's
// `let` on combined, injected here on standalone).
if (vm.runInContext('typeof activityVerdict', sandbox) === 'undefined') vm.runInContext('var activityVerdict = "";', sandbox);
vm.runInContext('activityVerdict = "passed";', sandbox);
sandbox.renderActivity();
summary = byId['activity-summary'].textContent;
check('summary with verdict=passed says gate passed', summary.includes('filtered by gate passed'));
vm.runInContext('activityVerdict = "historical";', sandbox);
sandbox.renderActivity();
check('summary with verdict=historical says historical / no gate proof', byId['activity-summary'].textContent.includes('historical / no gate proof'));
vm.runInContext('activityVerdict = "";', sandbox);
sandbox.renderActivity();
check('summary with verdict cleared omits gate wording', !/gate (passed|rejected|verdict)/.test(byId['activity-summary'].textContent));

console.log(failures ? `\n${failures} FAILURES` : '\nall dom tests passed');
process.exit(failures ? 1 : 0);
