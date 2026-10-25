// Tests for the diag-relay Cloudflare Worker. Run: node --test workers/diag-relay/worker.test.mjs
// No live calls: global fetch is stubbed; the real Resend API is never hit.
import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import worker from './worker.js';

const ENV = {
  CLIENT_ID: 'test-client',
  RESEND_API_KEY: 're_test_key',
  DIAG_INBOX: 'diag@inbox.example',
  FROM_ADDR: 'relay@sender.example',
};

let mailCalls;
beforeEach(() => {
  mailCalls = [];
  globalThis.fetch = async (url, init) => {
    mailCalls.push({ url, init });
    return new Response('{"id":"m1"}', { status: 200 });
  };
});

function post(body, headers = {}) {
  return worker.fetch(new Request('https://relay.example/diag', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'CF-Connecting-IP': '203.0.113.7', ...headers },
    body: typeof body === 'string' ? body : JSON.stringify(body),
  }), ENV);
}

test('rejects wrong path and non-POST', async () => {
  const r1 = await worker.fetch(new Request('https://relay.example/other', { method: 'POST' }), ENV);
  assert.equal(r1.status, 404);
  const r2 = await worker.fetch(new Request('https://relay.example/diag', { method: 'GET' }), ENV);
  assert.equal(r2.status, 404);
  assert.equal(mailCalls.length, 0);
});

test('rejects missing or wrong client id', async () => {
  const noHeader = await post({ text: 'x', website: '' });
  assert.equal(noHeader.status, 403);
  const wrong = await post({ text: 'x', website: '' }, { 'X-Sdrstream-Client': 'nope' });
  assert.equal(wrong.status, 403);
  assert.equal(mailCalls.length, 0);
});

test('honeypot: filled website field gets fake success, no mail', async () => {
  const r = await post({ text: 'hello', website: 'http://spam.example' },
    { 'X-Sdrstream-Client': 'test-client' });
  assert.equal(r.status, 200);
  assert.deepEqual(await r.json(), { ok: true });
  assert.equal(mailCalls.length, 0, 'honeypot must not send mail');
});

test('null, scalar, and array JSON bodies rejected with 400, no crash', async () => {
  for (const raw of ['null', '42', '"text"', '[1,2]', 'true']) {
    const r = await post(raw, { 'X-Sdrstream-Client': 'test-client', 'CF-Connecting-IP': '203.0.113.99' });
    assert.equal(r.status, 400, raw);
  }
  assert.equal(mailCalls.length, 0);
});

test('oversized body rejected with 413', async () => {
  const big = 'x'.repeat(64 * 1024 + 1);
  const r = await post({ text: big, website: '' }, { 'X-Sdrstream-Client': 'test-client' });
  assert.equal(r.status, 413);
  assert.equal(mailCalls.length, 0);
});

test('missing relay config returns 503', async () => {
  const r = await worker.fetch(new Request('https://relay.example/diag', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Sdrstream-Client': 'test-client' },
    body: JSON.stringify({ text: 'diag', website: '' }),
  }), { CLIENT_ID: 'test-client' }); // no RESEND_API_KEY
  assert.equal(r.status, 503);
});

test('happy path forwards to Resend with auth and content', async () => {
  const r = await post({ subject: 'sdrstream diag 2026-10-25 abc', text: 'diag body', website: '' },
    { 'X-Sdrstream-Client': 'test-client' });
  assert.equal(r.status, 200);
  assert.equal(mailCalls.length, 1);
  const call = mailCalls[0];
  assert.equal(call.url, 'https://api.resend.com/emails');
  assert.equal(call.init.headers.Authorization, 'Bearer re_test_key');
  const sent = JSON.parse(call.init.body);
  assert.deepEqual(sent.to, ['diag@inbox.example']);
  assert.equal(sent.from, 'relay@sender.example');
  assert.equal(sent.subject, 'sdrstream diag 2026-10-25 abc');
  assert.equal(sent.text, 'diag body');
});

test('per-IP rate limit: 6th request in a minute gets 429', async () => {
  const h = { 'X-Sdrstream-Client': 'test-client', 'CF-Connecting-IP': '198.51.100.9' };
  for (let i = 0; i < 5; i++) {
    const r = await post({ text: 'd', website: '' }, h);
    assert.equal(r.status, 200, 'request ' + i);
  }
  const r6 = await post({ text: 'd', website: '' }, h);
  assert.equal(r6.status, 429);
  assert.equal(mailCalls.length, 5);
});

test('mail API failure surfaces as 502', async () => {
  globalThis.fetch = async () => new Response('denied', { status: 403 });
  const r = await post({ text: 'd', website: '' },
    { 'X-Sdrstream-Client': 'test-client', 'CF-Connecting-IP': '192.0.2.3' });
  assert.equal(r.status, 502);
});
