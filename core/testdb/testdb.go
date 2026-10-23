// Package testdb gives tests a real, isolated Postgres database.
//
//	db := testdb.New(t)                  // empty database
//	db := testdb.New(t, migrations.FS)   // with the service's migrations applied
//
// One Postgres container is started per test binary (testcontainers, needs
// Docker) and each New creates a fresh database owned by a fresh non-superuser
// role, so row-level security policies are enforced exactly like in production.
// Set TEST_DATABASE_URL to a superuser DSN to use an existing server instead
// (e.g. a CI service container).
package testdb

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"core/components/gormc/plugins/exclause"
	"core/logger"
	"core/migrate"

	_ "github.com/jackc/pgx/v5/stdlib"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const image = "postgres:18.6-alpine"

var (
	once     sync.Once
	adminDSN string
	startErr error
)

func admin() (string, error) {
	once.Do(func() {
		if dsn := os.Getenv("TEST_DATABASE_URL"); dsn != "" {
			adminDSN = dsn
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		c, err := tcpostgres.Run(ctx, image,
			tcpostgres.WithDatabase("postgres"),
			tcpostgres.WithUsername("postgres"),
			tcpostgres.WithPassword("postgres"),
			tcpostgres.BasicWaitStrategies(),
		)
		if err != nil {
			startErr = fmt.Errorf("start postgres container (Docker required, or set TEST_DATABASE_URL): %w", err)
			return
		}
		// removed by testcontainers' reaper when the test binary exits
		adminDSN, startErr = c.ConnectionString(ctx, "sslmode=disable")
	})
	return adminDSN, startErr
}

// New returns a gorm DB on a new database. migrations, if any, are applied.
func New(t testing.TB, migrations ...fs.FS) *gorm.DB {
	t.Helper()
	return Open(t, NewDSN(t), migrations...)
}

// Open returns a gorm DB on dsn (from NewDSN) with migrations applied to the
// default schema. Use it when the test also needs the DSN, e.g. to migrate
// other schemas with migrate.UpInSchema.
func Open(t testing.TB, dbDSN string, migrations ...fs.FS) *gorm.DB {
	t.Helper()

	// same settings as gormc.Open (not imported: gormc's own tests use testdb)
	db, err := gorm.Open(postgres.Open(dbDSN), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Use(exclause.New()); err != nil {
		t.Fatal(err)
	}
	db.Logger = gormlogger.Discard
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })

	for _, m := range migrations {
		if err := migrate.Up(context.Background(), sqlDB, m, nopLogger{}); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// NewDSN creates a new empty database owned by a new non-superuser role and
// returns its DSN (to test code that opens its own connection, e.g. gormc).
// The database is dropped when the test ends.
func NewDSN(t testing.TB) string {
	t.Helper()
	dsn, err := admin()
	if err != nil {
		t.Fatal(err)
	}

	adminDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()

	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "test_" + hex.EncodeToString(b)
	for _, stmt := range []string{
		fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s' NOSUPERUSER NOBYPASSRLS`, name, name),
		fmt.Sprintf(`CREATE DATABASE %s OWNER %s`, name, name),
	} {
		if _, err := adminDB.Exec(stmt); err != nil {
			t.Fatalf("testdb: %s: %v", stmt, err)
		}
	}

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(name, name)
	u.Path = "/" + name

	// registered first, so it runs after the cleanups that close connections
	t.Cleanup(func() {
		cleanup, err := sql.Open("pgx", dsn)
		if err != nil {
			return
		}
		defer cleanup.Close()
		_, _ = cleanup.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, name))
		_, _ = cleanup.Exec(fmt.Sprintf(`DROP ROLE IF EXISTS %s`, name))
	})
	return u.String()
}

type nopLogger struct{}

func (nopLogger) Debug(...interface{})                     {}
func (nopLogger) Info(...interface{})                      {}
func (nopLogger) Warn(...interface{})                      {}
func (nopLogger) Error(...interface{})                     {}
func (nopLogger) Debugf(string, ...interface{})            {}
func (nopLogger) Infof(string, ...interface{})             {}
func (nopLogger) Warnf(string, ...interface{})             {}
func (nopLogger) Errorf(string, ...interface{})            {}
func (l nopLogger) With(string, interface{}) logger.Logger { return l }
func (l nopLogger) WithFields(logger.Fields) logger.Logger { return l }

// UpInSchema applies migrations inside schema of the database of dsn (see
// migrate.UpInSchema), failing the test on error.
func UpInSchema(t testing.TB, dsn string, migrations fs.FS, schema string) {
	t.Helper()
	if err := migrate.UpInSchema(context.Background(), dsn, migrations, schema, nopLogger{}); err != nil {
		t.Fatal(err)
	}
}
