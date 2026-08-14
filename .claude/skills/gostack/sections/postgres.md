# Postgres, sqlc, migrations

## Never write SQL strings in Go

Queries live in `internal/adapter/postgres/queries/*.sql` and are compiled by
sqlc into typed Go. ⛔ **Never edit `internal/adapter/postgres/generated/`** — it
is overwritten by every `sqlc generate`.

```sql
-- name: GetUser :one
SELECT * FROM users WHERE id = $1;

-- name: ListUsers :many
SELECT * FROM users ORDER BY created_at DESC LIMIT $1 OFFSET $2;

-- name: DeleteUser :exec
DELETE FROM users WHERE id = $1;
```

The annotation sets the return shape: `:one` a single row, `:many` a slice,
`:exec` nothing.

## Order of operations

sqlc validates queries against the **migrations**, not a live database, so a
query for a table with no migration cannot compile.

```
1. write the migration      migrations/NNNNNN_name.up.sql (+ .down.sql)
2. apply it                 make migrate-up
3. regenerate               make generate     (sqlc, then wire)
```

`make generate` runs sqlc before wire deliberately: the adapter imports
`generated/`, so wire cannot compile the package until sqlc has produced it.
Running wire first fails with a confusing missing-package error.

`gostack g crud <plural>` writes a migration skeleton and the five queries
together, precisely so this order is never wrong. Fill in the columns, then run
the two commands.

## The adapter maps errors

Two layers: `pkg/postgres` owns the pool and knows nothing about the domain;
`internal/adapter/postgres` turns rows into domain types and storage failures
into domain errors.

```go
func MapError(err error, notFound error) error {
    if errors.Is(err, pgx.ErrNoRows) {
        return notFound            // e.g. domain.ErrUserNotFound
    }
    var pgErr *pgconn.PgError
    if errors.As(err, &pgErr) && pgErr.Code == "23505" {
        return domain.ErrAlreadyExists
    }
    return err
}
```

Route every repository error through it. A `pgx` or `pgconn` error must never
reach a use case — use cases branch on domain errors only, and nothing above the
adapter should import a driver package.

Repository methods return domain types, never `generated.*` structs, and convert
`pgtype` values to plain Go types on the way out.

## Wiring the generated queries

The adapter ships without the sqlc `Queries` field, so a fresh project compiles
before sqlc has ever run. Once you have generated, add it:

```go
type Adapter struct {
    pool *pkgpostgres.Pool
    q    *generated.Queries
}

func New(pool *pkgpostgres.Pool) *Adapter {
    return &Adapter{pool: pool, q: generated.New(pool.Pool)}
}
```

Then give each entity its own file — `user_repo.go`, `post_repo.go` — rather than
one growing adapter file.

## Connection

`pkg/postgres` opens a `pgxpool` and pings it at startup, so a bad `POSTGRES_DSN`
fails immediately rather than on the first request. The pool is closed during
graceful shutdown, after the HTTP server stops accepting requests.
