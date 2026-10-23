package engine

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"core/apperr"
)

// memReader is an in-memory TupleReader.
type memReader struct{ tuples []Tuple }

func (m *memReader) HasDirect(_ context.Context, obj Object, rel string, s Subject) (bool, error) {
	for _, t := range m.tuples {
		if t.Object != obj || t.Relation != rel || t.Subject.Type != s.Type {
			continue
		}
		if t.Subject == s || (s.Relation == "" && t.Subject.ID == "*") {
			return true, nil
		}
	}
	return false, nil
}

func (m *memReader) Usersets(_ context.Context, obj Object, rel string) ([]Subject, error) {
	var out []Subject
	for _, t := range m.tuples {
		if t.Object == obj && t.Relation == rel && t.Subject.Relation != "" {
			out = append(out, t.Subject)
		}
	}
	return out, nil
}

func (m *memReader) DirectObjects(_ context.Context, obj Object, rel string) ([]Object, error) {
	var out []Object
	for _, t := range m.tuples {
		if t.Object == obj && t.Relation == rel && t.Subject.Relation == "" && !t.Subject.IsWildcard() {
			out = append(out, t.Subject.Object())
		}
	}
	return out, nil
}

func (m *memReader) BySubject(_ context.Context, s Subject) ([]Tuple, error) {
	var out []Tuple
	for _, t := range m.tuples {
		if t.Subject == s || (s.Relation == "" && t.Subject.Type == s.Type && t.Subject.ID == "*") {
			out = append(out, t)
		}
	}
	return out, nil
}

const kitModel = `
model
  schema 1.1
type user
type tenant
  relations
    define owner: [user]
    define admin: [user] or owner
    define member: [user] or admin
    define suspended: [user]
type team
  relations
    define tenant: [tenant]
    define member: [user, team#member]
type note
  relations
    define tenant: [tenant]
    define owner: [user]
    define editor: [user, team#member] or owner or admin from tenant
    define viewer: [user, user:*, team#member] or editor or member from tenant
    define can_view: viewer but not suspended from tenant
    define can_edit: editor and member from tenant
    define can_delete: owner or admin from tenant
`

