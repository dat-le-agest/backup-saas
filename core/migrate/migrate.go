// Package migrate applies a service's versioned SQL migrations (goose format)
// to Postgres, to the default schema (Up) or to a named one (UpInSchema).
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
	"regexp"
	"strings"

	"core/logger"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
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

// schemaName: lowercase letters, digits, underscores and hyphens (app ids)
var schemaName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

// QuoteSchema returns schema as a quoted SQL identifier ("notes", "my-app"),
// or an error if it isn't a valid schema name. Use it to qualify table names.
func QuoteSchema(schema string) (string, error) {
	if !schemaName.MatchString(schema) {
		return "", fmt.Errorf("invalid schema name %q", schema)
	}
	return `"` + strings.ReplaceAll(schema, `"`, `""`) + `"`, nil
}

// UpInSchema creates schema if needed and applies fsys inside it: migrations
// run with search_path=schema, so they create unqualified tables there, and the
// schema keeps its own version table. Functions referenced by triggers should
// be declared with SET search_path FROM CURRENT so they keep using this schema.
//
// dsn is the database's DSN (gormc.GormComponent.DSN); the role must be allowed
// to create schemas (it owns the database).
func UpInSchema(ctx context.Context, dsn string, fsys fs.FS, schema string, l logger.Logger) error {
	quoted, err := QuoteSchema(schema)
	if err != nil {
		return err
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("migrate %s: parse dsn: %w", schema, err)
	}
	cfg.RuntimeParams["search_path"] = quoted
	db := stdlib.OpenDB(*cfg)
	defer db.Close()

	if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS `+quoted); err != nil {
		return fmt.Errorf("migrate %s: create schema: %w", schema, err)
	}
	if err := Up(ctx, db, fsys, l.With("schema", schema)); err != nil {
		return fmt.Errorf("schema %s: %w", schema, err)
	}
	return nil
}
