'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync('go/internal/web/static/app.js', 'utf8');
const pure = source.split('// BEGIN history export helpers')[1].split('// END history export helpers.')[0];
const context = {};
vm.createContext(context); vm.runInContext(pure.slice(pure.indexOf('\n')), context);
const { csvCell, activityCSV, activityGateVerdict } = context;
test('formula prefixes including hidden control and whitespace are literal', () => {
  for (const p of ['=', '+', '-', '@', '  =', '\t=', '\r+', '\n@', '\u0000 -', '\u00a0=']) {
    const out = csvCell(p + 'payload');
    assert.ok(out.startsWith("'") || out.startsWith('"\''), JSON.stringify(out));
  }
  assert.equal(csvCell(-12), '-12');
  assert.equal(csvCell('normal'), 'normal');
  assert.equal(csvCell('a,"b"\r\nc'), '"a,""b""\r\nc"');
  assert.equal(csvCell('\tonly text'), "'\tonly text");
});
test('evidence classification never upgrades historical or failed entries', () => {
  assert.equal(activityGateVerdict({verification:'yes'}), 'passed');
  assert.equal(activityGateVerdict({verification:'yes', historical:true}), 'historical');
  assert.equal(activityGateVerdict({verification:'yes', rejected:'no'}), 'rejected');
  assert.equal(activityGateVerdict({}), 'ungated');
});
test('export keeps measured zero, false, unknown blanks and recorded evidence', () => {
  const csv = activityCSV([{level_db:0,bandwidth_hz:0,duration_sec:0,historical:false,asr_fallback:false,
    verification:'measured reason',detail:'=test',source:'receiver',confidence:0}]);
  const [header, row] = csv.split('\r\n').map(l=>l.split(','));
  const value = k=>row[header.indexOf(k)];
  for (const k of ['level_db','bandwidth_hz','duration_sec','confidence']) assert.equal(value(k),'0');
  assert.equal(value('historical'),'false'); assert.equal(value('asr_fallback'),'false');
  assert.equal(value('gate_verdict'),'passed'); assert.equal(value('verification'),'measured reason');
  assert.equal(value('detail'),"'=test"); assert.equal(value('ended'),'');
  assert.equal(activityCSV([]).split('\r\n').length,1);
});
test('combined history filters exclude invalid timestamps in a time window', () => {
  const begin = source.indexOf('function activityHaystack(');
  const end = source.indexOf('function renderModeOptions(', begin);
  Object.assign(context, {activitySearch:'cq', activityMode:'cw', activityKind:'', activityVerdict:'rejected', activityWindowSec:3600,
    mhz: n=>String(n), activityItems:[
      {started:new Date().toISOString(),mode:'cw',detail:'CQ TEST',rejected:'reason'},
      {started:'invalid',mode:'cw',detail:'CQ TEST',rejected:'reason'},
      {started:new Date().toISOString(),mode:'cw',detail:'CQ TEST',verification:'passed'},
      {started:new Date().toISOString(),mode:'rtty',detail:'CQ TEST',rejected:'reason'}]});
  vm.runInContext(source.slice(begin,end),context);
  assert.equal(context.filteredActivity().length,1);
  context.activityVerdict=''; assert.equal(context.filteredActivity().length,2);
  context.activityWindowSec=0; assert.equal(context.filteredActivity().length,3);
});