func mustModel(t *testing.T, dsl string) *Model {
	t.Helper()
	p, err := ParseDSL(dsl)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewModel("m1", p)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func tup(t *testing.T, s string) Tuple {
	t.Helper()
	// "note:1#viewer@user:alice"
	left, subj, _ := strings.Cut(s, "@")
	objStr, rel, _ := strings.Cut(left, "#")
	obj, err := ParseObject("object", objStr)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := ParseSubject("subject", subj)
	if err != nil {
		t.Fatal(err)
	}
	return Tuple{Object: obj, Relation: rel, Subject: sub, TenantID: "tnt_a"}
}

func newChecker(t *testing.T, tuples ...string) *Checker {
	t.Helper()
	m := mustModel(t, kitModel)
	r := &memReader{}
	for _, s := range tuples {
		tp := tup(t, s)
		if err := m.ValidateTuple(tp); err != nil {
			t.Fatalf("tuple %s: %v", s, err)
		}
		r.tuples = append(r.tuples, tp)
	}
	return NewChecker(m, r)
}

func check(t *testing.T, c *Checker, obj, rel, subj string) bool {
	t.Helper()
	o, _ := ParseObject("object", obj)
	s, _ := ParseSubject("subject", subj)
	ok, err := c.Check(context.Background(), o, rel, s)
	if err != nil {
		t.Fatalf("check %s#%s@%s: %v", obj, rel, subj, err)
	}
	return ok
}

var kitTuples = []string{
	"tenant:acme#owner@user:olivia",
	"tenant:acme#admin@user:adam",
	"tenant:acme#member@user:mia",
	"tenant:acme#member@user:sam",
	"tenant:acme#suspended@user:sam",
	"team:eng#tenant@tenant:acme",
	"team:eng#member@user:erin",
	"team:eng#member@team:platform#member", // nested team
	"team:platform#member@user:pete",
	"note:1#tenant@tenant:acme",
	"note:1#owner@user:mia",
	"note:2#tenant@tenant:acme",
	"note:2#editor@team:eng#member",
	"note:3#tenant@tenant:globex",
	"note:3#viewer@user:*", // public
	"tenant:globex#member@user:gina",
}

func TestCheck(t *testing.T) {
	c := newChecker(t, kitTuples...)
	cases := []struct {
		obj, rel, subj string
		want           bool
		why            string
	}{
		{"note:1", "owner", "user:mia", true, "direct"},
		{"note:1", "editor", "user:mia", true, "computed: owner → editor"},
		{"note:1", "viewer", "user:mia", true, "editor → viewer"},
		{"note:1", "editor", "user:adam", true, "admin from tenant"},
		{"note:1", "editor", "user:olivia", true, "owner → admin of tenant → editor"},
		{"note:1", "viewer", "user:sam", true, "member from tenant"},
		{"note:1", "editor", "user:sam", false, "a member can't edit someone else's note"},
		{"note:1", "viewer", "user:gina", false, "other tenant"},
		{"note:2", "editor", "user:erin", true, "team userset"},
		{"note:2", "editor", "user:pete", true, "nested team userset"},
		{"note:2", "editor", "team:eng#member", true, "the userset itself"},
		{"note:2", "editor", "team:platform#member", true, "a nested userset"},
		{"note:3", "viewer", "user:anyone", true, "wildcard"},
		{"note:3", "editor", "user:anyone", false, "wildcard only on viewer"},
		{"note:1", "can_view", "user:mia", true, "viewer and not suspended"},
		{"note:1", "can_view", "user:sam", false, "but not: suspended"},
		{"note:2", "can_edit", "user:erin", false, "and: editor via team but not a tenant member"},
		{"note:1", "can_edit", "user:mia", true, "and: owner who is a member"},
		{"note:1", "can_delete", "user:adam", true, "admin from tenant"},
		{"note:1", "can_delete", "user:sam", false, "member can't delete"},
		{"note:404", "viewer", "user:mia", false, "unknown object"},
	}
	for _, tc := range cases {
		if got := check(t, c, tc.obj, tc.rel, tc.subj); got != tc.want {
			t.Errorf("%s#%s@%s = %v, want %v (%s)", tc.obj, tc.rel, tc.subj, got, tc.want, tc.why)
		}
	}
}

func TestCheckCycles(t *testing.T) {
	// groups containing each other must terminate and still find real members
	c := newChecker(t,
		"team:a#member@team:b#member",
		"team:b#member@team:a#member",
		"team:b#member@user:bob",
		"note:1#viewer@team:a#member",
	)
	if !check(t, c, "note:1", "viewer", "user:bob") {
		t.Error("bob is in b, b is in a, a views note:1")
	}
	if check(t, c, "note:1", "viewer", "user:eve") {
		t.Error("eve is in no team")
	}

	// a cut cycle must not poison later answers: ask about the cycle first
	o, _ := ParseObject("o", "team:a")
	s, _ := ParseSubject("s", "user:bob")
	st := c.newState(s)
	if ok, err := st.check(context.Background(), o, "member", 0); err != nil || !ok {
		t.Fatalf("bob in a: %v %v", ok, err)
	}
	if ok, _ := st.check(context.Background(), Object{"note", "1"}, "viewer", 0); !ok {
		t.Error("memoized cycle result denied a real path")
	}
}

func TestCheckDepthLimit(t *testing.T) {
	var tuples []string
	for i := 0; i < 30; i++ {
		tuples = append(tuples, "team:t"+itoa(i)+"#member@team:t"+itoa(i+1)+"#member")
	}
	tuples = append(tuples, "team:t30#member@user:deep", "note:1#viewer@team:t0#member")
	c := newChecker(t, tuples...)

	o, _ := ParseObject("o", "note:1")
	s, _ := ParseSubject("s", "user:deep")
	if _, err := c.Check(context.Background(), o, "viewer", s); !errors.Is(err, ErrTooComplex) {
		t.Fatalf("expected ErrTooComplex, got %v", err)
	}
	if ok, err := c.WithMaxDepth(100).Check(context.Background(), o, "viewer", s); err != nil || !ok {
		t.Fatalf("with a higher limit: %v %v", ok, err)
	}
}

func TestCheckRejectsInvalidRequests(t *testing.T) {
	c := newChecker(t)
	o, _ := ParseObject("o", "note:1")
	for _, tc := range []struct {
		obj Object
		rel string
		sub Subject
	}{
		{Object{"folder", "1"}, "viewer", Subject{Type: "user", ID: "a"}},
		{o, "reader", Subject{Type: "user", ID: "a"}},
		{o, "viewer", Subject{Type: "user", ID: "*"}},
	} {
		if _, err := c.Check(context.Background(), tc.obj, tc.rel, tc.sub); !errors.Is(err, apperr.ErrInvalid) {
			t.Errorf("%v#%s@%v: expected invalid, got %v", tc.obj, tc.rel, tc.sub, err)
		}
	}
}

func TestListObjects(t *testing.T) {
	c := newChecker(t, append(kitTuples,
		"note:4#tenant@tenant:acme",
		"note:5#tenant@tenant:globex",
	)...)
	list := func(rel, subj string) []string {
		s, _ := ParseSubject("s", subj)
		ids, err := c.ListObjects(context.Background(), "note", rel, s, 100)
		if err != nil {
			t.Fatal(err)
		}
		return ids
	}

	cases := []struct {
		rel, subj string
		want      []string
	}{
		{"viewer", "user:mia", []string{"1", "2", "3", "4"}}, // tenant member (1,2,4) + public 3
		{"editor", "user:mia", []string{"1"}},                // owner only
		{"editor", "user:pete", []string{"2"}},               // nested team
		{"editor", "user:adam", []string{"1", "2", "4"}},     // admin from tenant
		{"can_view", "user:sam", []string{"3"}},              // suspended in acme: only globex's public note 3 remains...
		{"viewer", "user:sam", []string{"1", "2", "3", "4"}}, // ...while plain viewer keeps them
		{"viewer", "user:gina", []string{"3", "5"}},          // other tenant + public
		{"viewer", "user:stranger", []string{"3"}},           // public only
	}
	for _, tc := range cases {
		got := list(tc.rel, tc.subj)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("list note#%s@%s = %v, want %v", tc.rel, tc.subj, got, tc.want)
		}
	}

	s, _ := ParseSubject("s", "user:adam")
	if ids, _ := c.ListObjects(context.Background(), "note", "editor", s, 2); len(ids) != 2 {
		t.Errorf("limit not applied: %v", ids)
	}
}

