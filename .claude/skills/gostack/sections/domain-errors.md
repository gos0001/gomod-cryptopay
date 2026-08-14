# Domain, errors, and HTTP responses

## Domain is the contract

`internal/domain` holds plain models and the sentinel errors they raise. Rules
that are never bent:

- no `json:`, `db:` or `validate:` struct tags
- no imports from adapters, controllers, or use case packages
- stdlib only (`time`, `errors`, `uuid`)
- an entity's errors live in the same file as the entity

```go
package domain

var (
    ErrUserNotFound      = errors.New("user not found")
    ErrUserAlreadyExists = errors.New("user already exists")
)

type User struct {
    ID        string
    Email     string
    CreatedAt time.Time
    UpdatedAt time.Time
}
```

Base errors — `ErrNotFound`, `ErrAlreadyExists`, `ErrInvalidInput`,
`ErrUnauthorized`, `ErrForbidden` — are in `errors.go` for adapters that have no
entity-specific error to reach for.

Every value crossing a layer boundary maps to or from a domain type. The adapter
returns `domain.User`, never a generated row struct. The use case builds an
`Output` DTO from `domain.User`, rather than serialising the model itself.

## The error chain

Four links, each with one job:

1. **Domain** declares sentinel errors.
2. **Adapter** maps storage failures onto them — `MapError` turns
   `pgx.ErrNoRows` into a not-found error and SQLSTATE `23505` into
   already-exists. An adapter error never escapes the adapter.
3. **Use case** branches with `errors.Is`, and wraps anything unexpected with
   `fmt.Errorf("context: %w", err)`.
4. **Handler** maps domain errors onto status codes, again with `errors.Is`.

```go
out, err := h.uc.Execute(c.Request.Context(), in)
if err != nil {
    switch {
    case errors.Is(err, domain.ErrUserNotFound):
        httpserver.NotFound(c, "user not found")
    case errors.Is(err, domain.ErrUserAlreadyExists):
        httpserver.Conflict(c, "user already exists")
    default:
        h.logger.Errorw("get user failed", "error", err)
        httpserver.InternalServerError(c, "internal server error")
    }
    return
}
httpserver.OK(c, out)
```

Never send `err.Error()` to a client. Log the detail, return a stable message.

## Responses go through pkg/http_server

⛔ **Never call `c.JSON` directly.** The envelope is the API contract:

```
success   {"data": ...}
failure   {"error": "..."}
```

| Helper | Status |
|---|---|
| `OK` / `Created` / `NoContent` | 200 / 201 / 204 |
| `BadRequest` / `Unauthorized` / `Forbidden` | 400 / 401 / 403 |
| `NotFound` / `Conflict` / `TooManyRequests` | 404 / 409 / 429 |
| `InternalServerError` | 500 |

Only the handler logs errors — it is the only layer that knows what the caller
will see.
