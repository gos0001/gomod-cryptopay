# Orchestrators — callers that are not the network

`internal/controller/http_v1` is one way into a use case: an HTTP request.
Orchestrators are the others. An orchestrator owns the lifecycle of a class of
process — startup steps, scheduled work — and routes it into use cases.

Two ship with every project:

```
internal/orchestrator/
├── bootstrap/   runs tasks once, at startup, before the listener opens
│   ├── bootstrap.go   Task interface, Run(ctx) — MACHINE-EDITED
│   ├── config.go      BOOTSTRAP_TIMEOUT (default 60s)
│   └── wire.go
└── cron/        runs jobs on an interval, for the life of the process
    ├── cron.go        Job interface, Start(ctx) / Stop() — MACHINE-EDITED
    ├── config.go      CRON_SHUTDOWN_TIMEOUT (default 15s)
    └── wire.go
```

`bootstrap.go` and `cron.go` are yours to tune — the run policy is ordinary
code — but they carry the same marker comments `cmd/wire.go` does, and the CLI
splices at them:

```go
// gostack:imports    inside the import block
// gostack:params     inside New(...)
// gostack:routes     in the registration body
```

Both are consumed by `NewApp` in `cmd/app.go` from the very first build, with
zero tasks registered. That is deliberate — see `@sections/wire.md`.

## One handler file per caller

The rule is the same one `http_v1.go` follows: **every caller of a use case gets
its own file in the slice, named after the caller.** All of them call the same
`Execute`.

```
internal/usecases/create_user/      internal/usecases/seed_super_admin/
├── usecase.go                      ├── usecase.go
├── dto.go                          ├── dto.go
├── http_v1.go   the HTTP caller    ├── config.go     gates the task
└── wire.go                         ├── bootstrap.go  the bootstrap caller
                                    └── wire.go
```

Nothing stops a use case having several. A slice with `http_v1.go` **and**
`bootstrap.go` is reachable both from `POST /api/v1/...` and at startup, with one
`Execute` behind both. A job that must run at boot *and* every ten minutes is
`bootstrap.go` plus `cron.go` in the same package — that is the whole mechanism,
there is no "run immediately" flag to look for.

## Generating one

```
gostack g uc seed_super_admin --orchestrator bootstrap
gostack g uc outbox_drain     --orchestrator cron
```

Writes `usecase.go`, `dto.go`, `config.go`, `<orchestrator>.go`, `wire.go`, then
**mutates** `cmd/wire.go` and `internal/orchestrator/<name>/<name>.go` — import,
constructor parameter, and the line that appends the handler to the slice.

Everything generated is **off by default**, because a seeder that runs because
someone forgot to set a variable is worse than one that never runs:

```bash
SEED_SUPER_ADMIN_ENABLED=true   # bootstrap: a bool
OUTBOX_DRAIN_INTERVAL=30s       # cron: the interval is the switch
```

The asymmetry is on purpose. A cron job has no separate enable flag: zero is off,
and zero is also the value `time.NewTicker` panics on, so "enabled but
unscheduled" cannot be expressed. A bootstrap task has no interval to overload,
so it gets a bool.

## What each orchestrator guarantees

**bootstrap** runs its tasks in registration order, inside one shared timeout,
*before* `ListenAndServe`. The first error aborts the boot with `Fatalw`: serving
traffic against a half-prepared system is worse than not starting.

**cron** owns the loop, so `CronRun(ctx)` does the work exactly once and a failed
run costs one tick rather than the schedule. Each job gets its own goroutine and
ticker, which means a run that outlives its interval delays the next tick instead
of overlapping with itself — no locking, it falls out of the shape. The first run
is on the first tick, not at startup. `Start` logs every job it scheduled and
every one it skipped for a zero interval, so "why is my job not running" is
answerable from the boot log. `Stop()` runs *after* the HTTP server drains and
*before* the pools close, because in-flight jobs hold connections from them.

## Adding a third orchestrator

There is no generator for the orchestrator package itself. A long-lived process
that is not on a schedule — a queue consumer, a websocket hub — is one of these:

1. `internal/orchestrator/<name>/` with an interface (`Name() string` plus one
   `<Name>Run(ctx) error`), a struct holding the slice, `config.go`, and
   `wire.go` exporting `Set`.
2. Copy the three marker comments from `bootstrap.go` — `// gostack:imports` in
   the import block, `// gostack:params` in `New(...)`, `// gostack:routes` in
   the registration body.
3. Add its `Set` to `cmd/wire.go` and a parameter to `NewApp`, then call it from
   `App.Run`. Without a consumer, wire fails with *unused provider set*.

The CLI only knows `bootstrap` and `cron`, so a third one is wired by hand.

## What an orchestrator may not do

Sequencing only, exactly like the controller. No business logic, no domain types,
no adapters, no branching on data. If a task needs to decide something, that
decision belongs in its `Execute`.
