# Receiving webhooks

The destination is `webhook.url` in cryptopay's own configuration. It is not a
field on an invoice and cannot be set per request — a self-hosted service has one
receiver, and accepting a URL from a caller would let anyone point it anywhere.

Events are written to an outbox in the same transaction as the status change they
describe, so a receiver that is down delays notifications and never loses them.

## Events

| Event | Meaning |
|---|---|
| `invoice.detected` | a matching transfer is on chain, not yet final |
| `invoice.confirmed` | final — **this is the only one that means paid** |
| `invoice.reverted` | a detected transfer vanished in a reorg; the invoice is `pending` again |

```json
{"event":"invoice.confirmed","invoice_id":"9f1c…","status":"confirmed",
 "network":"tron","symbol":"USDT","pay_amount":"10500100","decimals":6}
```

`pay_amount` here is in **smallest units** — the payload is for machines. Divide by
`10^decimals` only for display, and never to compare.

## Headers

| Header | Carries |
|---|---|
| `X-Cryptopay-Signature` | `sha256=<hex>` |
| `X-Cryptopay-Timestamp` | Unix seconds, part of the signed string |
| `X-Cryptopay-Event` | event name |
| `X-Cryptopay-Event-Id` | UUID, stable across retries — deduplicate on this |
| `X-Cryptopay-Attempt` | 1-based attempt number |
| `X-Cryptopay-Api-Key` | `webhook.api_key` if set; convenience for a gateway, **not** a substitute for the signature |

## Verifying

The signature covers `"<timestamp>.<body>"`. The timestamp is inside it on purpose:
signing the body alone would leave a captured request valid forever. Reject stale
timestamps and a replay dies with them.

Four things must all be true, and the order matters — verify before parsing:

1. The **raw** body is used, byte for byte. Re-serialising parsed JSON changes the
   bytes and the signature will not match.
2. The comparison is constant-time. A plain `==` on hex leaks position through
   timing.
3. The timestamp is within a few minutes of now.
4. The event id has not been handled before.

Go — copy `Verify` rather than reimplementing it:

```go
import "github.com/gos0001/gomod-cryptopay/pkg/webhook"

body, _ := io.ReadAll(r.Body)
ts := r.Header.Get(webhook.HeaderTimestamp)

if !webhook.Verify(secret, ts, r.Header.Get(webhook.HeaderSignature), body) {
    http.Error(w, "bad signature", http.StatusUnauthorized)
    return
}
```

Any language, the same rule set:

```python
import hmac, hashlib, time

def verify(secret: str, timestamp: str, signature: str, body: bytes) -> bool:
    if not signature.startswith("sha256="):
        return False
    if abs(time.time() - int(timestamp)) > 300:      # replay window
        return False
    mac = hmac.new(secret.encode(), f"{timestamp}.".encode() + body, hashlib.sha256)
    return hmac.compare_digest(signature[7:], mac.hexdigest())
```

```js
const crypto = require('crypto');

function verify(secret, timestamp, signature, rawBody) {
  if (!signature?.startsWith('sha256=')) return false;
  if (Math.abs(Date.now() / 1000 - Number(timestamp)) > 300) return false;
  const mac = crypto.createHmac('sha256', secret)
    .update(timestamp + '.').update(rawBody).digest('hex');
  return crypto.timingSafeEqual(Buffer.from(signature.slice(7)), Buffer.from(mac));
}
```

In Express, that needs the raw body: `express.raw({type: 'application/json'})` on
this route, or `express.json({verify: (req, _res, buf) => { req.rawBody = buf }})`.
`JSON.stringify(req.body)` will not verify.

## Answering

- Any `2xx` is success. Anything else is retried with exponential backoff from
  `backoff_base` to `backoff_max`, up to `max_attempts`.
- **Answer quickly, work afterwards.** A receiver slower than `webhook.timeout`
  (10s by default) is treated as failed and the event is redelivered — so slow
  fulfilment turns into duplicate fulfilment unless you are idempotent.
- Return a body on rejection: it is stored in `last_error` and is what an operator
  will read when deliveries stop working.

## Idempotency

Redelivery is normal: a timeout, a deploy, a 500 all produce it. Record
`X-Cryptopay-Event-Id` in your own table with a unique index and ignore a repeat.

Also make the *effect* idempotent. `invoice.confirmed` arriving twice must not ship
twice, and `invoice.detected` may be followed by `invoice.reverted` and then
`invoice.detected` again — a reorg is not a fault, it is a chain doing what chains
do.

## Local testing

Point `webhook.url` at anything that echoes, verify the signature yourself with the
snippet above, and check the outbox: `delivered_at` filled on success,
`attempts` and `last_error` on failure. Kill the receiver mid-test — the event must
survive and arrive on a later attempt.
