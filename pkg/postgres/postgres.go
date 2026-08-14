// Package postgres is a thin wrapper over pgxpool. Zero domain imports —
// mapping rows to domain types is the adapter's job, not this package's.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Postgres SQLSTATEs this package reacts to.
const (
	undefinedDatabase = "3D000" // the database in the DSN does not exist
	insufficientPriv  = "42501" // the role may not create databases
)

// Concurrent CREATE DATABASE contends for the template database, which fails
// transiently rather than permanently — worth one more attempt before giving up.
const (
	createAttempts   = 3
	createRetryDelay = 250 * time.Millisecond
)

type Pool struct {
	*pgxpool.Pool
}

// New opens the pool and pings it, so a bad DSN fails at startup rather than on
// the first request.
//
// When the ping reports that the database itself does not exist, and
// DB_AUTO_CREATE is on, it is created and the connection retried. Note the
// ordering: the target is tried *first*, so a deployment whose database already
// exists never touches the maintenance database and never needs the CREATEDB
// privilege.
func New(cfg Config) (*Pool, error) {
	ctx := context.Background()

	pool, err := open(ctx, cfg.DSN)
	if err == nil {
		return pool, nil
	}

	if !cfg.AutoCreate || !isMissingDatabase(err) {
		return nil, err
	}

	if err := createDatabase(ctx, cfg.DSN); err != nil {
		return nil, err
	}

	return open(ctx, cfg.DSN)
}

func open(ctx context.Context, dsn string) (*Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	return &Pool{Pool: pool}, nil
}

func (p *Pool) Close() {
	p.Pool.Close()
}

func isMissingDatabase(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == undefinedDatabase
}

// createDatabase connects to the maintenance database on the same server and
// creates the one named in the DSN.
//
// Postgres has no CREATE DATABASE IF NOT EXISTS and will not run the statement
// inside a transaction, so the race between replicas is resolved by treating
// "already exists" as success rather than by locking.
func createDatabase(ctx context.Context, dsn string) error {
	u, err := url.Parse(dsn)
	if err != nil {
		return fmt.Errorf("postgres: POSTGRES_DSN is not a valid URL: %w", err)
	}

	name := strings.TrimPrefix(u.Path, "/")
	if name == "" {
		return errors.New("postgres: POSTGRES_DSN names no database")
	}

	// Same server, same credentials, the always-present maintenance database.
	admin := *u
	admin.Path = "/postgres"

	conn, err := pgx.Connect(ctx, admin.String())
	if err != nil {
		return fmt.Errorf("postgres: database %q does not exist and connecting to the "+
			"maintenance database to create it failed: %w", name, err)
	}
	defer func() { _ = conn.Close(ctx) }()

	// pgx.Identifier quotes the name; it comes from configuration, but a
	// database called "my-db" would be a syntax error without quoting.
	stmt := "CREATE DATABASE " + pgx.Identifier{name}.Sanitize()

	var lastErr error
	for attempt := 0; attempt < createAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(createRetryDelay):
			}
		}

		if _, err := conn.Exec(ctx, stmt); err == nil {
			return nil
		} else {
			lastErr = err
		}

		var pgErr *pgconn.PgError
		if errors.As(lastErr, &pgErr) && pgErr.Code == insufficientPriv {
			return fmt.Errorf("postgres: database %q does not exist and this role may not "+
				"create it — create it once with `CREATE DATABASE %s;` or grant CREATEDB", name, name)
		}

		// Losing a race with another replica surfaces in more than one shape:
		// 42P04 when Postgres catches the duplicate by name, 23505 straight off
		// pg_database's unique index when two backends pass that check together,
		// and 55006 when they contend for the template database. Rather than
		// enumerate them, ask whether the database is there now — the answer is
		// what actually matters.
		exists, checkErr := databaseExists(ctx, conn, name)
		if checkErr != nil {
			return fmt.Errorf("postgres: create database %q failed (%v) and checking whether it "+
				"exists also failed: %w", name, lastErr, checkErr)
		}
		if exists {
			return nil
		}
		// Genuinely absent: a transient contention error is worth one more try.
	}

	return fmt.Errorf("postgres: create database %q: %w", name, lastErr)
}

func databaseExists(ctx context.Context, conn *pgx.Conn, name string) (bool, error) {
	var exists bool
	err := conn.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists)
	return exists, err
}
