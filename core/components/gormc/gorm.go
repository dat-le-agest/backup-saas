// Package gormc is the sctx component for PostgreSQL through gorm.
//
//	sctx.WithComponent(gormc.NewGormDB("db", "", gormc.WithMigrations(migrations.FS)))
//
// Flags (prefix "" → env DB_DSN, DB_MIGRATE, ...):
//
//	--db-dsn        postgres://user:pass@host:5432/db?sslmode=disable (required)
//	--db-migrate    auto: apply migrations then serve | only: apply and exit | off
//	--db-max-conn, --db-max-idle-conn, --db-max-conn-idle-time
//
// Connect as a role that owns the service's database but is NOT a superuser:
// superusers bypass row-level security (see core/tenancy).
package gormc

import (
	"context"
	"core/components/gormc/plugins/exclause"
	"core/logger"
	"core/migrate"
	"core/sctx"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormLogger "gorm.io/gorm/logger"
	"gorm.io/plugin/opentelemetry/tracing"
)

const (
	MigrateAuto = "auto"
	MigrateOnly = "only"
	MigrateOff  = "off"
)

// GormComponent is what other packages retrieve via sctx.MustGetAs[gormc.GormComponent].
type GormComponent interface {
	GetDB() *gorm.DB
	// MigrateOnly is true for --db-migrate=only: main should exit after Load.
	MigrateOnly() bool
}

type Option func(*gormDB)

// WithMigrations sets the embedded goose migrations applied at startup.
func WithMigrations(fsys fs.FS) Option {
	return func(g *gormDB) { g.migrations = fsys }
}

type gormDB struct {
	id         string
	prefix     string
	migrations fs.FS

	dsn                   string
	migrateMode           string
	maxOpenConnections    int
	maxIdleConnections    int
	maxConnectionIdleTime int

	logger   logger.Logger
	logLevel string
	db       *gorm.DB
}

func NewGormDB(id, prefix string, opts ...Option) *gormDB {
	g := &gormDB{id: id, prefix: prefix}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

func (gdb *gormDB) ID() string {
	return gdb.id
}

func (gdb *gormDB) InitFlags() {
	prefix := gdb.prefix
	if prefix != "" {
		prefix += "-"
	}

	flag.StringVar(&gdb.dsn, prefix+"db-dsn", "",
		"Postgres DSN, e.g. postgres://app:secret@localhost:5432/app?sslmode=disable (required)")
	flag.StringVar(&gdb.migrateMode, prefix+"db-migrate", MigrateAuto,
		"schema migrations at startup: auto (apply, then serve) | only (apply and exit) | off - Default auto")
	flag.IntVar(&gdb.maxOpenConnections, prefix+"db-max-conn", 30,
		"maximum number of open connections to the database - Default 30")
	flag.IntVar(&gdb.maxIdleConnections, prefix+"db-max-idle-conn", 10,
		"maximum number of database connections in the idle - Default 10")
	flag.IntVar(&gdb.maxConnectionIdleTime, prefix+"db-max-conn-idle-time", 3600,
		"maximum amount of time a connection may be idle in seconds - Default 3600")
}

func (gdb *gormDB) Activate(serviceCtx sctx.ServiceContext) error {
	gdb.logger = serviceCtx.Logger(gdb.id)
	gdb.logLevel = serviceCtx.LogLevel()

	if gdb.dsn == "" {
		return errors.New("db-dsn is required (postgres://user:pass@host:5432/db?sslmode=disable)")
	}
	switch gdb.migrateMode {
	case MigrateAuto, MigrateOnly, MigrateOff:
	default:
		return fmt.Errorf("invalid db-migrate %q (auto | only | off)", gdb.migrateMode)
	}

	gdb.logger.Info("connecting to postgres...")
	conn, err := Open(gdb.dsn)
	if err != nil {
		return err
	}

	sqlDB, err := conn.DB()
	if err != nil {
		return err
	}
	sqlDB.SetMaxOpenConns(gdb.maxOpenConnections)
	sqlDB.SetMaxIdleConns(gdb.maxIdleConnections)
	sqlDB.SetConnMaxIdleTime(time.Second * time.Duration(gdb.maxConnectionIdleTime))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return fmt.Errorf("ping postgres: %w", err)
	}

	if gdb.migrations != nil && gdb.migrateMode != MigrateOff {
		if err := migrate.Up(context.Background(), sqlDB, gdb.migrations, gdb.logger); err != nil {
			_ = sqlDB.Close()
			return err
		}
	}

	// spans per query (without bound values: they may hold personal data) and
	// connection pool metrics, through the global OpenTelemetry providers
	if err := conn.Use(tracing.NewPlugin(tracing.WithoutQueryVariables())); err != nil {
		return fmt.Errorf("gorm otel plugin: %w", err)
	}

	gdb.db = conn
	return nil
}

// Open connects gorm to Postgres with the kit's settings. Used by the component
// and by core/testdb.
func Open(dsn string) (*gorm.DB, error) {
	conn, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		// unique violations become gorm.ErrDuplicatedKey
		TranslateError: true,
	})
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	// WITH / UNION / INTERSECT / EXCEPT clauses
	if err := conn.Use(exclause.New()); err != nil {
		return nil, fmt.Errorf("gorm exclause plugin: %w", err)
	}
	return conn, nil
}

func (gdb *gormDB) Stop() error {
	if gdb.db == nil {
		return nil
	}
	sqlDB, err := gdb.db.DB()
	if err != nil {
		return err
	}
	gdb.logger.Info("closing database connection...")
	return sqlDB.Close()
}

func (gdb *gormDB) MigrateOnly() bool {
	return gdb.migrateMode == MigrateOnly
}

func (gdb *gormDB) GetDB() *gorm.DB {
	if gdb.logLevel == "debug" {
		return gdb.db.Session(&gorm.Session{NewDB: true}).Debug()
	}

	return gdb.db.Session(&gorm.Session{
		NewDB:  true,
		Logger: gdb.db.Logger.LogMode(gormLogger.Silent),
	})
}
