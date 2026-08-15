# Deploying cryptopay

One container, one JSON file, one Postgres. No environment variables at all —
which is the first thing that surprises people: there is nothing to fall back to
if the file is not mounted, and the container exits naming the path it looked at.

```
config: no configuration file at "/etc/cryptopay/config.json" (pass -config to point somewhere else)
```

## Compose

```yaml
services:
  cryptopay:
    image: ghcr.io/gos0001/gomod-cryptopay:1.2.1
    ports: ["8080:8080"]
    volumes:
      - type: bind
        source: ./cryptopay.json
        target: /etc/cryptopay/config.json
        read_only: true
        bind: {create_host_path: false}
    restart: unless-stopped
    depends_on:
      postgres: {condition: service_healthy}
```

`create_host_path: false` matters. Without it a missing source file is created as
an empty **directory**, the container fails on "not a file", and the error sends
you looking in the wrong place — the short `./a:/b:ro` form does the same.

`postgres` can be the database your project already runs: cryptopay creates its
**own database** inside that server (`postgres.auto_create`) and applies its own
schema (`postgres.auto_schema`). Point `postgres.dsn` at a database name nothing
else uses. There is no migration step to run, ever.

## Minimum viable config

```json
{
  "postgres": {"dsn": "postgres://user:pass@postgres:5432/cryptopay?sslmode=disable"},
  "api": {"keys": ["<openssl rand -hex 32>"]},
  "invoices": {
    "pay_address_tron": "T...",
    "pay_address_bsc": "0x..."
  },
  "assets": [
    {"network": "tron", "symbol": "USDT",
     "contract_address": "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t",
     "decimals": 6, "step": "100", "nonce_max": 1000}
  ],
  "tron": {"api_key": "<trongrid key>"},
  "webhook": {
    "url": "https://your-backend/internal/cryptopay",
    "secret": "<openssl rand -hex 32>"
  }
}
```

Only `pay_address_*` for networks you actually list in `assets` are required.

Validate without a database, which is what makes it usable in CI:

```bash
docker run --rm -v ./cryptopay.json:/c.json:ro \
  ghcr.io/gos0001/gomod-cryptopay:1.2.1 -check-config -config /c.json
```

## The settings that carry consequences

**`invoices.pay_address_*`** — the address you watch. Wrong address means every
transfer is invisible; there is no key here to reveal the mistake.

**`webhook.secret`** is required whenever `webhook.url` is set, and refused if
absent rather than warned about: an unsigned webhook is indistinguishable from a
forgery and your receiver cannot tell that signing was never switched on.

**A zero interval disables a job.** `tron.watch_interval: "0s"` switches TRON off
entirely — that is the off switch, there is no separate enable flag. Same for
`bsc.watch_interval` and `webhook.interval`. `invoices.expire_interval: "0s"`
stops expiry, and amounts then stay reserved forever: the safe direction to fail,
but not a state to leave running.

**`bsc.rpc_urls`** is used round-robin. `bsc-dataseed.*` refuses `eth_getLogs` at
every range, so it cannot be used here no matter how healthy it looks — pick a
provider that serves logs.

**`tron.api_key`** raises the anonymous rate limit. The design fits inside
100k requests/day: roughly two requests per poll regardless of transfer volume.

**`app.trusted_proxies`** must list your reverse proxy if there is one, and
otherwise stay empty. It decides whether `X-Forwarded-For` is believed, and that
header is what the per-address rate limit on public invoice creation keys on.

## Operational notes

- Health: `GET /healthz`, no key. The image's `HEALTHCHECK` targets port 8080; if
  you change `app.addr`, override the healthcheck too.
- Logs: JSON by default; `log.format: "console"` for local work.
- The config file holds the API keys, the TronGrid key and the DSN. Mount it,
  never bake it into an image, and keep it out of git.
- An unknown key in the file is a startup warning, not a failure — read the first
  lines of the log after editing it.
- `docker run <image> version` prints the build and commit without an entrypoint
  override.
