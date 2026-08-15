---
name: cryptopay
description: Integrate gomod-cryptopay, a self-hosted crypto payment service, into this project. Use when adding crypto payments, USDT/TRC20/BEP-20 acceptance, invoices, or payment webhooks to a service that uses or could use cryptopay; when the project references ghcr.io/gos0001/gomod-cryptopay, cp_invoices, X-Cryptopay-Signature, or a cryptopay config.json; or when the user mentions cryptopay by name. Covers deploying the container, the unique-amount matching scheme, creating invoices from your backend, and verifying webhooks.
---

# cryptopay

A crypto payment service you run as a container. It answers one question:
**has this invoice been paid.** Orders, carts, subscriptions and refunds stay in
the service that owns them.

Image: `ghcr.io/gos0001/gomod-cryptopay:1.2.1` · Source:
https://github.com/gos0001/gomod-cryptopay

## What it does, and what it does not

Does: issues invoices, watches TRON and BSC for incoming token transfers, matches
them to invoices by amount, tracks confirmations, and posts signed webhooks.

Does **not**: hold keys, move funds, sweep balances, pay anyone out, convert
between assets, or price anything. It is **watch-only** — it observes one
receiving address per network, which is yours, and can do nothing else with it.
Losing its database loses bookkeeping, not money.

## The one idea to understand first

**There are no per-invoice addresses.** One receiving address per network, so what
identifies an invoice is a unique amount:

```
pay_amount = requested_amount + nonce * asset.step
```

A transfer of `V` settles the invoice when `pay_amount <= V < pay_amount + step`.
Everything outside that window — underpayment included — is filed as an orphan
transfer for a human, never applied to an invoice.

Full reasoning, and what to set `step` to: `references/amount-matching.md`.

## Rules that must not be broken

1. **Show the payer `pay_amount`, never `amount`.** `amount` is what was
   requested; a transfer of it lands outside the window and is not credited. The
   first invoice at a given amount gets nonce 0, where the two figures are
   identical — which is exactly why reading the wrong one passes testing and fails
   on the second customer.

2. **Treat every amount as a decimal string.** `"10.5001"`, or
   `pay_amount_units` for smallest units. Never parse into a float and never
   re-serialise as a JSON number: an 18-decimal amount does not survive float64,
   and a rounded amount misses the matching window.

3. **Give cryptopay its own database.** Not its own tables in yours. It creates
   the database and applies its schema on start.

4. **Never `JOIN` or foreign-key into `cp_*`.** Store `invoice_id` as a plain
   column in your own table. Separate databases make it impossible anyway, which
   is the point.

5. **This is a server-to-server API — the key never leaves your backend.** Every
   endpoint is authenticated, invoice creation included, and the service belongs on
   an internal network. A customer's browser talks to *your* backend, which creates
   the invoice and hands back the address and amount. Shipping `api.keys` to a page
   would grant every visitor the right to list, cancel and reconcile.

6. **Verify the webhook signature before believing anything in the body.** HMAC
   over `"<timestamp>.<body>"`, compared in constant time, with stale timestamps
   rejected. Details and a copyable implementation: `references/webhooks.md`.

7. **Deduplicate on `X-Cryptopay-Event-Id`.** Redelivery is normal, not a fault.
   Make your handler idempotent; the same event id may arrive twice.

8. **`invoice.confirmed` is the only event that means paid.** `invoice.detected`
   means seen on chain and not yet final — never release goods on it. A detected
   payment can be reverted by a reorg, and `invoice.reverted` says so.

9. **Underpayment is not payment.** It becomes an orphan transfer. There is no
   partial credit and no automatic refund; both are decisions for a human.

10. **A cancelled or expired invoice can still receive money.** The amount stays
    reserved for `amount_hold` after the invoice ends precisely because a transfer
    sent before expiry can land after it. Handle a late orphan; do not assume an
    expired invoice is closed business.

## What to read next

| File | When |
|---|---|
| `references/deploy.md` | adding the service to a compose file or cluster; writing its config |
| `references/amount-matching.md` | choosing `step` and `nonce_max`, or explaining why a transfer was not credited |
| `references/integrate.md` | creating invoices, showing the payer what to send, and modelling status in your own schema |
| `references/webhooks.md` | writing or debugging the receiver |

## Commands

- `/cryptopay-add` — add the service to this project: compose entry, config file,
  the column that references an invoice.
- `/cryptopay-webhook` — write or audit the webhook receiver in this project's
  language and framework.
