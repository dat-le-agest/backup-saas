package gormc

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"time"

	"core/components/gormc/plugins/exclause"
	"core/logger"
	"core/migrate"
	"core/sctx"

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

type GormComponent interface {
	GetDB() *gorm.DB
	MigrateOnly() bool
	MigrateMode() string
	DSN() string
}

type Option func(*gormDB)

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

func WithMigrations(fsys fs.FS) Option {
	return func(g *gormDB) {
		g.migrations = fsys
	}
}

func NewGormDB(id, prefix string, opts ...Option) *gormDB {
	g := &gormDB{
		id:     id,
		prefix: prefix,
	}

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

	flag.StringVar(
		&gdb.dsn,
		prefix+"db-dsn",
		"",
		"postgres DSN, required",
	)

	flag.StringVar(
		&gdb.migrateMode,
		prefix+"db-migrate",
		MigrateAuto,
		"migration mode: auto, only or off, default auto",
	)

	flag.IntVar(
		&gdb.maxOpenConnections,
		prefix+"db-max-conn",
		30,
		"maximum number of open database connections, default 30",
	)

	flag.IntVar(
		&gdb.maxIdleConnections,
		prefix+"db-max-idle-conn",
		10,
		"maximum number of idle database connections, default 10",
	)

	flag.IntVar(
		&gdb.maxConnectionIdleTime,
		prefix+"db-max-conn-idle-time",
		3600,
		"maximum idle time for a database connection in seconds, default 3600",
	)
}

func (gdb *gormDB) Activate(serviceCtx sctx.ServiceContext) error {
	gdb.logger = serviceCtx.Logger(gdb.id)
	gdb.logLevel = serviceCtx.LogLevel()

	if gdb.dsn == "" {
		return errors.New("db-dsn is required")
	}

	switch gdb.migrateMode {
	case MigrateAuto, MigrateOnly, MigrateOff:
	default:
		return fmt.Errorf(
			"invalid db-migrate %q: allowed values are auto, only and off",
			gdb.migrateMode,
		)
	}

	gdb.logger.Info("connecting to postgres")

	conn, err := Open(gdb.dsn)
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}

	sqlDB, err := conn.DB()
	if err != nil {
		return errors.Join(
			fmt.Errorf("get underlying database connection: %w", err),
			closeGormDB(conn),
		)
	}

	sqlDB.SetMaxOpenConns(gdb.maxOpenConnections)
	sqlDB.SetMaxIdleConns(gdb.maxIdleConnections)
	sqlDB.SetConnMaxIdleTime(
		time.Duration(gdb.maxConnectionIdleTime) * time.Second,
	)

	ctx, cancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancel()

	if err := sqlDB.PingContext(ctx); err != nil {
		return errors.Join(
			fmt.Errorf("ping postgres: %w", err),
			sqlDB.Close(),
		)
	}

	if gdb.migrations != nil && gdb.migrateMode != MigrateOff {
		if err := migrate.Up(ctx, sqlDB, gdb.migrations, gdb.logger); err != nil {
			return errors.Join(
				fmt.Errorf("run database migrations: %w", err),
				sqlDB.Close(),
			)
		}
	}

	// avoid recording query variables because they may contain sensitive data.
	if err := conn.Use(tracing.NewPlugin(tracing.WithoutQueryVariables())); err != nil {
		return errors.Join(
			fmt.Errorf("register gorm opentelemetry plugin: %w", err),
			sqlDB.Close(),
		)
	}

	gdb.db = conn

	return nil
}

// Open opens a postgres connection using the shared gorm configuration.
func Open(dsn string) (*gorm.DB, error) {
	conn, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		TranslateError: true,
	})
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}

	if err := conn.Use(exclause.New()); err != nil {
		return nil, errors.Join(
			fmt.Errorf("register gorm exclause plugin: %w", err),
			closeGormDB(conn),
		)
	}

	return conn, nil
}

func closeGormDB(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}

	return sqlDB.Close()
}

func (gdb *gormDB) Stop() error {
	if gdb.db == nil {
		return nil
	}

	gdb.logger.Info("closing database connection")

	sqlDB, err := gdb.db.DB()
	if err != nil {
		return fmt.Errorf("get underlying database connection: %w", err)
	}

	if err := sqlDB.Close(); err != nil {
		return fmt.Errorf("close database connection: %w", err)
	}

	gdb.db = nil

	return nil
}

func (gdb *gormDB) GetDB() *gorm.DB {
	if gdb.db == nil {
		return nil
	}

	if gdb.logLevel == "debug" {
		return gdb.db.Session(&gorm.Session{
			NewDB: true,
		}).Debug()
	}

	return gdb.db.Session(&gorm.Session{
		NewDB:  true,
		Logger: gdb.db.Logger.LogMode(gormLogger.Silent),
	})
}

func (gdb *gormDB) MigrateOnly() bool {
	return gdb.migrateMode == MigrateOnly
}

func (gdb *gormDB) MigrateMode() string {
	return gdb.migrateMode
}

func (gdb *gormDB) DSN() string {
	return gdb.dsn
}