func TestValidateTuple(t *testing.T) {
	m := mustModel(t, kitModel)
	bad := map[string]string{
		"unknown type":             "folder:1#viewer@user:a",
		"unknown relation":         "note:1#reader@user:a",
		"wrong subject type":       "note:1#owner@team:eng",
		"userset not accepted":     "note:1#owner@team:eng#member",
		"wildcard not accepted":    "note:1#editor@user:*",
		"computed, not assignable": "note:1#can_view@user:a",
		"wrong userset relation":   "note:1#viewer@team:eng#tenant",
	}
	for name, s := range bad {
		if err := m.ValidateTuple(tup(t, s)); !errors.Is(err, apperr.ErrInvalid) {
			t.Errorf("%s (%s): expected invalid, got %v", name, s, err)
		}
	}
	good := []string{"note:1#viewer@user:*", "note:1#editor@team:eng#member", "team:eng#tenant@tenant:acme"}
	for _, s := range good {
		if err := m.ValidateTuple(tup(t, s)); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	noTenant := tup(t, "note:1#owner@user:a")
	noTenant.TenantID = ""
	if err := m.ValidateTuple(noTenant); !errors.Is(err, apperr.ErrInvalid) {
		t.Error("tuple without tenant accepted")
	}
}

func TestModelValidation(t *testing.T) {
	bad := map[string]string{
		"unknown computed relation": "model\n  schema 1.1\ntype user\ntype doc\n  relations\n    define viewer: editor\n",
		"unknown type in [..]":      "model\n  schema 1.1\ntype doc\n  relations\n    define viewer: [user]\n",
		"tupleset not direct": "model\n  schema 1.1\ntype user\ntype folder\n  relations\n    define viewer: [user]\n" +
			"type doc\n  relations\n    define parent: [folder]\n    define alias: parent\n    define viewer: viewer from alias\n",
		"conditions": "model\n  schema 1.1\ntype user\ntype doc\n  relations\n    define viewer: [user with ok]\n" +
			"condition ok(x: int) {\n  x < 10\n}\n",
		"not even DSL": "this is not a model",
	}
	for name, dsl := range bad {
		if _, err := ParseDSL(dsl); !errors.Is(err, apperr.ErrInvalid) {
			t.Errorf("%s: expected invalid, got %v", name, err)
		}
	}
}

func TestParseRefs(t *testing.T) {
	for _, s := range []string{"note", "note:", ":1", "Note:1", "note:a b", "note:1#2", "note:*"} {
		if _, err := ParseObject("o", s); err == nil {
			t.Errorf("object %q accepted", s)
		}
	}
	for _, s := range []string{"user:*#member", "team:eng#", "team:eng#Member"} {
		if _, err := ParseSubject("s", s); err == nil {
			t.Errorf("subject %q accepted", s)
		}
	}
	if o, err := ParseObject("o", "doc:2026:q1"); err != nil || o.ID != "2026:q1" {
		t.Errorf("id with ':' %v %v", o, err)
	}
	if s, err := ParseSubject("s", "team:eng#member"); err != nil || s.Relation != "member" {
		t.Errorf("userset %v %v", s, err)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

type countingReader struct {
	memReader
	reads  int
	onRead func(Object)
}

func (c *countingReader) seen(o Object) {
	c.reads++
	if c.onRead != nil {
		c.onRead(o)
	}
}

func (c *countingReader) HasDirect(ctx context.Context, o Object, r string, s Subject) (bool, error) {
	c.seen(o)
	return c.memReader.HasDirect(ctx, o, r, s)
}
func (c *countingReader) Usersets(ctx context.Context, o Object, r string) ([]Subject, error) {
	c.seen(o)
	return c.memReader.Usersets(ctx, o, r)
}
func (c *countingReader) DirectObjects(ctx context.Context, o Object, r string) ([]Object, error) {
	c.seen(o)
	return c.memReader.DirectObjects(ctx, o, r)
}

type mapCache map[string]bool

func (m mapCache) Get(k string) (bool, bool) { v, ok := m[k]; return v, ok }
func (m mapCache) Add(k string, v bool)      { m[k] = v }

func TestCacheSharesResultsAcrossChecks(t *testing.T) {
	m := mustModel(t, kitModel)
	r := &countingReader{}
	for _, s := range kitTuples {
		r.tuples = append(r.tuples, tup(t, s))
	}
	cache := mapCache{}
	ask := func(prefix, obj, rel, subj string) bool {
		o, _ := ParseObject("o", obj)
		s, _ := ParseSubject("s", subj)
		ok, err := NewChecker(m, r).WithCache(cache, prefix).Check(context.Background(), o, rel, s)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}

	if !ask("m1@1|", "note:1", "editor", "user:adam") {
		t.Fatal("adam edits note:1")
	}
	r.reads = 0
	if !ask("m1@1|", "note:1", "editor", "user:adam") || r.reads != 0 {
		t.Fatalf("second check read %d times", r.reads)
	}
	// sub-results are cached too, and other questions reuse them
	if v, ok := cache["m1@1|tenant:acme#admin@user:adam"]; !ok || !v {
		t.Fatal("sub-result tenant:acme#admin@user:adam not cached")
	}
	tenantReads := 0
	r.onRead = func(o Object) {
		if o.Type == "tenant" {
			tenantReads++
		}
	}
	if !ask("m1@1|", "note:1", "can_delete", "user:adam") || tenantReads != 0 {
		t.Fatalf("cached tenant sub-result was recomputed (%d tenant reads)", tenantReads)
	}
	r.onRead = nil
	// another revision never sees these entries
	r.reads = 0
	ask("m1@2|", "note:1", "editor", "user:adam")
	if r.reads == 0 {
		t.Fatal("entries of another revision were used")
	}
	// denials are cached too
	if ask("m1@1|", "note:1", "editor", "user:stranger") {
		t.Fatal("stranger edits")
	}
	if v, ok := cache["m1@1|note:1#editor@user:stranger"]; !ok || v {
		t.Fatal("denial not cached")
	}
}

func TestCacheSkipsResultsCutByCycles(t *testing.T) {
	m := mustModel(t, kitModel)
	r := &memReader{}
	for _, s := range []string{
		"team:a#member@team:b#member",
		"team:b#member@team:a#member",
		"team:b#member@user:bob",
	} {
		r.tuples = append(r.tuples, tup(t, s))
	}
	cache := mapCache{}
	o, _ := ParseObject("o", "team:a")
	s, _ := ParseSubject("s", "user:eve")
	if ok, _ := NewChecker(m, r).WithCache(cache, "p|").Check(context.Background(), o, "member", s); ok {
		t.Fatal("eve is in no team")
	}
	for k := range cache {
		if strings.HasPrefix(k, "p|team:b#member") {
			t.Fatalf("result computed through a cut cycle was cached: %s", k)
		}
	}
}
