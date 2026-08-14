// Package postgres adapts the database to domain types.
//
// Rules this package must keep:
//   - every method returns domain types, never sqlc's generated.* structs
//   - storage errors are mapped to domain errors (pgx.ErrNoRows →
//     domain.ErrNotFound, pgconn code 23505 → domain.ErrAlreadyExists)
//   - queries live in queries/*.sql and are compiled by `sqlc generate`;
//     never hand-write SQL strings in Go, and never edit generated/
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gos0001/gomod-cryptopay/internal/adapter/postgres/generated"
	"github.com/gos0001/gomod-cryptopay/internal/domain"
	pkgpostgres "github.com/gos0001/gomod-cryptopay/pkg/postgres"
)

// uniqueViolation is Postgres' SQLSTATE for a duplicate key.
const uniqueViolation = "23505"

// Index names this adapter reacts to by name.
//
// Two different duplicates can come out of one INSERT into cp_invoices, and
// they mean opposite things to the caller: a contested payment amount is
// retryable and invisible to them, a reused external ID is their mistake. The
// SQLSTATE is 23505 for both, so the constraint name is the only thing that
// tells them apart.
const (
	idxInvoiceAmountHeld = "cp_invoices_asset_amount_held_key"
	idxInvoiceExternalID = "cp_invoices_external_id_key"
)

type Adapter struct {
	pool *pkgpostgres.Pool
	q    *generated.Queries
}

func New(pool *pkgpostgres.Pool) *Adapter {
	return &Adapter{pool: pool, q: generated.New(pool.Pool)}
}

// Ping reports whether the database is reachable. Used by the health endpoint:
// a service that cannot reach storage is not healthy, however well its listener
// is answering.
func (a *Adapter) Ping(ctx context.Context) error {
	if err := a.pool.Ping(ctx); err != nil {
		return fmt.Errorf("postgres: ping: %w", err)
	}
	return nil
}

// WithTx runs fn against an adapter bound to a single transaction.
//
// This exists for the outbox: a status change and the webhook event describing
// it have to commit together, or the system acquires a state where the invoice
// moved and nobody was told. Everything else should stay outside a transaction.
//
// fn receives a second *Adapter rather than mutating the receiver, so a caller
// cannot accidentally keep using the transactional handle after commit.
func (a *Adapter) WithTx(ctx context.Context, fn func(tx *Adapter) error) error {
	pgxTx, err := a.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin: %w", err)
	}
	// Rollback after a successful commit is a no-op that returns
	// pgx.ErrTxClosed, so this needs no committed flag.
	defer func() { _ = pgxTx.Rollback(context.WithoutCancel(ctx)) }()

	if err := fn(&Adapter{pool: a.pool, q: a.q.WithTx(pgxTx)}); err != nil {
		return err
	}

	if err := pgxTx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit: %w", err)
	}
	return nil
}

// MapError translates a storage error into a domain error. Repository methods
// should funnel their errors through this so use cases only ever see domain
// errors and can branch on them with errors.Is.
func MapError(err error, notFound error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		if notFound != nil {
			return notFound
		}
		return domain.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return domain.ErrAlreadyExists
	}
	return err
}

// uniqueViolationOn reports whether err is a duplicate-key failure on the named
// index.
func uniqueViolationOn(err error, index string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		pgErr.Code == uniqueViolation &&
		pgErr.ConstraintName == index
}
