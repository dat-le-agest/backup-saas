package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"authz/internal/repository"
	"authz/migrations"
	"core/apperr"
	"core/reqctx"
	"core/testdb"
	"core/uow"
)

const model = `
model
  schema 1.1
type user
type tenant
  relations
    define owner: [user]
    define admin: [user] or owner
    define member: [user] or admin
type team
  relations
    define member: [user, team#member]
type note
  relations
    define tenant: [tenant]
    define owner: [user]
    define editor: [user, team#member] or owner or admin from tenant
    define viewer: [user, user:*, team#member] or editor or member from tenant
`

func newService(t *testing.T) *AuthzService {
	t.Helper()
	db := testdb.New(t, migrations.FS) // real Postgres, migrations (incl. triggers) applied
	u := uow.New(db)
	return NewAuthzService(repository.NewStore(u), u)
}

func w(object, relation, subject string) TupleInput {
	return TupleInput{Object: object, Relation: relation, Subject: subject, TenantID: "tnt_a"}
}

func TestEndToEndOnPostgres(t *testing.T) {
	s := newService(t)
	ctx := reqctx.WithRequestID(
		reqctx.WithIdentity(context.Background(), reqctx.Identity{UserID: "usr_admin"}), "req-7")

	if _, err := s.Check(ctx, "note:1", "viewer", "user:a", "", MinimizeLatency); !errors.Is(err, apperr.ErrInvalid) {
		t.Fatalf("no active model: %v", err)
	}
	id, err := s.WriteModel(ctx, model, true)
	if err != nil {
		t.Fatal(err)
	}

	err = s.WriteTuples(ctx, []TupleInput{
		w("tenant:acme", "admin", "user:adam"),
		w("tenant:acme", "member", "user:mia"),
		w("team:eng", "member", "user:erin"),
		w("note:1", "tenant", "tenant:acme"),
		w("note:1", "owner", "user:mia"),
		w("note:2", "tenant", "tenant:acme"),
		w("note:2", "editor", "team:eng#member"),
		w("note:3", "viewer", "user:*"),
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// idempotent: writing the same tuples again is fine
	if err := s.WriteTuples(ctx, []TupleInput{w("note:1", "owner", "user:mia")}, nil, nil); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	cases := []struct {
		obj, rel, sub string
		want          bool
	}{
		{"note:1", "editor", "user:mia", true},
		{"note:1", "editor", "user:adam", true},
		{"note:1", "viewer", "user:mia", true},
		{"note:2", "editor", "user:erin", true},
		{"note:2", "editor", "user:mia", false},
		{"note:3", "viewer", "user:stranger", true},
		{"note:1", "viewer", "user:stranger", false},
	}
	for _, tc := range cases {
		got, err := s.Check(ctx, tc.obj, tc.rel, tc.sub, "", MinimizeLatency)
		if err != nil || got != tc.want {
			t.Errorf("%s#%s@%s = %v (%v), want %v", tc.obj, tc.rel, tc.sub, got, err, tc.want)
		}
	}

	objs, err := s.ListObjects(ctx, "note", "viewer", "user:mia", 0, "", MinimizeLatency)
	if err != nil || !reflect.DeepEqual(objs, []string{"note:1", "note:2", "note:3"}) {
		t.Fatalf("list: %v %v", objs, err)
	}

	// invalid tuples are rejected before anything is written
	err = s.WriteTuples(ctx, []TupleInput{w("note:9", "owner", "user:ok"), w("note:9", "editor", "user:*")}, nil, nil)
	if !errors.Is(err, apperr.ErrInvalid) {
		t.Fatalf("wildcard editor accepted: %v", err)
	}
	if ok, _ := s.Check(ctx, "note:9", "owner", "user:ok", "", MinimizeLatency); ok {
		t.Fatal("first tuple of a rejected batch was written")
	}

	// revoke, then the changelog shows the whole history with the actor
	if err := s.WriteTuples(ctx, nil, []TupleInput{w("note:3", "viewer", "user:*"), w("note:404", "owner", "user:x")}, nil); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Check(ctx, "note:3", "viewer", "user:stranger", "", MinimizeLatency); ok {
		t.Fatal("public link still works after delete")
	}
	changes, last, err := s.ReadChanges(ctx, "note", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 6 || changes[5].Operation != "delete" || changes[5].Actor != "user:usr_admin" || last != changes[5].Seq {
		t.Fatalf("changes: %+v last=%d", changes, last)
	}
	if more, l2, _ := s.ReadChanges(ctx, "note", last, 0); len(more) != 0 || l2 != last {
		t.Fatalf("read after last: %v %d", more, l2)
	}

	// model versions: a new active model drops "member from tenant" from viewer
	v2, err := s.WriteModel(ctx, `
model
  schema 1.1
type user
type tenant
  relations
    define owner: [user]
    define admin: [user] or owner
    define member: [user] or admin
type team
  relations
    define member: [user, team#member]
type note
  relations
    define tenant: [tenant]
    define owner: [user]
    define editor: [user, team#member] or owner or admin from tenant
    define viewer: [user, user:*, team#member] or editor
`, true)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Check(ctx, "note:2", "viewer", "user:mia", "", MinimizeLatency); ok {
		t.Error("new active model not used")
	}
	if ok, _ := s.Check(ctx, "note:2", "viewer", "user:mia", id, MinimizeLatency); !ok {
		t.Error("pinned old model not honored")
	}
	if active, err := s.GetModel(ctx, ""); err != nil || active.ID != v2 || !active.IsActive {
		t.Fatalf("active model: %+v %v", active, err)
	}
	if err := s.ActivateModel(ctx, "01JAZ8Q6M2Y3K4P5R6S7T8V9W0"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("activate unknown: %v", err)
	}
	if _, err := s.WriteModel(ctx, "not a model", false); !errors.Is(err, apperr.ErrInvalid) {
		t.Fatalf("bad dsl: %v", err)
	}
}

// Deleting an object removes the tuples on it and the tuples naming it as subject.
func TestDeleteObjects(t *testing.T) {
	s := newService(t)
	ctx := reqctx.WithIdentity(context.Background(), reqctx.Identity{UserID: "usr_admin"})
	if _, err := s.WriteModel(ctx, model, true); err != nil {
		t.Fatal(err)
	}
	err := s.WriteTuples(ctx, []TupleInput{
		w("note:8", "tenant", "tenant:acme"),
		w("note:8", "owner", "user:mia"),
		w("note:8", "viewer", "user:*"),
		w("note:8", "editor", "user:sam"), // a share
		w("team:eng", "member", "user:erin"),
		w("team:eng", "member", "team:platform#member"),
		w("note:2", "editor", "team:eng#member"), // team:eng as a userset subject
		w("note:2", "owner", "user:mia"),         // unrelated, must stay
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.WriteTuples(ctx, nil, nil, []string{"note:8", "team:eng"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ obj, rel, sub string }{
		{"note:8", "viewer", "user:anyone"},
		{"note:8", "editor", "user:sam"},
		{"note:8", "owner", "user:mia"},
		{"team:eng", "member", "user:erin"},
		{"note:2", "editor", "user:erin"},
	} {
		if ok, _ := s.Check(ctx, c.obj, c.rel, c.sub, "", MinimizeLatency); ok {
			t.Errorf("%s#%s@%s still granted", c.obj, c.rel, c.sub)
		}
	}
	if ok, _ := s.Check(ctx, "note:2", "owner", "user:mia", "", MinimizeLatency); !ok {
		t.Error("an unrelated tuple was deleted")
	}

	changes, _, _ := s.ReadChanges(ctx, "", 0, 0)
	deleted := 0
	for _, c := range changes {
		if c.Operation == "delete" {
			deleted++
		}
	}
	if deleted != 7 {
		t.Errorf("changelog has %d deletes, want 7", deleted)
	}

	if err := s.WriteTuples(ctx, nil, nil, []string{"note"}); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("bad object accepted: %v", err)
	}
	// idempotent
	if err := s.WriteTuples(ctx, nil, nil, []string{"note:8"}); err != nil {
		t.Errorf("deleting again: %v", err)
	}
}

// Two replicas on one database: a change through replica B is seen by replica A
// at once with Higher consistency, and within RevisionTTL otherwise.
func TestCacheAcrossReplicas(t *testing.T) {
	db := testdb.New(t, migrations.FS)
	newReplica := func() *AuthzService {
		u := uow.New(db)
		return NewAuthzService(repository.NewStore(u), u)
	}
	a, b := newReplica(), newReplica()
	ctx := reqctx.WithIdentity(context.Background(), reqctx.Identity{UserID: "usr_admin"})
	if _, err := b.WriteModel(ctx, model, true); err != nil {
		t.Fatal(err)
	}
	if err := b.WriteTuples(ctx, []TupleInput{w("note:1", "viewer", "user:mia")}, nil, nil); err != nil {
		t.Fatal(err)
	}

	check := func(s *AuthzService, c Consistency) bool {
		ok, err := s.Check(ctx, "note:1", "viewer", "user:mia", "", c)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !check(a, MinimizeLatency) {
		t.Fatal("A: mia views note:1")
	}

	// B revokes; A's cached answer may survive for at most RevisionTTL
	if err := b.WriteTuples(ctx, nil, []TupleInput{w("note:1", "viewer", "user:mia")}, nil); err != nil {
		t.Fatal(err)
	}
	if check(b, MinimizeLatency) {
		t.Fatal("B must see its own revoke immediately")
	}
	if check(a, Higher) {
		t.Fatal("A with Higher consistency must see the revoke")
	}
	// Higher refreshed A's revision: later reads see the change too
	if check(a, MinimizeLatency) {
		t.Fatal("A kept a stale answer after reading a newer revision")
	}

	// a manual SQL change bumps the revision too (statement trigger)
	before, _ := a.store.Revision(ctx)
	db.Exec(`INSERT INTO relation_tuples (object_type, object_id, relation, subject_type, subject_id, tenant_id)
	         VALUES ('note', '1', 'viewer', 'user', 'mia', 'tnt_a')`)
	after, _ := a.store.Revision(ctx)
	if after <= before {
		t.Fatalf("revision not bumped by SQL: %d → %d", before, after)
	}
	time.Sleep(RevisionTTL + 50*time.Millisecond)
	if !check(a, MinimizeLatency) {
		t.Fatal("A did not pick up the change after RevisionTTL")
	}
}

func TestBatchCheck(t *testing.T) {
	s := newService(t)
	ctx := reqctx.WithIdentity(context.Background(), reqctx.Identity{UserID: "usr_admin"})
	if _, err := s.WriteModel(ctx, model, true); err != nil {
		t.Fatal(err)
	}
	err := s.WriteTuples(ctx, []TupleInput{
		w("tenant:acme", "member", "user:mia"),
		w("note:1", "tenant", "tenant:acme"),
		w("note:1", "owner", "user:mia"),
		w("note:2", "tenant", "tenant:acme"),
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	checks := []CheckInput{
		{"note:1", "editor", "user:mia"}, // owner
		{"note:2", "editor", "user:mia"}, // member only
		{"note:2", "viewer", "user:mia"}, // member from tenant
		{"note:1", "editor", "user:mia"}, // duplicate
		{"note", "viewer", "user:mia"},   // bad object
		{"note:1", "nope", "user:mia"},   // unknown relation
		{"note:1", "viewer", "user:*"},   // wildcard can't be checked
		{"note:3", "viewer", "user:nobody"},
	}
	res, err := s.BatchCheck(ctx, checks, "", MinimizeLatency)
	if err != nil {
		t.Fatal(err)
	}
	want := []bool{true, false, true, true, false, false, false, false}
	failed := map[int]bool{4: true, 5: true, 6: true}
	for i, r := range res {
		if r.Allowed != want[i] {
			t.Errorf("checks[%d] %v: allowed %v, want %v", i, checks[i], r.Allowed, want[i])
		}
		if failed[i] != (r.Err != nil) {
			t.Errorf("checks[%d] %v: err %v", i, checks[i], r.Err)
		}
		if r.Err != nil && !errors.Is(r.Err, apperr.ErrInvalid) {
			t.Errorf("checks[%d]: want an invalid input error, got %v", i, r.Err)
		}
	}
	// field paths point at the item
	var ve *apperr.ValidationError
	if !errors.As(res[4].Err, &ve) || ve.Violations[0].Field != "checks[4].object" {
		t.Errorf("checks[4] error: %#v", res[4].Err)
	}

	if _, err := s.BatchCheck(ctx, nil, "", MinimizeLatency); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("empty batch: %v", err)
	}
	if _, err := s.BatchCheck(ctx, make([]CheckInput, maxBatchChecks+1), "", MinimizeLatency); !errors.Is(err, apperr.ErrInvalid) {
		t.Errorf("too many checks: %v", err)
	}
	if _, err := s.BatchCheck(ctx, checks, "01UNKNOWNMODEL", MinimizeLatency); err == nil {
		t.Error("unknown model accepted")
	}
}
