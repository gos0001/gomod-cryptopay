// Package dbschema applies this service's table definitions at startup.
//
// There is no migration tool and no version table. The schema is one embedded
// file of idempotent statements, executed on every start: it creates what is
// missing and leaves alone what exists. For a service whose schema is a handful
// of tables, that is the whole requirement — and it is what lets the published
// container image run against an empty database with no separate migrate step.
//
// Zero domain imports; the SQL arrives as a Definition so this package stays
// independent of where it is stored.
package dbschema

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"

	pkgpostgres "github.com/gos0001/gomod-cryptopay/pkg/postgres"
)

// Definition is the schema SQL. A named type rather than a bare string so wire
// can resolve it without colliding with every other string in the graph.
type Definition string

// schemaLockKey guards the apply against concurrent replicas.
//
// `CREATE TABLE IF NOT EXISTS` is not safe to run concurrently despite how it
// reads: two sessions can pass the existence check together and one then fails
// on a duplicate key in pg_type. An arbitrary but fixed key, scoped to this
// service.
const schemaLockKey int64 = 0x63706179 // "cpay" in hex

type Applier struct {
	pool *pkgpostgres.Pool
	def  Definition
	cfg  Config
}

func New(pool *pkgpostgres.Pool, def Definition, cfg Config) *Applier {
	return &Applier{pool: pool, def: def, cfg: cfg}
}

type Result struct {
	Skipped bool
	Reason  string
}

// Ensure creates any missing table or index.
func (a *Applier) Ensure(ctx context.Context) (Result, error) {
	if !a.cfg.AutoSchema {
		return Result{Skipped: true, Reason: "DB_AUTO_SCHEMA is false"}, nil
	}

	// A session advisory lock lives on one connection, so the whole apply has to
	// hold the same one rather than borrowing from the pool per statement.
	conn, err := a.pool.Acquire(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("dbschema: acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", schemaLockKey); err != nil {
		return Result{}, fmt.Errorf("dbschema: lock: %w", err)
	}
	defer func() {
		// Released explicitly rather than left to connection teardown: the
		// connection goes back to the pool still holding the lock otherwise.
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", schemaLockKey)
	}()

	// PgConn.Exec runs the simple query protocol, which is what allows a batch
	// of statements in one call — the extended protocol used by Exec accepts
	// only one statement per request.
	mrr := conn.Conn().PgConn().Exec(ctx, string(a.def))
	if _, err := mrr.ReadAll(); err != nil {
		return Result{}, fmt.Errorf("dbschema: apply: %w", describe(err))
	}
	if err := mrr.Close(); err != nil {
		return Result{}, fmt.Errorf("dbschema: apply: %w", describe(err))
	}

	return Result{}, nil
}

// describe adds the position of a failing statement, which a bare driver error
// omits and which is the only thing that makes a syntax error in a long schema
// findable.
func describe(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Position > 0 {
		return fmt.Errorf("%s (at character %d)", pgErr.Message, pgErr.Position)
	}
	return err
}
