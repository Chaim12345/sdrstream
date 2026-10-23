'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const { PAGE, select, counts } = require('../../go/internal/web/static/msglog.js');
const rows = Array.from({ length: 123 }, (_, i) => ({ mode: i % 2 ? 'cw' : 'rtty', detail: 'Message ' + i }));
test('honest 50-message page size and total counts', () => {
  assert.equal(PAGE, 50);
  assert.equal(select(rows, '', '').length, 123);
  assert.equal(select(rows, '', '').slice(-PAGE).length, 50);
  assert.deepEqual(Array.from(counts(rows)), [['rtty', 62], ['cw', 61]]);
});
test('detail search combines with decoder selection, trimmed case-insensitive literal match', () => {
  assert.equal(select(rows, 'cw', '  MESSAGE 1 ').length, 17);
  assert.equal(select(rows, 'rtty', 'Message 122')[0].detail, 'Message 122');
  assert.equal(select(rows, 'gone', '').length, 0);
  assert.equal(select(rows, '', '.*').length, 0);
});
test('empty data and reset do not mutate source history or lose zero/missing detail', () => {
  assert.equal(select([], '', '').length, 0);
  assert.equal(select(rows, '', '').length, rows.length);
  assert.equal(select([{mode: 'cw'}], '', 'x').length, 0);
  assert.equal(rows[0].detail, 'Message 0');
});
