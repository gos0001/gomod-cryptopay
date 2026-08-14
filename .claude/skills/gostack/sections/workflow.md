# Daily workflow

## First run

```bash
make tools            # air, wire, sqlc, migrate, golangci-lint
make docker-up        # dependencies via compose
make migrate-up       # apply migrations
make generate
make dev
```

## Make targets

| Target | Does |
|---|---|
| `dev` | air, rebuilding on change |
| `run` | one-shot `go run ./cmd` with `.env.development` loaded |
| `build` | binary into `./bin/app` |
| `build-prod` | production build |
| `generate` | `sqlc generate`, then `wire ./cmd/` |
| `test` | `go test ./... -race -count=1` |
| `lint` | golangci-lint |
| `migrate-up` / `migrate-down` | apply / roll back one |
| `migrate-create name=add_foo` | new migration pair |
| `docker-up` / `docker-down` | compose dependencies |

`generate` runs sqlc first on purpose: wire cannot compile the postgres adapter
until the `generated/` package exists. Running them the other way round fails
with a confusing missing-package error.

## Configuration

Config is per package, never global. Each package owns a `config.go`:

```go
type Config struct {
    Addr string `envconfig:"APP_ADDR" default:":8080"`
}

func LoadConfig() (Config, error) {
    var cfg Config
    return cfg, envconfig.Process("", &cfg)
}
```

`LoadConfig` goes in that package's `wire.Set`, so wire injects it. Mark values
with no sensible default `required:"true"` — the process then fails at startup
rather than at first use.

Values live in `.env.development` (committed) and `.env.production` (not).
`make run` and `make dev` load the development file.

## The dev loop

air watches `.go` and rebuilds.

After any constructor change, re-run `wire ./cmd/`. Forgetting it produces a
build error about arguments not matching — that is wire_gen.go being stale, not
your code being wrong.

## Testing

Tests live beside the code, in the same package, so they can construct structs
directly without going through `New`:

```go
package user_get

type fakePostgres struct{ user domain.User; err error }

func (f fakePostgres) GetUserByID(context.Context, string) (domain.User, error) {
    return f.user, f.err
}

func TestExecuteReturnsNotFoundForMissingUser(t *testing.T) {
    uc := Usecase{postgres: fakePostgres{err: domain.ErrUserNotFound}}
    _, err := uc.Execute(context.Background(), Input{ID: "x"})
    if !errors.Is(err, domain.ErrUserNotFound) {
        t.Fatalf("got %v", err)
    }
}
```

Plain structs, no mocking library. One behaviour per test, named after the
behaviour — never `TestExecute1`.
