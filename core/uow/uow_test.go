package uow_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"core/reqctx"
	"core/testdb"
	"core/uow"

	"gorm.io/gorm"
)

type account struct {
	ID      uint
	Balance int
}

func setup(t *testing.T) (*gorm.DB, *uow.Gorm) {
	t.Helper()
	db := testdb.New(t)
	if err := db.Exec(`CREATE TABLE accounts (id bigserial PRIMARY KEY, balance int NOT NULL)`).Error; err != nil {
		t.Fatal(err)
	}
	db.Create(&[]account{{Balance: 100}, {Balance: 100}})
	return db, uow.New(db)
}

// a repository method written the way the kit's repositories are
func debit(ctx context.Context, u *uow.Gorm, id uint, amount int) error {
	return u.DB(ctx).Model(&account{}).Where("id = ?", id).
		Update("balance", gorm.Expr("balance - ?", amount)).Error
}

func balance(db *gorm.DB, id uint) int {
	var a account
	db.First(&a, id)
	return a.Balance
}

var errBusiness = errors.New("insufficient funds")

func TestCommitAndRollback(t *testing.T) {
	db, u := setup(t)

	err := u.Do(context.Background(), func(ctx context.Context) error {
		if err := debit(ctx, u, 1, 30); err != nil {
			return err
		}
		return debit(ctx, u, 2, 30)
	})
	if err != nil || balance(db, 1) != 70 || balance(db, 2) != 70 {
		t.Fatalf("commit: %v %d %d", err, balance(db, 1), balance(db, 2))
	}

	// the second step fails: the first one is rolled back
	err = u.Do(context.Background(), func(ctx context.Context) error {
		if err := debit(ctx, u, 1, 30); err != nil {
			return err
		}
		return errBusiness
	})
	if !errors.Is(err, errBusiness) || balance(db, 1) != 70 {
		t.Fatalf("rollback: %v %d", err, balance(db, 1))
	}
}

func TestPanicRollsBack(t *testing.T) {
	db, u := setup(t)
	func() {
		defer func() { _ = recover() }()
		_ = u.Do(context.Background(), func(ctx context.Context) error {
			_ = debit(ctx, u, 1, 50)
			panic("boom")
		})
	}()
	if balance(db, 1) != 100 {
		t.Fatalf("panic did not roll back: %d", balance(db, 1))
	}
}

func TestNestedDoJoinsOuterTransaction(t *testing.T) {
	db, u := setup(t)
	err := u.Do(context.Background(), func(ctx context.Context) error {
		// a service calling another use case that also uses Do
		if err := u.Do(ctx, func(ctx context.Context) error { return debit(ctx, u, 1, 10) }); err != nil {
			return err
		}
		// uncommitted write is visible inside the transaction only
		if balance(u.DB(ctx), 1) != 90 || balance(db, 1) != 100 {
			t.Errorf("isolation: inside=%d outside=%d", balance(u.DB(ctx), 1), balance(db, 1))
		}
		return errBusiness
	})
	if !errors.Is(err, errBusiness) || balance(db, 1) != 100 {
		t.Fatalf("inner write survived outer rollback: %v %d", err, balance(db, 1))
	}
}

func TestAfterCommit(t *testing.T) {
	_, u := setup(t)
	var ran atomic.Int32

	_ = u.Do(context.Background(), func(ctx context.Context) error {
		uow.AfterCommit(ctx, func(context.Context) { ran.Add(1) })
		if ran.Load() != 0 {
			t.Error("ran before commit")
		}
		return nil
	})
	if ran.Load() != 1 {
		t.Fatalf("after commit ran %d times", ran.Load())
	}

	_ = u.Do(context.Background(), func(ctx context.Context) error {
		uow.AfterCommit(ctx, func(context.Context) { ran.Add(1) })
		return errBusiness
	})
	if ran.Load() != 1 {
		t.Fatal("ran after rollback")
	}

	// outside a transaction it runs immediately
	uow.AfterCommit(context.Background(), func(context.Context) { ran.Add(1) })
	if ran.Load() != 2 {
		t.Fatal("did not run outside a transaction")
	}
}

// Two transfers lock the same rows in opposite order: Postgres aborts one with a
// deadlock (40P01). Do retries it, so both succeed and the money adds up.
func TestRetriesDeadlock(t *testing.T) {
	db, u := setup(t)
	var attempts, afterCommits atomic.Int32
	barrier := sync.WaitGroup{}
	barrier.Add(2)

	transfer := func(from, to uint) error {
		first := true
		return u.Do(context.Background(), func(ctx context.Context) error {
			attempts.Add(1)
			uow.AfterCommit(ctx, func(context.Context) { afterCommits.Add(1) })
			if err := debit(ctx, u, from, 10); err != nil {
				return err
			}
			if first { // only the first attempt waits, to provoke the deadlock
				first = false
				barrier.Done()
				barrier.Wait()
			}
			return debit(ctx, u, to, -10)
		})
	}

	errs := make(chan error, 2)
	go func() { errs <- transfer(1, 2) }()
	go func() { errs <- transfer(2, 1) }()
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("transfer failed: %v", err)
		}
	}
	if attempts.Load() < 3 {
		t.Fatalf("expected a retry, attempts=%d", attempts.Load())
	}
	if afterCommits.Load() != 2 {
		t.Fatalf("after-commit callbacks ran %d times, want once per committed transfer", afterCommits.Load())
	}
	if balance(db, 1) != 100 || balance(db, 2) != 100 {
		t.Fatalf("balances %d/%d", balance(db, 1), balance(db, 2))
	}
}

func TestTxSetsTenantForRowLevelSecurity(t *testing.T) {
	db, u := setup(t)
	ctx := reqctx.WithIdentity(context.Background(), reqctx.Identity{UserID: "u", TenantID: "t-42"})

	var inTx, outside string
	_ = u.Tx(ctx, func(tx *gorm.DB) error {
		return tx.Raw("SELECT current_setting('app.tenant_id', true)").Scan(&inTx).Error
	})
	db.Raw("SELECT coalesce(current_setting('app.tenant_id', true), '')").Scan(&outside)
	if inTx != "t-42" || outside != "" {
		t.Fatalf("setting in tx %q, outside %q", inTx, outside)
	}

	// joining an outer Do: the setting was applied once, at the start
	_ = u.Do(ctx, func(ctx context.Context) error {
		return u.Tx(ctx, func(tx *gorm.DB) error {
			return tx.Raw("SELECT current_setting('app.tenant_id', true)").Scan(&inTx).Error
		})
	})
	if inTx != "t-42" {
		t.Fatalf("joined tx setting %q", inTx)
	}
}

func TestNoRetryForBusinessErrors(t *testing.T) {
	_, u := setup(t)
	var attempts int
	_ = u.Do(context.Background(), func(ctx context.Context) error {
		attempts++
		return errBusiness
	})
	if attempts != 1 {
		t.Fatalf("business error retried: %d attempts", attempts)
	}
}
