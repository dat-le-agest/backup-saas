package repository

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"core/apperr"
	"core/testdb"
	"core/uow"
	"iam/internal/model"
	"iam/internal/service"
	"iam/migrations"
)

// Concurrent failures must all be counted: a read-then-write increment loses
// updates under Postgres' concurrent transactions.
func TestRecordLoginFailureCountsConcurrentFailures(t *testing.T) {
	db := testdb.New(t, migrations.FS)
	s := NewStore(uow.New(db))
	const n = 25
	now := time.Now()

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- s.RecordLoginFailure(context.Background(), "alice@example.com", now, time.Hour, 1000, time.Minute)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	var throttle model.LoginThrottle
	db.Where("email = ?", "alice@example.com").First(&throttle)
	if throttle.Failures != n {
		t.Fatalf("counted %d failures, want %d", throttle.Failures, n)
	}
}

func TestRecordLoginFailureLocksAndResets(t *testing.T) {
	db := testdb.New(t, migrations.FS)
	s := NewStore(uow.New(db))
	ctx := context.Background()
	now := time.Now()

	for i := 0; i < 3; i++ {
		_ = s.RecordLoginFailure(ctx, "a@example.com", now, time.Hour, 3, 15*time.Minute)
	}
	got, _ := s.GetLoginThrottle(ctx, "a@example.com")
	if got.LockedUntil == nil || got.LockedUntil.Sub(now) < 14*time.Minute || got.Failures != 0 {
		t.Fatalf("not locked after 3 failures: %+v", got)
	}

	// after the lock expired, counting starts over
	later := got.LockedUntil.Add(time.Second)
	_ = s.RecordLoginFailure(ctx, "a@example.com", later, time.Hour, 3, 15*time.Minute)
	got, _ = s.GetLoginThrottle(ctx, "a@example.com")
	if got.LockedUntil != nil || got.Failures != 1 {
		t.Fatalf("after expiry: %+v", got)
	}

	// failures older than the window are forgotten
	_ = s.RecordLoginFailure(ctx, "a@example.com", later.Add(2*time.Hour), time.Hour, 3, 15*time.Minute)
	got, _ = s.GetLoginThrottle(ctx, "a@example.com")
	if got.Failures != 1 {
		t.Fatalf("old failures kept: %+v", got)
	}
}

// Tables of an app live in its schema: the same store call reaches the app of ctx only.
func TestAppTablesAreInTheAppSchema(t *testing.T) {
	dsn := testdb.NewDSN(t)
	db := testdb.Open(t, dsn, migrations.FS)
	for _, app := range []string{"notes", "my-app"} {
		testdb.UpInSchema(t, dsn, migrations.AppFS, app)
	}
	s := NewStore(uow.New(db))
	notes := service.InApp(context.Background(), "notes")
	mine := service.InApp(context.Background(), "my-app")

	if err := s.EnsureUser(notes, "u1"); err != nil { // users are shared
		t.Fatal(err)
	}
	if err := s.CreateTenant(notes, &model.Tenant{ID: "t1", Name: "Acme"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveMembership(notes, &model.Membership{TenantID: "t1", UserID: "u1", Role: "owner", Version: 1}); err != nil {
		t.Fatal(err)
	}

	if got, err := s.GetTenant(notes, "t1"); err != nil || got.AppID != "notes" {
		t.Fatalf("notes tenant: %+v %v", got, err)
	}
	if _, err := s.GetTenant(mine, "t1"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("tenant visible from another app: %v", err)
	}
	if list, err := s.ListTenantsForUser(mine, "u1"); err != nil || len(list) != 0 {
		t.Fatalf("my-app tenants: %v %v", list, err)
	}
	if list, err := s.ListTenantsForUser(notes, "u1"); err != nil || len(list) != 1 || list[0].Tenant.AppID != "notes" {
		t.Fatalf("notes tenants: %v %v", list, err)
	}

	// no app in ctx: an error, never a fallback to some schema
	if _, err := s.GetTenant(context.Background(), "t1"); err == nil || errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("no app: %v", err)
	}
	if _, err := s.GetTenant(service.InApp(context.Background(), `x"; DROP TABLE users; --`), "t1"); err == nil {
		t.Fatal("invalid app accepted")
	}
}
