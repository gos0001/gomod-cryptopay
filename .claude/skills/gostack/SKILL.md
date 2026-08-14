---
name: gostack
description: >
  gostack project guide for gomod-cryptopay. This repository was scaffolded by
  the gostack CLI and follows its contract: one package per use case, wire DI,
  and generated files the CLI owns. ALWAYS active in this repository — read
  before adding or editing any Go file. Auto-triggers on: "gostack", "usecase",
  "use case", "endpoint", "handler", "wire", "generator", "orchestrator",
  "bootstrap", "cron", "scheduled job", "sqlc", "migration", "query", "crud", /gostack, or any edit under cmd/, internal/, or pkg/.
---

# gomod-cryptopay

> **ALWAYS active in this repository. Never skip these rules.**
> Module: `github.com/gos0001/gomod-cryptopay`. The feature set is recorded in `gostack.json`.
> Exception: throwaway spikes on a scratch branch — everything else follows this.

## Core Rules (always apply)

1. **One use case = one package** under `internal/usecases/[<group>/]<name>/`, with a single `Execute(ctx, in)`. Every *caller* gets its own handler file named after it — `http_v1.go`, `bootstrap.go`, `cron.go` — but they all call that one `Execute`.
2. **Package names are globally unique.** wire aliases by package name, so it is `user_get`, never `get`. The CLI refuses colliding names.
3. **Prefer the generator over hand-writing.** `gostack g uc|api|page|crud` writes the package, its `wire.Set` and its route in one consistent step.
4. **A provider set must be consumed.** `gostack g uc` deliberately leaves `cmd/wire.go` alone — wire rejects a set nothing uses. Add the `Set` when a consumer appears.
5. **Wire is the only DI.** Every package with constructors exports `var Set = wire.NewSet(...)`. Re-run `wire ./cmd/` after any constructor change.
6. **Domain is pure.** `internal/domain` holds models and sentinel errors — no struct tags, no adapter imports, no transport imports.
7. **Errors flow one way.** Adapter maps storage errors to domain errors; handler maps domain errors to HTTP status with `errors.Is`. Never leak an adapter error past the adapter.
8. **Handlers respond through `pkg/http_server`.** ⛔ **NEVER call `c.JSON` directly** — the `{"data":...}` / `{"error":...}` shape is the API contract.
9. **Controller only routes.** Route to handler. No logic, no domain types, no adapters.
10. **`pkg/` never imports `internal/domain`,** and config lives per package via envconfig in that package's own `config.go`. No global config struct.

## Never edit by hand

- ⛔ `cmd/wire_gen.go` — regenerate with `wire ./cmd/`
- ⛔ the `// gostack:imports|params|routes|providers` marker comments — the CLI splices at them; deleting one silently disables code injection
- ⛔ `internal/adapter/postgres/generated/` — regenerate with `sqlc generate`

## Project Layout

```
gomod-cryptopay/
├── cmd/                     main.go, app.go, config.go, wire.go, wire_gen.go
├── internal/
│   ├── domain/              pure models + sentinel errors
│   ├── usecases/            one package per use case, grouped by entity
│   ├── controller/http_v1/  JSON API routes
│   ├── orchestrator/        non-network callers — bootstrap/, cron/
│   ├── adapter/postgres/    queries/, generated/, MapError
├── pkg/                     logger, http_server, postgres
├── migrations/              golang-migrate .up/.down pairs
├── sqlc.yaml
├── docker-compose.yml, Dockerfile, Dockerfile.dev
├── gostack.json             feature manifest — COMMITTED, the CLI reads it
└── Makefile
```

## Detailed Reference

Read these when working on each layer:

- Project layout + who owns which file: `@sections/layout.md`
- Use case packages, the unit of work: `@sections/usecases.md`
- Startup tasks and scheduled jobs: `@sections/orchestrators.md`
- Wire DI contract: `@sections/wire.md`
- Domain models, errors, HTTP responses: `@sections/domain-errors.md`
- gostack CLI generators: `@sections/generators.md`
- Daily workflow — make, env, dev loop: `@sections/workflow.md`
- Gotchas and failure modes: `@sections/gotchas.md`
- Postgres, sqlc, migrations: `@sections/postgres.md`
- Docker and compose: `@sections/docker.md`

