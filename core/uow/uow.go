// Package uow (unit of work) makes a use case atomic: everything repositories do
// with the ctx passed to Do runs in one database transaction.
//
// Services depend on the UnitOfWork interface:
//
//	err := s.uow.Do(ctx, func(ctx context.Context) error {
//		note, err := s.repo.Get(ctx, id)     // same transaction
//		...
//		return s.repo.Update(ctx, id, data)  // same transaction
//	})
//
// Repositories get the transaction from ctx through *Gorm:
//
//	r.uow.DB(ctx)                  // tables without row-level security
//	r.uow.Tx(ctx, func(tx) error)  // tenant tables (RLS): always in a transaction
//	                               // with app.tenant_id set
//
// Rules: don't call other services (gRPC/HTTP) inside Do (the transaction holds
// locks while waiting on the network), and keep fn free of side effects outside
// the database, because Do retries it on deadlocks and serialization failures.
// Use AfterCommit for side effects.
package uow

import (
	"context"
	"database/sql"
	"errors"
	"math/rand/v2"
	"time"

	"core/tenancy"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// UnitOfWork is what services depend on.
type UnitOfWork interface {
	// Do runs fn in a transaction: commit if fn returns nil, rollback otherwise
	// (and on panic). A Do inside another Do joins the outer transaction.
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}

type txKey struct{}

type txState struct {
	tx          *gorm.DB
	afterCommit []func(context.Context)
}

func current(ctx context.Context) *txState {
	st, _ := ctx.Value(txKey{}).(*txState)
	return st
}

// InTx reports whether ctx carries a transaction started by Do.
func InTx(ctx context.Context) bool { return current(ctx) != nil }

// AfterCommit registers fn to run once the outermost transaction commits. It is
// not run on rollback, nor for attempts that are retried. Outside a transaction
// fn runs immediately. Typical use: publish to a cache or message bus.
func AfterCommit(ctx context.Context, fn func(context.Context)) {
	if st := current(ctx); st != nil {
		st.afterCommit = append(st.afterCommit, fn)
		return
	}
	fn(ctx)
}

// Gorm is the UnitOfWork for Postgres through gorm.
type Gorm struct {
	db         *gorm.DB
	maxRetries int
	txOptions  *sql.TxOptions
}

type Option func(*Gorm)

// WithMaxRetries sets how many times a transaction is retried after a deadlock
// or serialization failure (default 3).
func WithMaxRetries(n int) Option { return func(g *Gorm) { g.maxRetries = n } }

// WithTxOptions sets the isolation level (default: Postgres' READ COMMITTED).
func WithTxOptions(opts *sql.TxOptions) Option { return func(g *Gorm) { g.txOptions = opts } }

func New(db *gorm.DB, opts ...Option) *Gorm {
	g := &Gorm{db: db, maxRetries: 3}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

func (g *Gorm) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	if InTx(ctx) {
		return fn(ctx)
	}

	for attempt := 0; ; attempt++ {
		st := &txState{}
		err := g.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			st.tx = tx
			// row-level security: tenant tables only show the caller's rows
			if tenantID, err := tenancy.TenantID(ctx); err == nil {
				if err := tenancy.SetLocal(tx, tenantID); err != nil {
					return err
				}
			}
			return fn(context.WithValue(ctx, txKey{}, st))
		}, g.txOptions)

		if err == nil {
			for _, cb := range st.afterCommit {
				cb(ctx)
			}
			return nil
		}
		if attempt >= g.maxRetries || !Retryable(err) || ctx.Err() != nil {
			return err
		}
		// jittered backoff: 10ms, 20ms, 40ms... (+/- 50%)
		backoff := time.Duration(10<<attempt) * time.Millisecond
		backoff = backoff/2 + time.Duration(rand.Int64N(int64(backoff)))
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return err
		}
	}
}

// DB returns the transaction of ctx, or the plain connection when ctx has none.
// For tables without row-level security.
func (g *Gorm) DB(ctx context.Context) *gorm.DB {
	if st := current(ctx); st != nil {
		return st.tx.WithContext(ctx)
	}
	return g.db.WithContext(ctx)
}

// Tx runs fn in the transaction of ctx, or in a new one (with app.tenant_id set)
// when called outside Do. Tenant tables protected by row-level security must
// always be accessed through Tx: outside a transaction they show no rows.
func (g *Gorm) Tx(ctx context.Context, fn func(tx *gorm.DB) error) error {
	return g.Do(ctx, func(ctx context.Context) error {
		return fn(current(ctx).tx.WithContext(ctx))
	})
}

// Retryable reports Postgres errors after which re-running the whole
// transaction can succeed: serialization failure and deadlock.
func Retryable(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "40001" || pgErr.Code == "40P01"
	}
	return false
}

// Nop runs fn without a transaction. For service unit tests with fake repositories.
type Nop struct{}

func (Nop) Do(ctx context.Context, fn func(ctx context.Context) error) error { return fn(ctx) }
