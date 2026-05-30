// Package store is the persistence boundary: a pgx pool plus the three repositories
// (raw / normalized / entity). All SQL lives here; the rest of the app speaks Go.
package store

import (
	"context"
	"fmt"
	"io/fs"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"

	"webhook-ingestor/migrations"
)

type Store struct {
	Pool *pgxpool.Pool
}

// New opens and verifies a Postgres connection pool.
func New(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &Store{Pool: pool}, nil
}

func (s *Store) Close() { s.Pool.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.Pool.Ping(ctx) }

// migrationLockKey is an arbitrary, stable id for the advisory lock that serializes
// migrations across processes ("webhook" in hex).
const migrationLockKey int64 = 0x776562686f6f6b

// Migrate applies every embedded *.sql migration not yet recorded, each in its own
// transaction. Forward-only and idempotent. A Postgres advisory lock serializes
// concurrent migrators, so the server and any number of worker containers can boot
// together and only one applies migrations at a time.
func (s *Store) Migrate(ctx context.Context) error {
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire conn: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockKey); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, migrationLockKey) //nolint:errcheck

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("ensure migrations table: %w", err)
	}

	files, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		return err
	}
	sort.Strings(files) // lexical order = apply order (0001_, 0002_, ...)

	for _, f := range files {
		var applied bool
		if err := conn.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, f).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}

		sqlBytes, err := migrations.FS.ReadFile(f)
		if err != nil {
			return err
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply %s: %w", f, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, f); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}
