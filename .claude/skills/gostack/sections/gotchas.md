# Gotchas

Failure modes that look like something else. Each entry names the symptom first,
because that is what you will actually see.

## "unused provider set" from wire

You added a `Set` to `wire.Build` that nothing consumes. Wire requires every
provider set to be reachable from `InitializeApp`.

This is why `gostack g uc` leaves `cmd/wire.go` alone — a bare use case has no
caller yet. Add its `Set` when something depends on it, not before.

## "not enough arguments in call to ..." after generating

`cmd/wire_gen.go` is stale. A generator changed a constructor signature but wire
did not re-run, usually because it is not on `PATH`. Run `make generate`, or
`wire ./cmd/` directly. If `wire` is missing, `make tools` installs it.

## "multiple bindings for *gin.Engine"

Two providers return the same type. The router has exactly one provider, in
`http_v1.New`.

## A new package collides with an existing one

Wire aliases packages by their name, not their path, so
`internal/usecases/users/get` and `internal/usecases/posts/get` both want the
alias `get`. The last path segment must be unique across the whole project. The
CLI refuses and points at the existing package; pick a more specific name —
`user_get`, `post_get`.

## A generator stops wiring anything into a file

A `// gostack:` marker comment was deleted. The CLI splices imports, parameters,
routes and providers at those markers, falling back to fragile heuristics when
they are absent. Nothing errors — code simply stops being added. Restore the
marker line.

## Edits to a generated file vanish

`cmd/wire_gen.go`, `internal/adapter/postgres/generated/` are overwritten on
every regeneration. Change the source instead: a constructor, a
`queries/*.sql` file.

## sqlc rejects a query: "relation does not exist"

sqlc validates queries against the migrations, not against a live database. A
query for a table you have not written a migration for cannot compile. Write the
migration first — `gostack g crud` generates a skeleton for exactly this reason.

## Confusing missing-package error after editing SQL

You ran wire before sqlc. The adapter imports `generated/`, which does not exist
until sqlc has run. Order is always migrate → sqlc → wire; `make generate` does
the last two correctly.

## gostack.json went missing

The CLI falls back to probing the filesystem to guess the feature set, which is
less reliable — a page generator may refuse in a project that does have pages.
Keep the file committed; it is deliberately not in `.gitignore`.

## Domain type ends up with json tags

Something serialised a domain model directly. Build an `Output` DTO from it
instead — the domain layer must stay free of transport concerns, or every
API change starts forcing a domain change.

## Handler returns a bare string or c.JSON

Responses must go through `pkg/http_server`, so every payload is `{"data":...}`
or `{"error":...}`. A handler that writes its own shape breaks clients that rely
on the envelope.
