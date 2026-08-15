---
description: Add the cryptopay payment service to this project — compose service, config file, and the order column that references an invoice
---

Add cryptopay to **this** project. Read the `cryptopay` skill first for the
integration contract; this command applies it.

You are working in someone's own repository, not in cryptopay's. Nothing is
overwritten without saying so first.

## 1. Look before writing

Establish by reading, not assuming:

- Is there a `docker-compose.yml` (or `compose.yaml`)? Which services exist, and is
  there already a `postgres`?
- Where do this project's secrets live, and is that file gitignored? cryptopay's
  config carries the API keys, the TronGrid key and a DSN — if the candidate
  location is tracked by git, say so and stop.
- What migration tool does the project use, and where do its files live? You will
  need one migration for the order column.
- Which table represents an order, purchase, or subscription? That is what gains
  the invoice reference.
- Is cryptopay already present? If so, report what is configured and stop.

## 2. Ask what you cannot read

These cannot be guessed, and a wrong guess is money in the wrong place:

- **The receiving address per network.** Watch-only: the operator's own wallet.
- **Which networks and tokens.** TRON and/or BSC; contract address and decimals.
- **The TronGrid API key**, if TRON is used.
- **A BSC RPC URL that serves `eth_getLogs`.** Say plainly that
  `bsc-dataseed.*` does not, whatever its uptime looks like.
- **The webhook URL** on this project's backend, and whether that route exists yet.

Generate the API key and the webhook secret yourself (`openssl rand -hex 32`) —
those are not questions.

## 3. Write

**Config file** — one JSON file, no environment variables. Sections: `postgres`,
`api`, `invoices`, `assets`, `tron`/`bsc`, `webhook`. `postgres.dsn` names a
database nothing else uses; cryptopay creates it and its schema on start.

**Compose service** — image `ghcr.io/gos0001/gomod-cryptopay:1.2.1`, the config
bind-mounted read-only with `bind: {create_host_path: false}`, `depends_on` the
existing postgres with `condition: service_healthy`. Publish 8080 only if the
browser will reach it directly; otherwise leave it on the internal network.

**Migration** — in the project's own tool, in its own style:

```sql
ALTER TABLE <orders> ADD COLUMN cryptopay_invoice_id UUID;
ALTER TABLE <orders> ADD COLUMN paid_at TIMESTAMPTZ;
```

No foreign key, no `JOIN`: cryptopay has its own database, and coupling the two is
the mistake this design exists to prevent.

**Webhook receiver** — if the route does not exist, create it and verify the
signature before parsing the body, in this project's language. `/cryptopay-webhook`
does this properly; call it rather than writing a second half-version here.

## 4. Report

State what you wrote, what you generated, and what the user must still do:

- put the real addresses and keys into the config file
- confirm the config path is not tracked by git
- `docker compose up -d`, then check `GET /healthz` and
  `GET /api/v1/assets` with the key
- validate first if they prefer: `-check-config` needs no database

Do not claim it works end to end. Nothing has been paid yet, and a payment is the
only proof.
