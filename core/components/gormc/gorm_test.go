package gormc

import (
	"testing"
	"testing/fstest"

	"core/logger"
	"core/sctx"
	"core/testdb"
)

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

type fakeSC struct{ sctx.ServiceContext }

func (fakeSC) Logger(string) logger.Logger { return nopLogger{} }
func (fakeSC) LogLevel() string            { return "info" }

var migrations = fstest.MapFS{
	"00001_init.sql": {Data: []byte("-- +goose Up\nCREATE TABLE widgets (id bigserial PRIMARY KEY);\n-- +goose Down\nDROP TABLE widgets;\n")},
	"00002_name.sql": {Data: []byte("-- +goose Up\nALTER TABLE widgets ADD COLUMN name text;\n-- +goose Down\nALTER TABLE widgets DROP COLUMN name;\n")},
}

func newDB(dsn, mode string) *gormDB {
	g := NewGormDB("db", "", WithMigrations(migrations))
	g.dsn, g.migrateMode = dsn, mode
	g.maxOpenConnections, g.maxIdleConnections, g.maxConnectionIdleTime = 5, 2, 60
	return g
}

func TestActivateAppliesMigrationsOnce(t *testing.T) {
	dsn := testdb.NewDSN(t)

	for i := 0; i < 2; i++ { // second start: nothing left to apply
		g := newDB(dsn, MigrateAuto)
		if err := g.Activate(fakeSC{}); err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
		if err := g.GetDB().Exec("INSERT INTO widgets (name) VALUES ('x')").Error; err != nil {
			t.Fatalf("schema not migrated: %v", err)
		}
		_ = g.Stop()
	}
}

// Replicas starting together: the advisory lock makes each migration run once.
func TestConcurrentStartsMigrateOnce(t *testing.T) {
	dsn := testdb.NewDSN(t)
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() {
			g := newDB(dsn, MigrateAuto)
			err := g.Activate(fakeSC{})
			if err == nil {
				_ = g.Stop()
			}
			errs <- err
		}()
	}
	for i := 0; i < 4; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigrateModes(t *testing.T) {
	dsn := testdb.NewDSN(t)

	off := newDB(dsn, MigrateOff)
	if err := off.Activate(fakeSC{}); err != nil {
		t.Fatal(err)
	}
	if err := off.GetDB().Exec("SELECT 1 FROM widgets").Error; err == nil {
		t.Fatal("migrations ran with --db-migrate=off")
	}
	_ = off.Stop()

	only := newDB(dsn, MigrateOnly)
	if err := only.Activate(fakeSC{}); err != nil || !only.MigrateOnly() {
		t.Fatalf("only: %v", err)
	}
	_ = only.Stop()
}

func TestActivateValidation(t *testing.T) {
	if err := newDB("", MigrateAuto).Activate(fakeSC{}); err == nil {
		t.Error("expected error for missing dsn")
	}
	if err := newDB("postgres://x@127.0.0.1:1/x", "sometimes").Activate(fakeSC{}); err == nil {
		t.Error("expected error for invalid migrate mode")
	}
	if err := newDB("postgres://x@127.0.0.1:1/x?connect_timeout=1", MigrateAuto).Activate(fakeSC{}); err == nil {
		t.Error("expected error for unreachable database")
	}
}
