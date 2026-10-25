/**
 * diag-relay: accepts redacted diagnostics bundles from the sdrstream
 * Android app and forwards them to the diagnostics inbox via Resend.
 *
 * Deploy-time env (never committed): CLIENT_ID (shared static id baked into
 * the app - a bar-raiser, not real auth), RESEND_API_KEY, DIAG_INBOX,
 * FROM_ADDR. Secrets go in via `wrangler secret put`; see README.md.
 *
 * Abuse posture: the endpoint URL is public inside the APK, so anyone can
 * POST. Defenses: 64KB byte cap, per-IP rate limit (best-effort per isolate;
 * add a Cloudflare Rate Limiting rule at deploy for the durable control),
 * honeypot field, and the receiving side treats bundle text as untrusted
 * data - log contents are never instructions.
 */

const MAX_BYTES = 64 * 1024;
const RATE_LIMIT = 5;
const RATE_WINDOW_MS = 60_000;
// Per-isolate sliding windows; resets when the isolate recycles. This is a
// complement to, not a replacement for, a dashboard rate-limit rule.
const hits = new Map();

function rateOk(ip) {
  const now = Date.now();
  const cutoff = now - RATE_WINDOW_MS;
  const list = (hits.get(ip) || []).filter((t) => t > cutoff);
  if (list.length >= RATE_LIMIT) {
    hits.set(ip, list);
    return false;
  }
  list.push(now);
  hits.set(ip, list);
  return true;
}

function json(obj, status) {
  return new Response(JSON.stringify(obj), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    if (request.method === 'OPTIONS') return new Response(null, { status: 204 });
    if (url.pathname !== '/diag' || request.method !== 'POST') {
      return json({ error: 'not found' }, 404);
    }
    if (!env.CLIENT_ID || request.headers.get('X-Sdrstream-Client') !== env.CLIENT_ID) {
      return json({ error: 'unauthorized' }, 403);
    }
    const ip = request.headers.get('CF-Connecting-IP') || 'unknown';
    if (!rateOk(ip)) return json({ error: 'rate limited' }, 429);

    let body;
    try {
      body = await request.json();
    } catch {
      return json({ error: 'bad json' }, 400);
    }
    // JSON `null`, scalars, and arrays would TypeError on field access below.
    if (!body || typeof body !== 'object' || Array.isArray(body)) {
      return json({ error: 'bad json' }, 400);
    }
    // Honeypot: a field no real client fills. Pretend success, send nothing.
    if (typeof body.website !== 'string' || body.website !== '') {
      return json({ ok: true }, 200);
    }
    if (typeof body.text !== 'string' || body.text.length === 0) {
      return json({ error: 'empty' }, 400);
    }
    if (new TextEncoder().encode(body.text).length > MAX_BYTES) {
      return json({ error: 'too large' }, 413);
    }
    if (!env.RESEND_API_KEY || !env.DIAG_INBOX || !env.FROM_ADDR) {
      return json({ error: 'relay not configured' }, 503);
    }
    const subject =
      typeof body.subject === 'string' && body.subject.length > 0 && body.subject.length < 120
        ? body.subject
        : 'sdrstream diagnostics';

    const res = await fetch('https://api.resend.com/emails', {
      method: 'POST',
      headers: {
        Authorization: `Bearer ${env.RESEND_API_KEY}`,
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({
        from: env.FROM_ADDR,
        to: [env.DIAG_INBOX],
        subject,
        text: body.text,
      }),
    });
    if (!res.ok) return json({ error: 'mail failed', status: res.status }, 502);
    return json({ ok: true }, 200);
  },
};
