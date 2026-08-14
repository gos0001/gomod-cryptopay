# gomod-cryptopay

[![CI](https://github.com/gos0001/gomod-cryptopay/actions/workflows/ci.yml/badge.svg)](https://github.com/gos0001/gomod-cryptopay/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/tag/gos0001/gomod-cryptopay?label=release&sort=semver)](https://github.com/gos0001/gomod-cryptopay/pkgs/container/gomod-cryptopay)
[![Go Reference](https://pkg.go.dev/badge/github.com/gos0001/gomod-cryptopay.svg)](https://pkg.go.dev/github.com/gos0001/gomod-cryptopay)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

```bash
docker pull ghcr.io/gos0001/gomod-cryptopay:1
```

Crypto payment service in a container. It issues invoices, watches TRON and BSC
for incoming token transfers, matches them to invoices, and notifies your backend
over signed webhooks.

**Watch-only: there are no private keys anywhere in this service.** It observes
one receiving address per network — yours — and never moves funds. Losing the
whole database loses bookkeeping, not money. One binary, one config file, one
Postgres; it creates its own database and tables on start.

---

## Install

```yaml
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD:?}
    volumes: [postgres_data:/var/lib/postgresql/data]
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U postgres"]
      interval: 5s
      retries: 5

  cryptopay:
    image: ghcr.io/gos0001/gomod-cryptopay:1
    ports: ["8080:8080"]
    volumes:
      - type: bind
        source: ./config.json
        target: /etc/cryptopay/config.json
        read_only: true
        # Without this, a missing config.json is created as an empty *directory*
        # and the container fails on "not a file" — an error that sends you
        # looking in the wrong place.
        bind: {create_host_path: false}
    restart: unless-stopped
    depends_on:
      postgres: {condition: service_healthy}

volumes:
  postgres_data:
```

Start from [`config.example.json`](config.example.json), fill in `postgres.dsn`,
`api.keys`, your receiving addresses and at least one asset, then:

```bash
docker compose up -d
curl -H "X-Api-Key: $KEY" localhost:8080/api/v1/assets
```

Check a configuration without starting anything — no database needed:

```bash
docker run --rm -v ./config.json:/c.json:ro \
  ghcr.io/gos0001/gomod-cryptopay:1 -check-config -config /c.json
```

---

## Three things to know

**1. The config file must be mounted.** There are no environment variables at
all — not one, so there is nothing to fall back to when the file is missing. The
container exits immediately, naming the path it looked at:

```
config: no configuration file at "/etc/cryptopay/config.json" (pass -config to point somewhere else)
```

**2. An invoice is identified by its amount, not by an address.** There is one
receiving address per network, so what distinguishes two invoices is a unique
amount:

```
pay_amount = requested_amount + nonce * asset.step
```

A transfer of `V` settles the invoice when `pay_amount <= V < pay_amount + step`.
The upper bound is exclusive because `pay_amount + step` is, by construction,
some other invoice's amount.

Show the payer **`pay_amount`, never `amount`** — a transfer of the requested
figure falls outside the window. Anything outside it, underpayment included,
becomes a row in `cp_orphan_transfers` for manual reconciliation; nothing is ever
silently dropped.

An amount stays reserved for `invoices.amount_hold` after its invoice ends,
because a transfer sent just before expiry can land well after it and must not
pay whichever invoice inherited the amount.

**3. Amounts are decimal strings, never JSON numbers.** You send `"10.50"` in
whole tokens; the API answers with both `pay_amount` (`"10.5001"`) and
`pay_amount_units` (`"10500100"`, smallest units). Internally every figure is an
integer number of smallest units — `NUMERIC(78,0)` in Postgres, `big.Int` in Go.
A JSON number is a float64, and an 18-decimal amount does not survive one.

---

## Endpoints

Everything under `/api/v1` requires `X-Api-Key`; a wrong or missing key is `401`.
`/healthz` is open. Every response is `{"data": ...}` or `{"error": ...}`.

| Method | Path | Does |
|---|---|---|
| `POST` | `/api/v1/invoices` | create an invoice — `201`, or `200` with the same invoice when `external_id` repeats |
| `GET` | `/api/v1/invoices` | list, filtered and cursor-paginated |
| `GET` | `/api/v1/invoices/:id` | one invoice by id |
| `POST` | `/api/v1/invoices/:id/cancel` | cancel while still `pending` |
| `GET` | `/api/v1/assets` | the configured assets, with their `step` and decimals |
| `GET` | `/api/v1/orphans` | transfers that matched no invoice |
| `GET` | `/healthz` | liveness; no key |

`POST /api/v1/invoices` is the one endpoint that can also be opened to callers
with no key, for creation straight from a customer's browser — see
[Creating from a browser](#creating-from-a-browser).

### Creating an invoice

```bash
curl -X POST localhost:8080/api/v1/invoices \
  -H "X-Api-Key: $KEY" -H 'Content-Type: application/json' \
  -d '{"network":"tron","symbol":"USDT","amount":"10.50","external_id":"order-42"}'
```

```json
{"data":{"invoice":{
  "id":"9f1c…","external_id":"order-42",
  "network":"tron","symbol":"USDT","decimals":6,
  "pay_address":"TWd4…","pay_amount":"10.5001","pay_amount_units":"10500100",
  "amount":"10.5","status":"pending","confirmations":0,
  "created_at":"2026-08-14T12:00:00Z","expires_at":"2026-08-14T12:30:00Z"
}}}
```

The first invoice at a given amount takes nonce 0, so its `pay_amount` equals
`amount`; the next one at the same amount is offset by `step`. Render
`pay_amount` either way — that the two sometimes coincide is exactly what makes
reading `amount` a bug that works in testing.

| Field | Required | Notes |
|---|---|---|
| `network` | yes | `tron` or `bsc` |
| `symbol` | yes¹ | `contract_address` is the alternative when two contracts share a symbol |
| `contract_address` | yes¹ | |
| `amount` | yes | decimal string in whole tokens |
| `external_id` | no | your own key, and the **idempotency key** — repeating it returns the same invoice instead of a second one |
| `expires_in` | no | duration string such as `"45m"`; defaults to `invoices.ttl`, capped at 24h |
| `description` | no | ≤ 500 chars |
| `metadata` | no | any JSON object, ≤ 8 KiB, stored and echoed back |

¹ one of the two.

`GET /api/v1/invoices` accepts `status`, `network`, `asset_id`, `external_id`,
`created_from`, `created_to`, `limit` and `cursor`, and returns `next_cursor`
when more rows follow.

### Creating from a browser

Set `public_api.invoice_create` and list your site in `cors.allowed_origins`, and
the page can create an invoice with no key in the JavaScript:

```js
const r = await fetch('https://pay.example.com/api/v1/invoices', {
  method: 'POST',
  headers: {'Content-Type': 'application/json'},
  body: JSON.stringify({network: 'tron', symbol: 'USDT', amount: '10.50'}),
});
const {data} = await r.json();
show(data.invoice.pay_address, data.invoice.pay_amount);   // never data.invoice.amount
```

**Only creation is opened.** A browser cannot list invoices, read one back, cancel
one, or see orphan transfers — those stay behind `X-Api-Key`, which belongs on
your server and nowhere else. So the page shows the address and amount, and the
**status comes from your own backend**, which learns it from the webhook. There is
no public endpoint to poll, deliberately: it would let anyone walk your invoices.

Three fields are refused or ignored without a key, and each for a reason worth
knowing:

| Field | Public request | Why |
|---|---|---|
| `external_id` | `400` | it is the idempotency key: repeating a merchant's `order-42` would return that invoice in full — id, amounts, description, metadata. A read of somebody else's order dressed as a write |
| `metadata` | `400` | up to 8 KiB of arbitrary JSON per invoice; a browser has nothing to put there |
| `expires_in` | ignored | an anonymous caller could otherwise hold an amount for a day |

**CORS is not access control.** An origin list governs who may *read* responses,
not who may send a request: anything that is not a browser — `curl` included —
ignores it entirely. Once `public_api.invoice_create` is on, the endpoint is open
to whoever can reach it, and what bounds abuse is the rate limit below, not the
origin list. That is also why nothing but creation is opened.

The limit exists for a specific reason: each invoice consumes a nonce, an asset
has `nonce_max` of them, and a used amount stays reserved for `ttl +
amount_hold` after the invoice ends. Unlimited anonymous creation would exhaust
the amount space and deny payment to real customers — that, not disk, is the
risk.

If your page is behind a reverse proxy, set `app.trusted_proxies`. Without it the
limit counts every request against the proxy's own address; with every proxy
trusted — which is what gin does when the setting is empty of meaning — an
attacker resets their bucket by sending a different `X-Forwarded-For`.

---

## Invoice status

```
pending ──► detected ──► confirmed
   │            │
   │            └──► pending          (reorg: the transfer was un-mined)
   ├──► confirmed                     (watcher's first sight is already final)
   ├──► expired
   └──► cancelled
```

`confirmed`, `expired` and `cancelled` are terminal. Two edges are deliberate:

- **`pending → confirmed` skips `detected`** — after downtime a watcher's first
  sight of a transfer can already be past the confirmation threshold.
- **`detected → expired` does not exist.** Once money is on chain, the clock must
  not void the invoice.

---

## Webhooks

The destination is **`webhook.url` in your configuration**, not a field in the
invoice-creation request. This is a self-hosted module with one receiver; taking a
URL from a caller would have the service posting wherever it was pointed, and
would show your receiver's domain to anyone holding an API key.

Events are written to an outbox in the same transaction as the status change they
describe, so a receiver that is down delays notifications — it never loses them.
Retries are exponential from `backoff_base` to `backoff_max`, up to
`max_attempts`.

| Event | When |
|---|---|
| `invoice.detected` | a matching transfer is on chain, not yet confirmed |
| `invoice.confirmed` | the transfer reached `confirmations` / finality |
| `invoice.reverted` | a detected transfer disappeared in a reorg; the invoice is `pending` again |

```json
{"event":"invoice.confirmed","invoice_id":"9f1c…","status":"confirmed",
 "network":"tron","symbol":"USDT","pay_amount":"10500100","decimals":6}
```

`pay_amount` in the payload is in **smallest units** — the payload is for
machines, not for display.

| Header | Carries |
|---|---|
| `X-Cryptopay-Signature` | `sha256=<hex>` |
| `X-Cryptopay-Timestamp` | Unix seconds, part of the signed string |
| `X-Cryptopay-Event` | event name |
| `X-Cryptopay-Event-Id` | UUID, stable across retries — use it to deduplicate |
| `X-Cryptopay-Attempt` | 1-based attempt number |
| `X-Cryptopay-Api-Key` | `webhook.api_key`, if set — convenience for a gateway, **not** a substitute for the signature |

### Verifying

The signature covers `"<timestamp>.<body>"`. The timestamp is inside it on
purpose: signing the body alone would leave a captured request valid forever.
Reject stale timestamps and a replay dies with them.

```go
import "github.com/gos0001/gomod-cryptopay/pkg/webhook"

ts := r.Header.Get(webhook.HeaderTimestamp)
if !webhook.Verify(secret, ts, r.Header.Get(webhook.HeaderSignature), body) {
    http.Error(w, "bad signature", http.StatusUnauthorized)
    return
}
```

`Verify` compares in constant time. Copy it rather than writing `==` against a
recomputed hex string.

Answer `2xx` quickly and do your work afterwards: a receiver slower than
`webhook.timeout` is treated as failed and retried.

---

## Configuration

One JSON file. `-config` points at it, `config.json` by default. JSON has no
comments, so this section is where the meaning of each setting lives —
`config.example.json` only gives the shape. An unknown key is a startup warning,
not a failure. A missing file is fatal.

### `app`, `log`

| Key | Default | Purpose |
|---|---|---|
| `app.addr` | `":8080"` | listen address. The image's `HEALTHCHECK` targets port 8080 — move the listener and you must override the healthcheck too |
| `app.trusted_proxies` | `[]` | IPs or CIDRs whose `X-Forwarded-For` may be believed. Empty trusts none, so the client address is the socket's peer. Behind nginx, list nginx — otherwise every request is attributed to the proxy and the public rate limit counts them as one client |
| `log.level` | `"info"` | `debug`, `info`, `warn`, `error` |
| `log.format` | `"json"` | `json`, or `console` for development |

### `postgres`

| Key | Default | Purpose |
|---|---|---|
| `dsn` | — | **required** |
| `auto_create` | `true` | create the database named in the DSN if it is absent, connecting to `postgres` to do it |
| `auto_schema` | `true` | apply the embedded schema at every start, under a `pg_advisory_lock` |

There is no migration step and no migration files: `schema/schema.sql` is compiled
into the binary and every statement in it is idempotent.

### `api`

| Key | Default | Purpose |
|---|---|---|
| `keys` | — | **required**, at least one; each ≥ 24 characters. Compared in constant time |

Rotate by listing both keys, moving callers, then removing the old one.

### `cors`

Off by default: a deployment that never talks to a browser sends no CORS headers
at all.

| Key | Default | Purpose |
|---|---|---|
| `allowed_origins` | `[]` | exact origins, scheme and port included. `https://shop.example.com` does not cover `http`, a subdomain, or `:8443`. A trailing slash is refused at startup rather than silently never matching |
| `allow_all_origins` | `false` | answer every origin. Safe only because this API never uses cookies — it authenticates with a header, so a hostile page gains nothing its own server could not do |
| `max_age` | `"12h"` | how long a browser may cache a preflight. Every uncached preflight is an extra round trip |

`Access-Control-Allow-Credentials` is never sent, and there is no setting to turn
it on. With credentials allowed, a page could act with a visitor's ambient
session; header authentication has no such ambient state, which is the property
worth keeping.

### `public_api`

| Key | Default | Purpose |
|---|---|---|
| `invoice_create` | `false` | allow `POST /api/v1/invoices` with no key. Nothing else is opened |
| `rate_per_minute` | `30` | per client address, public requests only. A backend holding a key is never throttled |
| `burst` | `10` | how many may arrive at once before the rate applies |

Off by default on purpose: pulling a newer image must not change who may write to
your database. See [Creating from a browser](#creating-from-a-browser) for what
the public path refuses and why.

### `invoices`

| Key | Default | Purpose |
|---|---|---|
| `ttl` | `"30m"` | how long a new invoice stays payable; ≤ 24h |
| `amount_hold` | `"2h"` | how long a used amount stays reserved after the invoice ends. Must comfortably exceed the slowest transfer you expect to arrive late |
| `expire_interval` | `"1m"` | how often invoices are expired and holds released. **Zero unschedules the job**, and amounts then stay reserved forever — the safe direction to fail |
| `pay_address_tron` | — | your receiving address; required if any TRON asset is configured |
| `pay_address_bsc` | — | likewise for BSC |

### `assets` (array)

| Key | Default | Purpose |
|---|---|---|
| `network` | — | `tron` or `bsc` |
| `symbol` | — | display name |
| `contract_address` | — | the token contract; BSC addresses are stored lowercased |
| `decimals` | — | 6 for TRC20 USDT, 18 for BEP-20 USDT |
| `step` | 4 decimal places | the uniquifying increment, **a decimal string in smallest units**. `"100"` at 6 decimals is 0.0001 USDT |
| `nonce_max` | `1000` | concurrent invoices per asset; the amount space is `step * nonce_max` wide |

`step` must be large enough that no two invoices collide and small enough that
the offset is negligible to the payer. Coarser `step` × `nonce_max` = a wider
band of amounts you must be able to accept.

### `tron`

Budget-driven: one address per network is what makes a 100k-requests/day TronGrid
key sufficient. Steady state is ~2 requests per poll regardless of how many
transfers arrive, because a payment's block is cached at first sight and
confirmations are then arithmetic against a single chain-head query.

| Key | Default | Purpose |
|---|---|---|
| `api_url` | `"https://api.trongrid.io"` | |
| `api_key` | `""` | TronGrid key; without one the anonymous rate limit applies |
| `daily_request_budget` | `100000` | local ceiling; the client refuses further calls rather than being throttled |
| `qps` | `10` | pacing; TronGrid's hard limit is 15 |
| `timeout` | `"15s"` | |
| `watch_interval` | `"5s"` | poll period. **Zero switches the network off** |
| `stale_after` | `"5m"` | a detected payment quiet for this long is logged for attention |

### `bsc`

| Key | Default | Purpose |
|---|---|---|
| `rpc_urls` | — | **required**; used round-robin, so a rate-limited provider is one of several. `bsc-dataseed.*` will not serve `eth_getLogs` — use a provider that does |
| `log_range` | `2000` | blocks per `eth_getLogs` call; public nodes cap this |
| `use_finalized_tag` | `true` | treat the `finalized` tag as final instead of counting confirmations |
| `confirmations` | `15` | used when `use_finalized_tag` is false |
| `reorg_depth` | `64` | how far back each tick re-scans to notice a vanished transfer |
| `timeout` | `"20s"` | |
| `failure_cooldown` | `"30s"` | how long a failing endpoint is skipped |
| `watch_interval` | `"5s"` | poll period. **Zero switches the network off** |

Reorgs are detected by absence — a log that was there and no longer is — because
`eth_getLogs` returns canonical logs only and never reports `removed`.

### `webhook`

Leave `url` empty and notifications are off entirely; nothing is queued.

| Key | Default | Purpose |
|---|---|---|
| `url` | `""` | your receiver |
| `secret` | — | **required whenever `url` is set**, ≥ 16 chars. `openssl rand -hex 32` |
| `api_key` | `""` | sent as `X-Cryptopay-Api-Key` |
| `timeout` | `"10s"` | per attempt |
| `interval` | `"10s"` | how often the outbox is drained. Zero unschedules delivery |
| `batch_size` | `50` | rows per drain |
| `concurrency` | `4` | deliveries in flight at once |
| `max_attempts` | `10` | after which an event stops being retried |
| `backoff_base` | `"10s"` | |
| `backoff_max` | `"1h"` | |
| `retention` | `"168h"` | how long delivered and exhausted rows are kept |

A URL without a secret is refused at startup rather than warned about: an
unsigned webhook is indistinguishable from a forgery, and your receiver has no
way to notice that signing was never switched on.

### `bootstrap`, `cron`

| Key | Default | Purpose |
|---|---|---|
| `bootstrap.timeout` | `"60s"` | budget for startup tasks — schema, asset seeding |
| `cron.shutdown_timeout` | `"15s"` | how long in-flight jobs get on shutdown |

The authoritative list of sections is `cmd/checkconfig.go`.

---

## Claude Code plugin

A plugin for integrating this service into **your** project — not for developing
this repository:

```
/plugin marketplace add gos0001/gomod-cryptopay
/plugin install cryptopay@cryptopay
```

It carries the integration contract as a skill (amount matching, what the public
path refuses, webhook verification) plus two commands: `/cryptopay-add` adds the
service to a project, and `/cryptopay-webhook` writes or audits the receiver in
that project's own framework.

## Development

```bash
make tools          # air, wire, sqlc, golangci-lint
make docker-up      # postgres only; the service creates its own database
make generate       # sqlc THEN wire — the order is load-bearing
make dev            # air hot reload against config.development.json
make test           # go test ./... -race -count=1
make check-config   # validate the config file without starting anything
```

The BSC watcher is developed against a local Hardhat node, not a testnet: a
testnet cannot be made to reorganise on demand, and `detected → pending` is the
branch that handles a payment being un-mined.

```bash
make hardhat-install
make hardhat-up     # in one terminal
make hardhat-seed   # deploys a mock 18-decimal USDT and sends a transfer
```

Point `bsc.rpc_urls` at `http://localhost:8545` for this, and swap one line for a
production provider when you are done.

Images are multi-arch by cross-compilation rather than emulation:

```bash
make image                      # single arch, into the local daemon
make image-push VERSION=v1.0.0  # linux/amd64,linux/arm64 to GHCR
```

## License

MIT — see [LICENSE](LICENSE).
