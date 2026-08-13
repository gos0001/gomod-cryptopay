# gomod-cryptopay

A JSON API service in Go.
Scaffolded by [gostack](https://github.com/gos0001/gostack) — vertical slices, one
package per use case, wire for dependency injection, sqlc for Postgres.

## Quick start

```bash
make tools            # air, wire, sqlc, migrate, golangci-lint
make docker-up        # start postgres
make migrate-up       # apply migrations
make generate         # sqlc, then wire
make dev              # http://localhost:8080
```

Configuration comes from `.env.development` via envconfig, per package.

## Layout

```
gomod-cryptopay/
├── cmd/                     entrypoint, graceful shutdown, wire graph
├── internal/
│   ├── domain/              pure models + sentinel errors, no tags
│   ├── usecases/            one package per use case, grouped by entity
│   ├── controller/http_v1/  JSON API routes — routing only
│   ├── adapter/postgres/    sqlc queries, generated code, error mapping
├── pkg/                     shared low-level packages, no domain imports
├── migrations/  sqlc.yaml
├── .claude/                 the project's Claude skill
└── gostack.json             feature manifest — committed
```

## Adding features

| Command | Result |
|---|---|
| `gostack g uc users/get_profile` | a use case package — pure business logic |
| `gostack g api create_order` | a use case plus a JSON handler and its route |
| `gostack g crud users` | domain type, five use cases, migration, queries, routes |

Use cases nest under a group folder: `gostack g uc users/ban` lands in
`internal/usecases/users/ban/`. Package names must be globally unique, because
wire aliases packages by name.

Note that `g uc` deliberately does not add its `Set` to `cmd/wire.go`: wire
rejects a provider set that nothing consumes. Wire it in when a consumer appears.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `APP_ADDR` | `:8080` | listen address |
| `APP_ENV` | `development` | picks the logger format |
| `POSTGRES_DSN` | — | required; connection string |

## Make targets

| Target | Does |
|---|---|
| `dev` | air hot reload |
| `build` / `build-prod` | binary into `./bin/app` |
| `generate` | `sqlc generate` then `wire ./cmd/` |
| `test` | `go test ./... -race` |
| `lint` | golangci-lint |
| `migrate-up` / `migrate-down` | apply or roll back one migration |
| `migrate-create name=add_foo` | new migration pair |
| `docker-up` / `docker-down` | dependencies via compose |

## Database

Queries live in `internal/adapter/postgres/queries/*.sql` and are compiled by
sqlc into `internal/adapter/postgres/generated/` — never edit that directory,
and never write raw SQL strings in Go.

The order matters: write the migration, apply it, regenerate. `make generate`
runs sqlc before wire because wire cannot compile the adapter until the
generated package exists.

The adapter maps storage failures onto domain errors — `pgx.ErrNoRows` becomes
`domain.ErrNotFound`, a `23505` unique violation becomes `domain.ErrAlreadyExists`
— so use cases branch on domain errors alone.

## Architecture

One package per use case, with a single `Execute`. Domain types are the contract
between layers. Adapter interfaces are declared in the use case that needs them,
listing only the methods it uses. Controllers route and nothing else. Handlers
answer through `pkg/http_server`, so every response is `{"data":...}` or
`{"error":...}`. `pkg/` never imports `internal/domain`. Config is per package.

The full contract lives in `.claude/skills/gostack/SKILL.md`.

## Generated files

Do not edit by hand:

- `cmd/wire_gen.go` — `wire ./cmd/`
- the `// gostack:` marker comments — the CLI splices code at them
- `internal/adapter/postgres/generated/` — `sqlc generate`

