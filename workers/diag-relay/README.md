# diag-relay

Receives redacted diagnostics bundles from the sdrstream Android app and
forwards them to the diagnostics inbox via [Resend](https://resend.com).
Runs on the Cloudflare Workers free plan (100,000 requests/day - the app
sends at most one auto-report per device per day, so realistic volume is
orders of magnitude under the cap). Both services are $0 at this volume;
if a cap is ever hit the send fails and the app retries on next launch.

## Endpoint contract

`POST /diag` with header `X-Sdrstream-Client: <CLIENT_ID>` and JSON body:

```json
{"subject": "sdrstream diag 2026-10-25 ab12cd34", "text": "<redacted bundle>", "website": ""}
```

- `website` is a honeypot: any non-empty value gets a fake `200 {"ok":true}`
  and nothing is sent.
- `text` is capped at 64 KB (413 above).
- 5 requests/minute per IP (429 above; per-isolate, see below).

## Deploy (owner steps - NOT done by the repo lanes)

1. `npm install -g wrangler && wrangler login` (free Cloudflare account).
2. Create the worker: `wrangler deploy workers/diag-relay/worker.js --name diag-relay`
   (or paste worker.js into the dashboard editor).
3. Set secrets and vars:
   - `wrangler secret put RESEND_API_KEY` (free Resend key, 100 emails/day, 3,000/month)
   - `wrangler secret put CLIENT_ID` (random string; also baked into the app's
     `DiagUploadAndroid.CLIENT_ID` - a bar-raiser, not real auth)
   - vars `DIAG_INBOX` (diagnostics inbox address) and `FROM_ADDR` (a verified
     Resend sender) in `wrangler.toml` or the dashboard.
4. Recommended: add a Cloudflare Rate Limiting rule on `/diag` (the in-code
   limit is per-isolate and resets on recycle; the dashboard rule is durable).
5. Fill `DiagUploadAndroid.ENDPOINT` + `CLIENT_ID` in the app and cut a
   release. Until those are filled the app-side uploader is a hard no-op.

## Tests

```
node --test workers/diag-relay/worker.test.mjs
```

All mail calls are stubbed; the tests never hit the live Resend API.
