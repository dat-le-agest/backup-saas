package tenancy

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"core/apperr"
	"core/reqctx"

	"gorm.io/driver/sqlite"
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

func setup(t *testing.T) (*gorm.DB, *sqlRecorder) {
	t.Helper()
	rec := &sqlRecorder{Interface: gormlogger.Discard}
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: rec})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sqlDB.Close() })

	if err := db.AutoMigrate(&item{}); err != nil {
		t.Fatal(err)
	}
	db.Create(&[]item{{TenantID: "t1", Name: "a"}, {TenantID: "t1", Name: "b"}, {TenantID: "t2", Name: "c"}})
	rec.sql = nil
	return db, rec
}

func tenant(id string) context.Context {
	return reqctx.WithIdentity(context.Background(), reqctx.Identity{UserID: "u", TenantID: id})
}

func TestScopeFiltersByTenant(t *testing.T) {
	db, _ := setup(t)

	var items []item
	if err := db.Scopes(Scope(tenant("t1"))).Find(&items).Error; err != nil || len(items) != 2 {
		t.Fatalf("t1: %v %v", items, err)
	}

	// t2's row is invisible to t1 even when addressed by id
	var it item
	if err := db.Scopes(Scope(tenant("t1"))).First(&it, 3).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("cross-tenant read: %v %v", it, err)
	}

	// updates and deletes are scoped too
	res := db.Model(&item{}).Scopes(Scope(tenant("t1"))).Where("id = ?", 3).Update("name", "hacked")
	if res.Error != nil || res.RowsAffected != 0 {
		t.Fatalf("cross-tenant update affected %d rows (%v)", res.RowsAffected, res.Error)
	}
	res = db.Scopes(Scope(tenant("t1"))).Delete(&item{}, 3)
	if res.Error != nil || res.RowsAffected != 0 {
		t.Fatalf("cross-tenant delete affected %d rows (%v)", res.RowsAffected, res.Error)
	}
}

func TestScopeQualifiesColumnInJoins(t *testing.T) {
	db, rec := setup(t)
	var items []item
	// self-join: an unqualified tenant_id would be ambiguous
	err := db.Scopes(Scope(tenant("t1"))).
		Joins("JOIN items AS other ON other.id = items.id").
		Find(&items).Error
	if err != nil || len(items) != 2 {
		t.Fatalf("join: %v %v (sql: %v)", items, err, rec.sql)
	}
}

func TestScopeFailsClosedWithoutTenant(t *testing.T) {
	cases := map[string]struct {
		ctx  context.Context
		want error
	}{
		"anonymous": {context.Background(), apperr.ErrUnauthenticated},
		"no tenant": {reqctx.WithIdentity(context.Background(), reqctx.Identity{UserID: "u"}), apperr.ErrForbidden},
	}
	for name, tc := range cases {
		db, rec := setup(t)

		var items []item
		err := db.Scopes(Scope(tc.ctx)).Find(&items).Error
		if !errors.Is(err, tc.want) || len(items) != 0 {
			t.Errorf("%s find: %v %v", name, items, err)
		}
		if err := db.Scopes(Scope(tc.ctx)).Delete(&item{}, 1).Error; !errors.Is(err, tc.want) {
			t.Errorf("%s delete: %v", name, err)
		}
		if len(rec.sql) != 0 {
			t.Errorf("%s: statements reached the database: %v", name, rec.sql)
		}

		var count int64
		db.Model(&item{}).Count(&count)
		if count != 3 {
			t.Errorf("%s: rows deleted without tenant, %d left", name, count)
		}
	}
}
