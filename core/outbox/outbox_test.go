package outbox_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"core/logger"
	"core/outbox"
	"core/reqctx"
	"core/testdb"
	"core/uow"

	"gorm.io/gorm"
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

func setup(t *testing.T) (*gorm.DB, *uow.Gorm, *outbox.Outbox) {
	t.Helper()
	db := testdb.New(t)
	// apply the documented migration exactly as services ship it
	up := outbox.MigrationSQL[:len(outbox.MigrationSQL)-len("-- +goose Down\nDROP TABLE outbox_messages;\n")]
	if err := db.Exec(up).Error; err != nil {
		t.Fatal(err)
	}
	u := uow.New(db)
	return db, u, outbox.New(u)
}

type recorder struct {
	mu   sync.Mutex
	keys []string
	ctxs []context.Context
	fail func(key string, attempts int) error
}

func (r *recorder) handle(ctx context.Context, m outbox.Message) error {
	if r.fail != nil {
		if err := r.fail(m.Key, m.Attempts); err != nil {
			return err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys = append(r.keys, m.Key)
	r.ctxs = append(r.ctxs, ctx)
	return nil
}

func publish(t *testing.T, u *uow.Gorm, ob *outbox.Outbox, ctx context.Context, keys ...string) {
	t.Helper()
	err := u.Do(ctx, func(ctx context.Context) error {
		for _, k := range keys {
			if err := ob.Publish(ctx, "t", k, map[string]string{"k": k}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPublishIsTransactional(t *testing.T) {
	db, u, ob := setup(t)

	if err := ob.Publish(context.Background(), "t", "x", 1); !errors.Is(err, outbox.ErrNotInTransaction) {
		t.Fatalf("outside a unit of work: %v", err)
	}

	boom := errors.New("business failure")
	_ = u.Do(context.Background(), func(ctx context.Context) error {
		_ = ob.Publish(ctx, "t", "rolled-back", 1)
		return boom
	})
	var n int64
	db.Raw(`SELECT count(*) FROM outbox_messages`).Scan(&n)
	if n != 0 {
		t.Fatalf("message survived a rollback: %d", n)
	}
}

func TestRelayDeliversInOrderWithContext(t *testing.T) {
	db, u, ob := setup(t)
	rec := &recorder{}
	relay := ob.NewRelay(db, nopLogger{}, outbox.RelayOptions{})
	relay.Handle("t", rec.handle)

	ctx := reqctx.WithRequestID(reqctx.WithIdentity(context.Background(), reqctx.Identity{UserID: "u1"}), "req-1")
	publish(t, u, ob, ctx, "a", "b")
	publish(t, u, ob, ctx, "c")

	if n, err := relay.RunOnce(context.Background()); err != nil || n != 3 {
		t.Fatalf("run: %d %v", n, err)
	}
	if got := rec.keys; len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("order %v", got)
	}
	if id, _ := reqctx.IdentityFrom(rec.ctxs[0]); id.UserID != "u1" || reqctx.RequestIDFrom(rec.ctxs[0]) != "req-1" {
		t.Fatalf("context not carried: %+v %q", id, reqctx.RequestIDFrom(rec.ctxs[0]))
	}
	if n, _ := relay.RunOnce(context.Background()); n != 0 {
		t.Fatalf("redelivered %d", n)
	}
}

func TestFailureHoldsBackLaterMessages(t *testing.T) {
	db, u, ob := setup(t)
	failing := true
	rec := &recorder{fail: func(key string, _ int) error {
		if key == "b" && failing {
			return errors.New("authz unavailable")
		}
		return nil
	}}
	relay := ob.NewRelay(db, nopLogger{}, outbox.RelayOptions{})
	relay.Handle("t", rec.handle)
	publish(t, u, ob, context.Background(), "a", "b", "c")

	if n, err := relay.RunOnce(context.Background()); err != nil || n != 1 {
		t.Fatalf("first run: %d %v", n, err)
	}
	// b is waiting for its retry: c must not overtake it
	if n, _ := relay.RunOnce(context.Background()); n != 0 || len(rec.keys) != 1 {
		t.Fatalf("c overtook b: %v", rec.keys)
	}
	var attempts int
	var lastErr string
	db.Raw(`SELECT attempts, last_error FROM outbox_messages WHERE key = 'b'`).Row().Scan(&attempts, &lastErr)
	if attempts != 1 || lastErr != "authz unavailable" {
		t.Fatalf("failure not recorded: %d %q", attempts, lastErr)
	}

	failing = false
	db.Exec(`UPDATE outbox_messages SET next_attempt_at = now() WHERE key = 'b'`)
	if n, _ := relay.RunOnce(context.Background()); n != 2 || rec.keys[1] != "b" || rec.keys[2] != "c" {
		t.Fatalf("after recovery: %v", rec.keys)
	}
}

func TestPoisonMessageIsSetAside(t *testing.T) {
	db, u, ob := setup(t)
	rec := &recorder{fail: func(key string, _ int) error {
		if key == "poison" {
			return errors.New("never works")
		}
		return nil
	}}
	relay := ob.NewRelay(db, nopLogger{}, outbox.RelayOptions{MaxAttempts: 2})
	relay.Handle("t", rec.handle)
	publish(t, u, ob, context.Background(), "poison", "next")

	_, _ = relay.RunOnce(context.Background())
	db.Exec(`UPDATE outbox_messages SET next_attempt_at = now()`)
	_, _ = relay.RunOnce(context.Background()) // second failure: set aside
	if n, _ := relay.RunOnce(context.Background()); n != 1 || rec.keys[0] != "next" {
		t.Fatalf("delivery did not continue: %v", rec.keys)
	}
	var failed int64
	db.Raw(`SELECT count(*) FROM outbox_messages WHERE key = 'poison' AND failed_at IS NOT NULL`).Scan(&failed)
	if failed != 1 {
		t.Fatal("poison message not set aside")
	}
}

func TestConcurrentRelaysDeliverOnce(t *testing.T) {
	db, u, ob := setup(t)
	var delivered atomic.Int32
	keys := make([]string, 40)
	for i := range keys {
		keys[i] = string(rune('a'+i%26)) + string(rune('0'+i/26))
	}
	publish(t, u, ob, context.Background(), keys...)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		relay := ob.NewRelay(db, nopLogger{}, outbox.RelayOptions{BatchSize: 5})
		relay.Handle("t", func(context.Context, outbox.Message) error { delivered.Add(1); return nil })
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				_, _ = relay.RunOnce(context.Background())
			}
		}()
	}
	wg.Wait()
	if got := delivered.Load(); got != 40 {
		t.Fatalf("delivered %d times, want 40", got)
	}
}

func TestStartedRelayIsWokenByCommit(t *testing.T) {
	db, u, ob := setup(t)
	got := make(chan string, 1)
	relay := ob.NewRelay(db, nopLogger{}, outbox.RelayOptions{PollInterval: 1 << 40}) // effectively never polls
	relay.Handle("t", func(_ context.Context, m outbox.Message) error { got <- m.Key; return nil })
	relay.Start()
	defer relay.Stop()

	publish(t, u, ob, context.Background(), "wake")
	if k := <-got; k != "wake" {
		t.Fatalf("got %q", k)
	}
}
