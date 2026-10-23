package migrate_test

import (
	"context"
	"database/sql"
	"testing"
	"testing/fstest"

	"core/logger"
	"core/migrate"
	"core/testdb"

	_ "github.com/jackc/pgx/v5/stdlib"
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

// a table with a changelog trigger: the trigger function must keep writing to
// its own schema whatever the caller's search_path
var perSchema = fstest.MapFS{
	"00001_items.sql": {Data: []byte(`-- +goose Up
CREATE TABLE items (id bigint PRIMARY KEY);
CREATE TABLE item_log (id bigint NOT NULL);
-- +goose StatementBegin
CREATE FUNCTION log_item() RETURNS trigger LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
BEGIN
    INSERT INTO item_log (id) VALUES (NEW.id);
    RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE TRIGGER log_item AFTER INSERT ON items FOR EACH ROW EXECUTE FUNCTION log_item();
-- +goose Down
DROP TABLE items;
DROP TABLE item_log;
DROP FUNCTION log_item();
`)},
}

func TestUpInSchema(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.NewDSN(t)

	for _, schema := range []string{"notes", "my-app", "notes"} { // the second "notes" is a no-op
		if err := migrate.UpInSchema(ctx, dsn, perSchema, schema, nopLogger{}); err != nil {
			t.Fatalf("%s: %v", schema, err)
		}
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// the caller's search_path doesn't matter: tables are qualified, the trigger is pinned
	if _, err := db.ExecContext(ctx, `INSERT INTO "my-app".items (id) VALUES (7)`); err != nil {
		t.Fatal(err)
	}
	var mine, others int
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM "my-app".item_log`).Scan(&mine)
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM notes.item_log`).Scan(&others)
	if mine != 1 || others != 0 {
		t.Fatalf("trigger wrote my-app=%d notes=%d", mine, others)
	}

	// each schema has its own version table
	var version int64
	if err := db.QueryRowContext(ctx, `SELECT max(version_id) FROM notes.goose_db_version`).Scan(&version); err != nil || version != 1 {
		t.Fatalf("notes version %d %v", version, err)
	}
}

func TestQuoteSchema(t *testing.T) {
	if q, err := migrate.QuoteSchema("my-app"); err != nil || q != `"my-app"` {
		t.Fatalf("got %q %v", q, err)
	}
	for _, bad := range []string{"", "Notes", "1app", `x"; DROP TABLE users; --`, "a.b"} {
		if _, err := migrate.QuoteSchema(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
