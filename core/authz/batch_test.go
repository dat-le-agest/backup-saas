package authz

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"core/apperr"
)

// fakeBatch records BatchCheck calls and answers from allow.
type fakeBatch struct {
	mu    sync.Mutex
	allow map[RelationCheck]bool
	fail  map[RelationCheck]error
	err   error
	calls [][]RelationCheck
	cons  []Consistency
}

func (f *fakeBatch) check(_ context.Context, checks []RelationCheck, c Consistency) ([]RelationResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, checks)
	f.cons = append(f.cons, c)
	if f.err != nil {
		return nil, f.err
	}
	out := make([]RelationResult, len(checks))
	for i, c := range checks {
		out[i] = RelationResult{Allowed: f.allow[c], Err: f.fail[c]}
	}
	return out, nil
}

func note(id, tenant string) Resource { return Resource{Type: "note", ID: id, TenantID: tenant} }

func TestReBACCheckManyInOneCall(t *testing.T) {
	f := &fakeBatch{
		allow: map[RelationCheck]bool{
			{"note:1", "can_read", "user:u1"}:   true,
			{"note:1", "can_update", "user:u1"}: true,
		},
		fail: map[RelationCheck]error{
			{"note:9", "can_read", "user:u1"}: apperr.FromCode("invalid_input", "too complex"),
		},
	}
	single := &fakeChecker{}
	az := NewReBAC(single, WithBatchChecker(f.check))

	errs := CheckMany(as("u1", "t1", ""), az, []Request{
		{Read, note("1", "t1")},
		{Update, note("1", "t1")},
		{Delete, note("1", "t1")},
		{Read, note("2", "t2")}, // other tenant: decided locally
		{Read, note("9", "t1")}, // evaluation error
	})
	want := []error{nil, nil, apperr.ErrForbidden, apperr.ErrNotFound, apperr.ErrInvalid}
	for i := range want {
		if want[i] == nil && errs[i] != nil || want[i] != nil && !errors.Is(errs[i], want[i]) {
			t.Errorf("request %d: got %v, want %v", i, errs[i], want[i])
		}
	}
	if len(f.calls) != 1 || len(f.calls[0]) != 4 {
		t.Fatalf("want one call with 4 checks, got %v", f.calls)
	}
	if f.cons[0] != Fresh {
		t.Errorf("update/delete in the batch: want Fresh, got %v", f.cons[0])
	}
	if len(single.calls) != 0 {
		t.Errorf("single checks made: %v", single.calls)
	}
}

func TestReBACCheckManyReadsUseTheCache(t *testing.T) {
	f := &fakeBatch{}
	az := NewReBAC(&fakeChecker{}, WithBatchChecker(f.check))
	CheckMany(as("u1", "t1", ""), az, []Request{{Read, note("1", "t1")}, {Read, note("2", "t1")}})
	if f.cons[0] != MinimizeLatency {
		t.Errorf("reads only: got %v", f.cons[0])
	}
}

func TestReBACCheckManySplitsLargeBatches(t *testing.T) {
	f := &fakeBatch{allow: map[RelationCheck]bool{}}
	reqs := make([]Request, MaxBatchChecks*2+1)
	for i := range reqs {
		id := fmt.Sprint(i)
		reqs[i] = Request{Read, note(id, "t1")}
		f.allow[RelationCheck{"note:" + id, "can_read", "user:u1"}] = i%2 == 0
	}
	errs := CheckMany(as("u1", "t1", ""), NewReBAC(&fakeChecker{}, WithBatchChecker(f.check)), reqs)
	if len(f.calls) != 3 {
		t.Errorf("want 3 calls, got %d", len(f.calls))
	}
	for i, err := range errs { // results land at the right index
		if (err == nil) != (i%2 == 0) {
			t.Errorf("request %d: %v", i, err)
		}
	}
}

func TestReBACCheckManyFailsClosed(t *testing.T) {
	f := &fakeBatch{err: apperr.ErrUnavailable}
	errs := CheckMany(as("u1", "t1", ""), NewReBAC(&fakeChecker{}, WithBatchChecker(f.check)),
		[]Request{{Read, note("1", "t1")}, {Read, note("2", "t1")}})
	for i, err := range errs {
		if !errors.Is(err, apperr.ErrUnavailable) {
			t.Errorf("request %d: got %v", i, err)
		}
	}

	short := func(context.Context, []RelationCheck, Consistency) ([]RelationResult, error) {
		return []RelationResult{{Allowed: true}}, nil // one result for two checks
	}
	errs = CheckMany(as("u1", "t1", ""), NewReBAC(&fakeChecker{}, WithBatchChecker(short)),
		[]Request{{Read, note("1", "t1")}, {Read, note("2", "t1")}})
	if errs[0] == nil || errs[1] == nil {
		t.Errorf("mismatched results must deny all: %v", errs)
	}
}

func TestCheckManyFallsBackToCheck(t *testing.T) {
	// ReBAC without a batch checker: one Check each
	single := &fakeChecker{allow: map[call]bool{{"note:1", "can_read", "user:u1"}: true}}
	errs := CheckMany(as("u1", "t1", ""), NewReBAC(single), []Request{{Read, note("1", "t1")}, {Read, note("2", "t1")}})
	if errs[0] != nil || !errors.Is(errs[1], apperr.ErrForbidden) || len(single.calls) != 2 {
		t.Errorf("got %v after %v", errs, single.calls)
	}

	// RBAC and any other Authorizer
	errs = CheckMany(as("u1", "t1", RoleViewer), NewRBAC(policy), []Request{{Read, note("1", "t1")}, {Update, note("1", "t1")}})
	if errs[0] != nil || !errors.Is(errs[1], apperr.ErrForbidden) {
		t.Errorf("rbac: %v", errs)
	}
	errs = CheckMany(as("u1", "t1", ""), authorizerFunc(func(context.Context, string, Resource) error { return apperr.ErrForbidden }),
		[]Request{{Read, note("1", "t1")}})
	if !errors.Is(errs[0], apperr.ErrForbidden) {
		t.Errorf("plain authorizer: %v", errs)
	}
}

type authorizerFunc func(ctx context.Context, action string, res Resource) error

func (f authorizerFunc) Check(ctx context.Context, action string, res Resource) error {
	return f(ctx, action, res)
}
