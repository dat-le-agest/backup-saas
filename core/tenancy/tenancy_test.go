package tenancy_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"core/apperr"
	"core/reqctx"
	. "core/tenancy"
	"core/testdb"
	"core/uow"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type item struct {
	ID       uint
	TenantID string
	Name     string
}

// sqlRecorder records every statement gorm actually sends to the database.
type sqlRecorder struct {
	gormlogger.Interface
	mu  sync.Mutex
	sql []string
}

func (r *sqlRecorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	s, _ := fc()
	r.mu.Lock()
	r.sql = append(r.sql, s)
	r.mu.Unlock()
}

const schema = `
CREATE TABLE items (id bigserial PRIMARY KEY, tenant_id varchar(64) NOT NULL, name text NOT NULL);
ALTER TABLE items ENABLE ROW LEVEL SECURITY;
ALTER TABLE items FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON items
  USING (tenant_id = current_setting('app.tenant_id', true))
  WITH CHECK (tenant_id = current_setting('app.tenant_id', true));`

func tenant(id string) context.Context {
	return reqctx.WithIdentity(context.Background(), reqctx.Identity{UserID: "u", TenantID: id})
}

func setup(t *testing.T) *gorm.DB {
	t.Helper()
	db := testdb.New(t)
	if err := db.Exec(schema).Error; err != nil {
		t.Fatal(err)
	}
	// deterministic order (not a map): t1 gets ids 1-2, t2 gets id 3
	for _, row := range []item{{TenantID: "t1", Name: "a"}, {TenantID: "t1", Name: "b"}, {TenantID: "t2", Name: "c"}} {
		err := uow.New(db).Tx(tenant(row.TenantID), func(tx *gorm.DB) error {
			return tx.Create(&row).Error
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestScopeFiltersByTenant(t *testing.T) {
	db := setup(t)
	ctx := tenant("t1")

	err := uow.New(db).Tx(ctx, func(tx *gorm.DB) error {
		var items []item
		if err := tx.Scopes(Scope(ctx)).Find(&items).Error; err != nil || len(items) != 2 {
			t.Errorf("t1: %v %v", items, err)
		}
		var it item
		if err := tx.Scopes(Scope(ctx)).Where("name = ?", "c").First(&it).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Errorf("cross-tenant read: %v %v", it, err)
		}
		res := tx.Model(&item{}).Scopes(Scope(ctx)).Where("name = ?", "c").Update("name", "hacked")
		if res.Error != nil || res.RowsAffected != 0 {
			t.Errorf("cross-tenant update affected %d rows (%v)", res.RowsAffected, res.Error)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Row-level security alone (no Scope) hides and protects other tenants' rows.
func TestRowLevelSecurityWithoutScope(t *testing.T) {
	db := setup(t)

	err := uow.New(db).Tx(tenant("t1"), func(tx *gorm.DB) error {
		var n int64
		tx.Model(&item{}).Count(&n) // forgot Scope
		if n != 2 {
			t.Errorf("t1 sees %d rows, want 2", n)
		}
		res := tx.Exec("UPDATE items SET name = 'hacked'") // no WHERE at all
		if res.RowsAffected != 2 {
			t.Errorf("unscoped update touched %d rows, want only t1's 2", res.RowsAffected)
		}
		res = tx.Exec("DELETE FROM items WHERE tenant_id = 't2'")
		if res.RowsAffected != 0 {
			t.Errorf("deleted %d rows of t2", res.RowsAffected)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// WITH CHECK: can't write a row for another tenant
	err = uow.New(db).Tx(tenant("t1"), func(tx *gorm.DB) error {
		return tx.Create(&item{TenantID: "t2", Name: "planted"}).Error
	})
	if err == nil {
		t.Fatal("insert into another tenant was allowed")
	}

	// t2 is untouched
	_ = uow.New(db).Tx(tenant("t2"), func(tx *gorm.DB) error {
		var it item
		tx.First(&it)
		if it.Name != "c" {
			t.Errorf("t2 row changed: %+v", it)
		}
		return nil
	})
}

func TestNoTenantSettingSeesNothing(t *testing.T) {
	db := setup(t)

	// outside Tx the setting is unset: RLS returns no rows instead of all of them
	var n int64
	db.Model(&item{}).Count(&n)
	if n != 0 {
		t.Fatalf("query without tenant setting sees %d rows", n)
	}

	// the setting is transaction-local: it doesn't leak to the next use of the connection
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	_ = uow.New(db).Tx(tenant("t1"), func(*gorm.DB) error { return nil })
	var setting string
	db.Raw("SELECT coalesce(current_setting('app.tenant_id', true), '')").Scan(&setting)
	if setting != "" {
		t.Fatalf("tenant setting leaked: %q", setting)
	}
}

func TestScopeQualifiesColumnInJoins(t *testing.T) {
	db := setup(t)
	ctx := tenant("t1")
	_ = uow.New(db).Tx(ctx, func(tx *gorm.DB) error {
		var items []item
		err := tx.Scopes(Scope(ctx)).
			Joins("JOIN items AS other ON other.id = items.id").
			Find(&items).Error
		if err != nil || len(items) != 2 {
			t.Errorf("join: %v %v", items, err)
		}
		return nil
	})
}

func TestFailsClosedWithoutTenant(t *testing.T) {
	cases := map[string]struct {
		ctx  context.Context
		want error
	}{
		"anonymous": {context.Background(), apperr.ErrUnauthenticated},
		"no tenant": {reqctx.WithIdentity(context.Background(), reqctx.Identity{UserID: "u"}), apperr.ErrForbidden},
	}
	db := setup(t)
	for name, tc := range cases {
		rec := &sqlRecorder{Interface: gormlogger.Discard}
		session := db.Session(&gorm.Session{Logger: rec})

		var items []item
		if err := session.Scopes(Scope(tc.ctx)).Find(&items).Error; !errors.Is(err, tc.want) {
			t.Errorf("%s Scope: %v", name, err)
		}
		if len(rec.sql) != 0 {
			t.Errorf("%s: statements reached the database: %v", name, rec.sql)
		}
	}
}
