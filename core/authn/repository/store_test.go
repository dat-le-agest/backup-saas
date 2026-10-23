package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"core/authn/migrations"
	"core/authn/model"
	"core/testdb"
	"core/uow"
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
