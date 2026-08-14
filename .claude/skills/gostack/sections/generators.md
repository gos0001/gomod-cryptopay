# gostack generators

Prefer these over hand-writing a package. Each writes the package, its `wire.Set`
and its route in one consistent step, and re-runs wire afterwards.

## g uc — a use case, nothing else

```
gostack g uc send_email              → internal/usecases/send_email/
gostack g uc users/get_profile       → internal/usecases/users/get_profile/
```

Writes `usecase.go`, `dto.go`, `wire.go`.

**Does not touch `cmd/wire.go`,** by design: wire rejects a provider set that
nothing consumes, so registering a bare use case would break the build. Add
`<pkg>.Set` to `wire.Build` yourself once something calls it.

### --orchestrator — a use case with a non-HTTP caller

```
gostack g uc seed_super_admin --orchestrator bootstrap    → runs once at startup
gostack g uc outbox_drain     --orchestrator cron         → runs on an interval
```

Adds `config.go` and a handler file named after the orchestrator
(`bootstrap.go` / `cron.go`) to the four above. With this flag `g uc` **does**
mutate `cmd/wire.go`, plus `internal/orchestrator/<name>/<name>.go` — the
orchestrator becomes the consumer, so the set is no longer unused.

Both are off until configured — `<NAME>_ENABLED=true` for bootstrap,
`<NAME>_INTERVAL=30s` for cron. See `@sections/orchestrators.md`.

## g api — a use case plus a JSON endpoint

```
gostack g api create_order                    → POST /api/v1/create-order
gostack g api list_tags --method GET          → GET  /api/v1/list-tags
gostack g api users/ban_user                  → POST /api/v1/ban-user
```

Writes `usecase.go`, `dto.go`, `http_v1.go`, `wire.go`. **Mutates** `cmd/wire.go`
(adds the `Set`) and `internal/controller/http_v1/controller.go` (import,
constructor parameter, route). The route is derived from the name with
underscores turned into dashes; edit `controller.go` afterwards if you want a
different path.

## g crud — a whole entity

```
gostack g crud users
```

Writes:

- `internal/domain/user.go` — the type and its sentinel errors (fields are yours to fill in)
- five packages under `internal/usecases/users/`: `user_get`, `user_list`, `user_create`, `user_update`, `user_delete`
- `migrations/NNNNNN_create_users.{up,down}.sql` — a table skeleton, columns are yours
- `internal/adapter/postgres/queries/user.sql` — the five queries

**Mutates** `cmd/wire.go` and `controller.go`, registering:

```
GET    /api/v1/users        POST   /api/v1/users
GET    /api/v1/users/:id    PUT    /api/v1/users/:id
                            DELETE /api/v1/users/:id
```

The singular form is derived naively (`users` → `user`, `categories` →
`category`). Check it before committing.

After generating: fill in the domain fields, the migration columns and the query
column lists, then `make migrate-up && make generate`.

## Naming rules

Names are `[a-z][a-z0-9_-]*` per segment. A group prefix is a directory:
`users/ban` puts package `ban` in `internal/usecases/users/`. Because wire
aliases by package name, the **last segment must be unique across the project** —
the CLI refuses a name already used elsewhere and tells you where it is.
