package migrations

import (
	"context"
	"database/sql"
	"testing"

	"core/testdb"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func provider(t *testing.T, db *sql.DB) *goose.Provider {
	t.Helper()
	p, err := goose.NewProvider(goose.DialectPostgres, db, FS)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// 00003_apps on a database with data from before apps: existing tenants go to
// "notes", outstanding refresh tokens are revoked.
func TestAppsMigrationKeepsExistingData(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("pgx", testdb.NewDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	p := provider(t, db)

	if _, err := p.UpTo(ctx, 2); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO tenants (id, name) VALUES ('t1', 'Acme')`,
		`INSERT INTO users (id) VALUES ('u1')`,
		`INSERT INTO memberships (tenant_id, user_id, role, version) VALUES ('t1', 'u1', 'owner', 1)`,
		`INSERT INTO refresh_tokens (token_hash, family_id, user_id, tenant_id, expires_at) VALUES ('h1', 'f1', 'u1', 't1', now() + interval '1 day')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	if _, err := p.Up(ctx); err != nil {
		t.Fatal(err)
	}

	var app string
	if err := db.QueryRowContext(ctx, `SELECT app_id FROM tenants WHERE id = 't1'`).Scan(&app); err != nil || app != "notes" {
		t.Fatalf("tenant app: %q %v", app, err)
	}
	var revoked bool
	if err := db.QueryRowContext(ctx, `SELECT revoked_at IS NOT NULL FROM refresh_tokens WHERE token_hash = 'h1'`).Scan(&revoked); err != nil || !revoked {
		t.Fatalf("old refresh token revoked: %v %v", revoked, err)
	}

	// and back down
	if _, err := p.DownTo(ctx, 2); err != nil {
		t.Fatalf("down: %v", err)
	}
}

// On an empty database no app is created: iam registers its --iam-apps.
func TestAppsMigrationOnEmptyDatabase(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("pgx", testdb.NewDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := provider(t, db).Up(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM apps`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("apps: %d %v", n, err)
	}
}
