// Package migrate applies a service's versioned SQL migrations (goose format)
// to Postgres.
//
// Each service embeds its migrations:
//
//	// migrations/migrations.go
//	//go:embed *.sql
//	var FS embed.FS
//
// and hands them to gormc.WithMigrations, which runs Up at startup according to
// --db-migrate (auto | only | off).
package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"

	"core/logger"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

// Up applies pending migrations. A Postgres advisory lock serializes concurrent
// runs, so several replicas starting at once apply each migration exactly once.
func Up(ctx context.Context, db *sql.DB, fsys fs.FS, l logger.Logger) error {
	p, err := provider(db, fsys)
	if err != nil {
		return err
	}

	results, err := p.Up(ctx)
	for _, r := range results {
		if r.Error == nil {
			l.Infof("migration applied: %s (%s)", r.Source.Path, r.Duration)
		}
	}
	if err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}

	version, err := p.GetDBVersion(ctx)
	if err != nil {
		return fmt.Errorf("migrate version: %w", err)
	}
	l.Infof("database schema at version %d (%d applied now)", version, len(results))
	return nil
}

// Version returns the current schema version (0 when nothing was applied).
func Version(ctx context.Context, db *sql.DB, fsys fs.FS) (int64, error) {
	p, err := provider(db, fsys)
	if err != nil {
		return 0, err
	}
	return p.GetDBVersion(ctx)
}

func provider(db *sql.DB, fsys fs.FS) (*goose.Provider, error) {
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockTimeout(2, 60)) // wait up to 2 min
	if err != nil {
		return nil, err
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, fsys, goose.WithSessionLocker(locker))
	if err != nil {
		return nil, fmt.Errorf("migrations: %w", err)
	}
	return p, nil
}
