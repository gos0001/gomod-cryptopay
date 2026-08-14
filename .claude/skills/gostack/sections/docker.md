# Docker and compose

## Two images

`Dockerfile` — production. Multi-stage: build with the Go toolchain, ship a
static binary on Alpine as a non-root user.

`Dockerfile.dev` — development. Runs air with the source bind-mounted, so edits
rebuild inside the container.

## Compose

`docker compose up -d` starts Postgres, with health checks and
named volumes so data survives a restart. The app service waits on
`service_healthy`, not merely on the container starting — a Postgres container is
up well before it accepts connections.

```bash
make docker-up      # dependencies only, app runs on the host via make dev
make docker-down
```

Running the dependencies in compose while the app runs on the host is the usual
loop: fastest rebuilds, and `.env.development` already points at `localhost`.

## Networking

Inside compose, services reach each other by service name, so the app service
overrides the connection strings:

```
POSTGRES_DSN  → postgres://postgres:postgres@postgres:5432/gomod-cryptopay_dev
```

On the host these stay `localhost`. That is the only difference between the two
setups.

## Production build

```bash
make build-prod
```

Builds with the `production` tag.

`.dockerignore` keeps `bin/`, `tmp/`, `.git/` and the Dockerfiles themselves out
of the build context.
