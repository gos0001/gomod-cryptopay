---
description: Write or audit this project's cryptopay webhook receiver — signature verification, deduplication, and idempotent fulfilment
---

Write or audit the cryptopay webhook receiver in **this** project. Read the
`cryptopay` skill and `references/webhooks.md` first; this command applies them in
the project's own language and framework.

## 1. Find the ground truth

- Is there already a receiver? Read it before writing anything, and audit rather
  than replace.
- Which framework and router? The raw-body question is answered differently in
  each, and getting it wrong is the single most common cause of a receiver that
  rejects every genuine event.
- Where does the webhook secret live, and is that file gitignored?
- Which table records an order's payment, and does it already have somewhere to
  record a processed event id?

## 2. The four checks, in this order

Verify **before** parsing. A body you have not authenticated is attacker input.

1. **Signature over the raw body.** HMAC-SHA256 of `"<timestamp>.<body>"` with
   `webhook.secret`, compared against `X-Cryptopay-Signature` minus its `sha256=`
   prefix. The bytes must be the ones that arrived — re-serialising parsed JSON
   changes them and the signature will not match.
2. **Constant-time comparison.** `hmac.compare_digest`, `crypto.timingSafeEqual`,
   `hmac.Equal`. Never `==` on the hex.
3. **Timestamp freshness**, a few minutes. This is what the timestamp is inside the
   signature for: without the check, a captured request stays valid forever.
4. **Event id not seen before.** `X-Cryptopay-Event-Id`, unique index in your own
   table.

Framework notes worth stating explicitly, because each has a trap:

- **Express** — `express.raw({type: 'application/json'})` on this route, or capture
  `req.rawBody` in `express.json`'s `verify` hook. `JSON.stringify(req.body)` never
  verifies.
- **FastAPI / Flask** — `await request.body()` / `request.get_data()`, not the
  parsed model.
- **Rails** — `request.raw_post`.
- **Go** — read the body once into a slice, then hand that slice to both the
  verifier and the decoder.

## 3. Handling the events

- `invoice.confirmed` is the only event that means paid. Fulfil here.
- `invoice.detected` means seen on chain, not final. Show progress; release
  nothing.
- `invoice.reverted` means a detected transfer vanished in a reorg. Undo any
  "payment seen" state; the invoice is payable again.

`pay_amount` in the payload is in smallest units. Compare as an integer against
what you expected; divide by `10^decimals` only for display.

Make the effect idempotent, not just the recording. `invoice.confirmed` twice must
not ship twice, and `detected → reverted → detected` is a normal sequence.

## 4. Answering

- `2xx` fast. Queue the slow work; anything past `webhook.timeout` (10s by default)
  is treated as failed and redelivered.
- On rejection, return a short body saying why — it lands in `last_error` and is
  what an operator reads when deliveries stop.
- Never `2xx` an event you could not verify. Silent acceptance of a forged event is
  worse than a retry storm.

## 5. Test it, do not assume it

- Verify with a **known-good** case: compute the signature independently, with a
  different implementation from the one in the receiver, and check they agree.
- Then the negative cases, each of which must be rejected: tampered body, wrong
  secret, stale timestamp, absent signature header.
- Then a duplicate delivery of the same event id: exactly one fulfilment.

Report what you wrote, which framework-specific raw-body mechanism you used and
why, and what remains untested — a real payment is the only thing that proves the
path end to end.
