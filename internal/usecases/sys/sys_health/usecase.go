// Package sys_health answers whether the service can actually do its job.
package sys_health

import (
	"context"

	postgresadapter "github.com/gos0001/gomod-cryptopay/internal/adapter/postgres"
)

type Postgres interface {
	Ping(ctx context.Context) error
}

type Usecase struct {
	postgres Postgres
}

func New(pg *postgresadapter.Adapter) *Usecase {
	return &Usecase{postgres: pg}
}

// Execute reports readiness, and says so in the output rather than as an error.
//
// An unreachable database is a legitimate answer to a health check, not a
// failure of the check itself — the caller wants a status, and returning an
// error here would make the handler unable to tell "the database is down" from
// "the health check is broken".
func (uc *Usecase) Execute(ctx context.Context, _ Input) (Output, error) {
	if err := uc.postgres.Ping(ctx); err != nil {
		return Output{Status: StatusUnavailable, Database: "unreachable"}, nil
	}
	return Output{Status: StatusOK, Database: "ok"}, nil
}
