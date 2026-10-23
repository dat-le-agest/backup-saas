package authz

import (
	"context"
	"errors"
	"testing"

	"core/apperr"
	"core/reqctx"
)

type call struct{ object, relation, subject string }

type fakeChecker struct {
	allow       map[call]bool
	err         error
	calls       []call
	consistency []Consistency
}

func (f *fakeChecker) Check(_ context.Context, object, relation, subject string, cons Consistency) (bool, error) {
	c := call{object, relation, subject}
	f.calls = append(f.calls, c)
	f.consistency = append(f.consistency, cons)
	return f.allow[c], f.err
}

func TestReBACMapsToRelations(t *testing.T) {
	f := &fakeChecker{allow: map[call]bool{
		{"note:42", "can_update", "user:u1"}:        true,
		{"tenant:t1", "can_create_note", "user:u1"}: true,
		{"tenant:t1", "can_read_note", "user:u1"}:   true,
	}}
	az := NewReBAC(f)
	ctx := as("u1", "t1", "") // no role needed: the model decides

	cases := []struct {
		action string
		res    Resource
		want   error
		called call
	}{
		{Update, Resource{Type: "note", ID: "42", TenantID: "t1"}, nil, call{"note:42", "can_update", "user:u1"}},
		{Delete, Resource{Type: "note", ID: "42", TenantID: "t1"}, apperr.ErrForbidden, call{"note:42", "can_delete", "user:u1"}},
		{Create, Resource{Type: "note", TenantID: "t1", OwnerID: "u1"}, nil, call{"tenant:t1", "can_create_note", "user:u1"}},
		{Read, Resource{Type: "note"}, nil, call{"tenant:t1", "can_read_note", "user:u1"}}, // tenant from the caller
	}
	for _, tc := range cases {
		f.calls = nil
		err := az.Check(ctx, tc.action, tc.res)
		if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("%s %+v: got %v, want %v", tc.action, tc.res, err, tc.want)
		}
		if len(f.calls) != 1 || f.calls[0] != tc.called {
			t.Errorf("%s %+v: asked %v, want %v", tc.action, tc.res, f.calls, tc.called)
		}
	}
}

func TestReBACGuardsBeforeAsking(t *testing.T) {
	f := &fakeChecker{}
	az := NewReBAC(f)
	res := Resource{Type: "note", ID: "1", TenantID: "t1"}

	if err := az.Check(context.Background(), Read, res); !errors.Is(err, apperr.ErrUnauthenticated) {
		t.Errorf("anonymous: %v", err)
	}
	if err := az.Check(as("u1", "", ""), Read, res); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("no tenant: %v", err)
	}
	if err := az.Check(as("u1", "t2", ""), Read, res); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("other tenant: %v", err)
	}
	// without an app the store can't be chosen
	noApp := reqctx.WithIdentity(context.Background(), reqctx.Identity{UserID: "u1", TenantID: "t1"})
	if err := az.Check(noApp, Read, res); !errors.Is(err, apperr.ErrUnauthenticated) {
		t.Errorf("no app: %v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("authz service asked anyway: %v", f.calls)
	}
}

func TestReBACFailsClosed(t *testing.T) {
	az := NewReBAC(&fakeChecker{err: apperr.ErrUnavailable})
	err := az.Check(as("u1", "t1", ""), Read, Resource{Type: "note", ID: "1", TenantID: "t1"})
	if !errors.Is(err, apperr.ErrUnavailable) {
		t.Fatalf("authz down must deny with the upstream error, got %v", err)
	}
}

func TestReBACCustomRelations(t *testing.T) {
	f := &fakeChecker{allow: map[call]bool{{"doc:7", "edit", "user:u1"}: true}}
	az := NewReBAC(f, WithRelations(func(action string, res Resource) (string, string) {
		return res.Type + ":" + res.ID, map[string]string{Update: "edit"}[action]
	}))
	if err := az.Check(as("u1", "t1", ""), Update, Resource{Type: "doc", ID: "7", TenantID: "t1"}); err != nil {
		t.Fatal(err)
	}
}

func TestReBACConsistency(t *testing.T) {
	note := Resource{Type: "note", ID: "1", TenantID: "t1"}
	cases := []struct {
		name   string
		opts   []ReBACOption
		ctx    context.Context
		action string
		want   Consistency
	}{
		{"read takes the cache", nil, as("u1", "t1", ""), Read, MinimizeLatency},
		{"create is fresh", nil, as("u1", "t1", ""), Create, Fresh},
		{"update is fresh", nil, as("u1", "t1", ""), Update, Fresh},
		{"delete is fresh", nil, as("u1", "t1", ""), Delete, Fresh},
		{"WithFresh forces a read", nil, WithFresh(as("u1", "t1", "")), Read, Fresh},
		{"custom fresh actions", []ReBACOption{WithFreshActions(Delete)}, as("u1", "t1", ""), Update, MinimizeLatency},
		{"custom fresh actions keep theirs", []ReBACOption{WithFreshActions(Delete)}, as("u1", "t1", ""), Delete, Fresh},
		{"no fresh actions, WithFresh still applies", []ReBACOption{WithFreshActions()}, WithFresh(as("u1", "t1", "")), Delete, Fresh},
	}
	for _, tc := range cases {
		f := &fakeChecker{}
		_ = NewReBAC(f, tc.opts...).Check(tc.ctx, tc.action, note)
		if len(f.consistency) != 1 || f.consistency[0] != tc.want {
			t.Errorf("%s: asked with %v, want %v", tc.name, f.consistency, tc.want)
		}
	}
}
