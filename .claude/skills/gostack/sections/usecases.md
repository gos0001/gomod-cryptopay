# Use case packages

One use case is one directory, one Go package, one business operation, with a
single `Execute`. Never a second entry point.

## Anatomy

```
internal/usecases/users/user_get/
├── usecase.go   Usecase struct, adapter interfaces, New, Execute
├── dto.go       Input, Output, Validate
├── http_v1.go   the JSON handler (only when there is an HTTP caller)
└── wire.go      var Set = wire.NewSet(...)
```

`gostack g uc <name>` writes the first, second and fourth.
`gostack g api <name>` writes all four and registers the route.

## Adapter interfaces belong to the use case

Declare the interface in the use case, not in the adapter package, and list only
the methods this use case actually calls. The constructor takes the *concrete*
adapter — wire resolves concrete types, not interfaces — and stores it behind the
narrow interface:

```go
type Postgres interface {
    GetUserByID(ctx context.Context, id string) (domain.User, error)
}

type Usecase struct {
    postgres Postgres
}

func New(pg *postgresadapter.Adapter) *Usecase {
    return &Usecase{postgres: pg}
}

func (uc *Usecase) Execute(ctx context.Context, in Input) (Output, error) {
    user, err := uc.postgres.GetUserByID(ctx, in.ID)
    if err != nil {
        return Output{}, err
    }
    return Output{ID: user.ID, Name: user.Name}, nil
}
```

This keeps the use case honest about its dependencies and makes tests trivial:
implement the interface with a plain struct in `usecase_test.go`, in the same
package, and construct `Usecase{}` directly. No mocking library.

## Grouping and package names

Related use cases nest under a group folder:

```
internal/usecases/users/user_get/     package user_get
internal/usecases/users/user_list/    package user_list
internal/usecases/users/get_profile/  package get_profile
internal/usecases/send_email/         package send_email    (ungrouped)
```

The group is a directory only. **Package names must be globally unique**, because
wire aliases packages by name — `internal/usecases/users/get` and
`internal/usecases/posts/get` would both want the alias `get` and collide. That
is why CRUD names packages `user_get`, not `get`, and why the CLI refuses a name
already taken elsewhere.

## What a use case may and may not do

May: take `context.Context` first, use `internal/domain`, use its own adapter
interfaces, use `pkg/`.

May not: import a controller or transport package, expose more than one `Execute`,
return raw adapter structs, or skip context propagation.

## DTOs

`Input` fields the handler fills from headers or the path — not from the JSON body
— get `json:"-"`. `Validate()` trims and checks, returning a plain error the
handler turns into 400. `Output` carries `json:` tags and is built *from* domain
types, so domain models are never serialised directly onto the wire.
