# Project layout — who owns which file

Import paths below are relative to `github.com/gos0001/gomod-cryptopay`.

## Dependency direction

Never reversed:

```
pkg/  ←  adapter/  ←  domain/  ←  usecases/  ←  controller/
                                           ↖  orchestrator/
```

`controller/` and `orchestrator/` sit at the same level: both are callers of use
cases, one reached over the network, the other by the process lifecycle.

`pkg/` is the floor: thin wrappers over libraries, zero `internal/domain` imports.
If something in `pkg/` needs a domain type, it is an adapter, not a pkg.

## Three kinds of file

**Hand-written — yours.**
`internal/domain/*.go`, `internal/usecases/**`, `pkg/**`, `cmd/app.go`,
`cmd/config.go`, `cmd/main.go`, adapters.

**Generated — regenerate, never edit.**

| File | Owner | Regenerate with |
|---|---|---|
| `cmd/wire_gen.go` | wire | `wire ./cmd/` |
| `internal/adapter/postgres/generated/` | sqlc | `sqlc generate` |

**Machine-edited — yours, but the CLI splices into it.**
`cmd/wire.go`, `internal/controller/http_v1/controller.go` and
`internal/orchestrator/*/{bootstrap,cron}.go` are normal files you may edit,
but they carry marker comments the generators insert at:

```go
// gostack:imports     inside the import block
// gostack:params      inside the New(...) parameter list
// gostack:routes      in the routing body
// gostack:providers   inside wire.Build(...)
```

Keep the markers. Deleting one does not break the build — it silently stops the
next generator from wiring anything into that file.

## Directory map

```
cmd/                        entrypoint; graceful shutdown lives in app.go
internal/domain/            models + sentinel errors; no tags, no imports out
internal/usecases/          business logic, one package per use case
internal/controller/http_v1 JSON routes; routing only
internal/orchestrator/      non-network callers: bootstrap/ and cron/
internal/adapter/postgres/  queries/*.sql, generated/, MapError
pkg/                        logger, http_server, postgres
migrations/                 NNNNNN_name.up.sql / .down.sql
```

## gostack.json

Records which features this project was created with. The CLI reads it to decide
whether `gostack g page` is allowed, whether to emit SQL, and so on. It is
committed; if it goes missing the CLI falls back to probing the tree, which is
less reliable. Never add it to `.gitignore`.
