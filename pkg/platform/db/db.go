// Package db connects to PostgreSQL and applies schema migrations.
package db

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect opens a connection pool and waits up to a minute for the database to
// accept connections. In Kubernetes the app and its database often start at
// the same time, and waiting here is cheaper than a crash loop.
func Connect(ctx context.Context, url string, log *slog.Logger) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}

	deadline := time.Now().Add(time.Minute)
	delay := 500 * time.Millisecond
	for {
		err := pool.Ping(ctx)
		if err == nil {
			return pool, nil
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			pool.Close()
			return nil, fmt.Errorf("connect to postgres: %w", err)
		}
		log.Warn("postgres not reachable yet", "error", err, "retry_in", delay.String())
		select {
		case <-ctx.Done():
		case <-time.After(delay):
		}
		delay = min(delay*2, 5*time.Second)
	}
}

// Source is a named set of .sql files, applied in filename order.
type Source struct {
	Name string
	FS   fs.FS
}

// An arbitrary number: the key of the advisory lock that serialises migrations.
const migrationLock int64 = 7426001

// Migrate applies every migration that has not yet been applied, and records
// each one in schema_migrations.
//
// Every replica calls this at startup. The advisory lock makes them take
// turns, so two pods starting together cannot both run the same migration.
func Migrate(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, sources ...Source) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLock); err != nil {
		return fmt.Errorf("take migration lock: %w", err)
	}
	defer func() {
		conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, migrationLock)
	}()

	_, err = conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    text PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`)
	if err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	for _, src := range sources {
		names, err := fs.Glob(src.FS, "*.sql")
		if err != nil {
			return fmt.Errorf("list %s migrations: %w", src.Name, err)
		}
		sort.Strings(names)

		for _, name := range names {
			version := src.Name + "/" + name

			var applied bool
			err := conn.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, version,
			).Scan(&applied)
			if err != nil {
				return fmt.Errorf("check %s: %w", version, err)
			}
			if applied {
				continue
			}

			sql, err := fs.ReadFile(src.FS, name)
			if err != nil {
				return fmt.Errorf("read %s: %w", version, err)
			}

			// The migration and its bookkeeping row commit together, so a
			// migration that fails halfway leaves nothing behind.
			tx, err := conn.Begin(ctx)
			if err != nil {
				return fmt.Errorf("begin %s: %w", version, err)
			}
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				tx.Rollback(ctx)
				return fmt.Errorf("apply %s: %w", version, err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
				tx.Rollback(ctx)
				return fmt.Errorf("record %s: %w", version, err)
			}
			if err := tx.Commit(ctx); err != nil {
				return fmt.Errorf("commit %s: %w", version, err)
			}
			log.Info("applied migration", "version", version)
		}
	}
	return nil
}
