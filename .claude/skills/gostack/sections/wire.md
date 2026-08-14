# Wire — the DI contract

Wire is the only dependency injection in this project. There is no service
locator, no global state, no `init()` that wires things up.

## Every package exports a Set

```go
// internal/usecases/users/user_get/wire.go
package user_get

import "github.com/google/wire"

var Set = wire.NewSet(New, NewHTTPv1)
```

List only constructors the package actually has. A package with configuration
includes its `LoadConfig` too:

```go
var Set = wire.NewSet(LoadConfig, New)
```

## The graph

`cmd/wire.go` is hand-editable and carries the `//go:build wireinject` tag. It
aggregates every `Set` into one `InitializeApp`:

```go
func InitializeApp() (*App, error) {
    wire.Build(
        LoadConfig,
        logger.Set,
        http_v1.Set,
        // gostack:providers
        NewApp,
    )
    return nil, nil
}
```

`cmd/wire_gen.go` is the real implementation. ⛔ **Never edit it.** Change a
constructor, then run `wire ./cmd/` (or `make generate`). Commit both files.

## A provider set must be consumed

This is the rule that surprises people. Wire fails with *unused provider set* if
you add a `Set` that nothing in the graph asks for.

That is why `gostack g uc` deliberately does **not** touch `cmd/wire.go`: a bare
use case has no transport entry point yet, so nothing consumes it. Add its `Set`
when a consumer appears — a page, an API handler, or another use case.

`gostack g api` registers a route in the same run precisely because of this: the
handler must be reachable, or the build breaks. `gostack g uc --orchestrator`
does the same, which is why *that* form of `g uc` may touch `cmd/wire.go`.

The same applies to adapters. `internal/adapter/postgres` enters the graph only
when the first `gostack g crud` creates a use case that needs it.

`bootstrap.Set` and `cron.Set` are the exception that proves the rule: both
are in `wire.Build` from the first build, with no tasks registered, because
`NewApp` takes a `*bootstrap.Bootstrap` and a `*cron.Cron`. That is what
makes it safe for a generator to splice a task into them later — the consumer is
already there.

## Interfaces need concrete providers

Wire resolves concrete types. A constructor that takes an interface has no
provider unless you write a `wire.Bind`. The convention here avoids that
entirely: constructors take the concrete adapter and store it in an
interface-typed field.

```go
func New(pg *postgresadapter.Adapter) *Usecase   // wire can resolve this
func New(pg Postgres) *Usecase                    // wire cannot
```

## One binding per type

Two providers returning the same type is an error — *multiple bindings*. This is
why the pages controller returns its own marker type rather than `*gin.Engine`:
the router already has exactly one provider, in `http_v1.New`.

## When to re-run wire

After adding or removing a constructor, changing a constructor's signature,
adding a `Set` to `wire.Build`, or generating anything with the CLI. The
generators run it for you; `make generate` does it explicitly.
