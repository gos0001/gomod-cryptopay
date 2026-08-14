# Creating invoices and tracking them

Two shapes, and they are not alternatives — most projects use both.

## From your backend (the default)

The key lives on your server. Your backend creates the invoice, stores its id
against the order, and shows the payer what came back.

```
POST /api/v1/invoices
X-Api-Key: <api.keys entry>
Content-Type: application/json

{"network": "tron", "symbol": "USDT", "amount": "10.50", "external_id": "order-42"}
```

```json
{"data": {"invoice": {
  "id": "9f1c…", "external_id": "order-42",
  "network": "tron", "symbol": "USDT", "decimals": 6,
  "pay_address": "TWd4…", "pay_amount": "10.5001", "pay_amount_units": "10500100",
  "amount": "10.5", "status": "pending", "confirmations": 0,
  "created_at": "…", "expires_at": "…"
}}}
```

`201` for a new invoice, `200` when the same `external_id` returns the existing
one. **Use your order id as `external_id`** — it makes creation idempotent, so a
retried request cannot produce two invoices for one order.

Show `pay_address` and `pay_amount`. Never `amount`.

| Field | Notes |
|---|---|
| `network` | `tron` or `bsc` |
| `symbol` or `contract_address` | one of the two; `contract_address` when two contracts share a symbol |
| `amount` | decimal string in whole tokens |
| `external_id` | your order id; the idempotency key |
| `expires_in` | duration string, `"45m"`; defaults to `invoices.ttl`, capped at 24h |
| `description`, `metadata` | echoed back; metadata is any JSON up to 8 KiB |

## From the browser

Set `public_api.invoice_create: true` and list the site in
`cors.allowed_origins`. The page then creates an invoice with **no key in the
JavaScript**:

```js
const r = await fetch('https://pay.example.com/api/v1/invoices', {
  method: 'POST',
  headers: {'Content-Type': 'application/json'},
  body: JSON.stringify({network: 'tron', symbol: 'USDT', amount: '10.50'}),
});
const {data} = await r.json();
render(data.invoice.pay_address, data.invoice.pay_amount);
```

Only creation is public. The page **cannot** list invoices, read one back, cancel
one, or see orphan transfers — so the status comes from your own backend, which
learns it from the webhook. There is no public endpoint to poll, on purpose: one
would let anyone enumerate your invoices.

Three fields are refused or ignored without a key:

- `external_id` → `400`. It is the idempotency key, so accepting it anonymously
  would let anyone guess `order-42` and be handed that invoice in full.
- `metadata` → `400`.
- `expires_in` → ignored; the configured TTL applies.

So a browser-created invoice has no `external_id`, and you must carry its `id`
back to your own order yourself — usually by POSTing the returned id to your
backend, or by having the backend create the invoice in the first place. If you
need the order link to be trustworthy, create it from the backend.

An origin list is not access control: `curl` ignores CORS entirely. What bounds
abuse of the public endpoint is `public_api.rate_per_minute` per client address —
and that keying only works if `app.trusted_proxies` is set correctly behind a
proxy.

## Status, and what to do at each one

```
pending ──► detected ──► confirmed
   │            └──► pending      (reorg)
   ├──► confirmed                 (first sight already final)
   ├──► expired
   └──► cancelled
```

| Status | Meaning | Your move |
|---|---|---|
| `pending` | awaiting a transfer | show the address and amount, count down `expires_at` |
| `detected` | seen on chain, not final | show "payment seen, confirming" — **do not release goods** |
| `confirmed` | final | fulfil the order |
| `expired` | TTL passed with no transfer | close the attempt, offer a new invoice |
| `cancelled` | cancelled through the API | as above |

`confirmed`, `expired` and `cancelled` are terminal. Note what is missing:
`detected → expired` does not exist, because once money is on chain the clock must
not void the invoice.

## In your own schema

Store the invoice id as a plain column. Never a foreign key into `cp_invoices`,
never a `JOIN` — cryptopay owns a separate database.

```sql
ALTER TABLE orders
  ADD COLUMN cryptopay_invoice_id UUID,
  ADD COLUMN paid_at TIMESTAMPTZ;
```

Keep your own `paid_at` rather than reading status back on every page load: the
webhook tells you once, and your database is where your order lives.

## Reading invoices from the backend

- `GET /api/v1/invoices/:id` — one invoice.
- `GET /api/v1/invoices?status=pending&limit=50&cursor=…` — filtered by `status`,
  `network`, `asset_id`, `external_id`, `created_from`, `created_to`; returns
  `next_cursor` while more rows follow.
- `POST /api/v1/invoices/:id/cancel` — only while `pending`.
- `GET /api/v1/orphans` — transfers that matched nothing. Worth surfacing to
  whoever handles support: this is where a customer's mistyped amount ends up.

Polling is a fallback, not the design. Use the webhook.
