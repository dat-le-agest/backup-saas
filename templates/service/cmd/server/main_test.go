package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	notev1 "api/gen/go/apps/notes/note/v1"
	"api/gen/go/apps/notes/note/v1/notev1connect"
	authzv1 "api/gen/go/platform/authz/v1"
	"api/gen/go/platform/authz/v1/authzv1connect"
	"core/apperr"
	"core/di"
	zaplogger "core/logger/zap"
	"core/outbox"
	"core/reqctx"
	"core/rpc"
	"core/testdb"
	"core/uow"
	"sdk/authz"
	"sdk/components/authzc"
	grpctransport "template/internal/transport/rpc"
	"template/migrations"

	"connectrpc.com/connect"
	"github.com/gin-gonic/gin"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

// newTestServer wires the real container on Postgres and serves it with h2c,
// the same way main does through ginc.
func newTestServer(t *testing.T) string {
	t.Helper()
	url, _ := newTestServerWithDB(t)
	return url
}

func newTestServerWithDB(t *testing.T) (string, *gorm.DB) {
	t.Helper()
	url, db, _ := newTestServerWith(t, authzSetup{enforce: authzc.EnforceRBAC})
	return url, db
}

func newTestServerWith(t *testing.T, setup authzSetup) (string, *gorm.DB, *di.Container) {
	t.Helper()

	// real Postgres, migrations applied, non-superuser owner: RLS is enforced
	db := testdb.New(t, migrations.FS)

	c, err := buildContainer(db, zaplogger.NewZapLogger().GetLogger("test"), setup)
	if err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	di.MustGet[*grpctransport.NoteHandler](c).Mount(r)

	srv := httptest.NewUnstartedServer(r)
	srv.Config.Protocols = new(http.Protocols)
	srv.Config.Protocols.SetHTTP1(true)
	srv.Config.Protocols.SetUnencryptedHTTP2(true)
	srv.Start()
	t.Cleanup(srv.Close)
	return srv.URL, db, c
}

// newClient behaves like the gateway's client: gRPC over h2c + core/rpc interceptors.
func newClient(url string) notev1connect.NoteServiceClient {
	p := new(http.Protocols)
	p.SetUnencryptedHTTP2(true)
	return notev1connect.NewNoteServiceClient(
		&http.Client{Transport: &http.Transport{Protocols: p}},
		url,
		connect.WithGRPC(),
		connect.WithInterceptors(rpc.ClientMetadataInterceptor(), rpc.ClientErrorInterceptor()),
	)
}

// as returns a context whose identity the client forwards like the gateway does.
func as(userID, tenantID, role string) context.Context {
	return reqctx.WithIdentity(context.Background(), reqctx.Identity{UserID: userID, TenantID: tenantID, Role: role})
}

func TestNoteCRUD(t *testing.T) {
	c := newClient(newTestServer(t))
	ctx := as("u1", "t1", authz.RoleAdmin)

	created, err := c.CreateNote(ctx, &notev1.CreateNoteRequest{Title: "  first ", Content: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	n := created.GetNote()
	if n.GetId() == 0 || n.GetTitle() != "first" || n.GetCreatedBy() != "u1" || n.GetCreateTime().AsTime().IsZero() {
		t.Fatalf("create: %v", n)
	}

	if _, err := c.CreateNote(ctx, &notev1.CreateNoteRequest{Title: "second"}); err != nil {
		t.Fatal(err)
	}

	list, err := c.ListNotes(ctx, &notev1.ListNotesRequest{Page: 1, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.GetNotes()) != 1 || list.GetTotal() != 2 || list.GetNotes()[0].GetTitle() != "second" {
		t.Fatalf("list: %v", list)
	}
	if p := list.GetNotes()[0].GetPermissions(); !p.GetCanUpdate() || !p.GetCanDelete() {
		t.Fatalf("admin permissions: %v", p)
	}
	viewer, _ := c.ListNotes(as("u2", "t1", authz.RoleViewer), &notev1.ListNotesRequest{Limit: 1})
	if p := viewer.GetNotes()[0].GetPermissions(); p == nil || p.GetCanUpdate() || p.GetCanDelete() {
		t.Fatalf("viewer permissions: %v", p)
	}

	got, err := c.GetNote(as("u2", "t1", authz.RoleMember), &notev1.GetNoteRequest{Id: n.GetId()})
	if err != nil {
		t.Fatal(err)
	}
	if p := got.GetNote().GetPermissions(); p == nil || p.GetCanUpdate() || p.GetCanDelete() {
		t.Fatalf("member on another's note: %v", p)
	}

	defaults, _ := c.ListNotes(ctx, &notev1.ListNotesRequest{})
	if defaults.GetPage() != 1 || defaults.GetLimit() != 20 {
		t.Fatalf("default paging: %v", defaults)
	}

	// only content is sent: title must stay unchanged
	updated, err := c.UpdateNote(ctx, &notev1.UpdateNoteRequest{Id: n.GetId(), Content: proto.String("updated")})
	if err != nil {
		t.Fatal(err)
	}
	if updated.GetNote().GetContent() != "updated" || updated.GetNote().GetTitle() != "first" {
		t.Fatalf("update: %v", updated.GetNote())
	}

	if _, err := c.DeleteNote(ctx, &notev1.DeleteNoteRequest{Id: n.GetId()}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetNote(ctx, &notev1.GetNoteRequest{Id: n.GetId()}); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("get after delete: %v", err)
	}
}

func TestNoteErrors(t *testing.T) {
	c := newClient(newTestServer(t))
	ctx := as("u1", "t1", authz.RoleAdmin)

	existing, err := c.CreateNote(ctx, &notev1.CreateNoteRequest{Title: "existing"})
	if err != nil {
		t.Fatal(err)
	}
	existingID := existing.GetNote().GetId()

	cases := []struct {
		name     string
		call     func() error
		sentinel error
		code     connect.Code
	}{
		{"empty title", func() error {
			_, err := c.CreateNote(ctx, &notev1.CreateNoteRequest{Title: " "})
			return err
		}, apperr.ErrInvalid, connect.CodeInvalidArgument},
		{"blank title on update", func() error {
			_, err := c.UpdateNote(ctx, &notev1.UpdateNoteRequest{Id: existingID, Title: proto.String("")})
			return err
		}, apperr.ErrInvalid, connect.CodeInvalidArgument},
		{"id 0", func() error {
			_, err := c.GetNote(ctx, &notev1.GetNoteRequest{Id: 0})
			return err
		}, apperr.ErrInvalid, connect.CodeInvalidArgument},
		{"get missing", func() error {
			_, err := c.GetNote(ctx, &notev1.GetNoteRequest{Id: 999})
			return err
		}, apperr.ErrNotFound, connect.CodeNotFound},
		{"update missing", func() error {
			_, err := c.UpdateNote(ctx, &notev1.UpdateNoteRequest{Id: 999})
			return err
		}, apperr.ErrNotFound, connect.CodeNotFound},
		{"delete missing", func() error {
			_, err := c.DeleteNote(ctx, &notev1.DeleteNoteRequest{Id: 999})
			return err
		}, apperr.ErrNotFound, connect.CodeNotFound},
	}
	for _, tc := range cases {
		err := tc.call()
		if !errors.Is(err, tc.sentinel) || connect.CodeOf(err) != tc.code {
			t.Errorf("%s: got %v (code %v)", tc.name, err, connect.CodeOf(err))
		}
	}

	_, err = c.CreateNote(ctx, &notev1.CreateNoteRequest{Title: ""})
	if err.Error() != "invalid input: title is required" {
		t.Fatalf("message not preserved: %q", err.Error())
	}
}

// The same handler also answers Connect/JSON over HTTP/1.1, handy for curl while debugging.
func TestConnectJSON(t *testing.T) {
	url := newTestServer(t)

	post := func(procedure, body string) (*http.Response, error) {
		req, _ := http.NewRequest(http.MethodPost, url+procedure, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(reqctx.HeaderUserID, "u1")
		req.Header.Set(reqctx.HeaderTenantID, "t1")
		req.Header.Set(reqctx.HeaderTenantRole, authz.RoleMember)
		return http.DefaultClient.Do(req)
	}

	res, err := post(notev1connect.NoteServiceCreateNoteProcedure, `{"title":"from curl"}`)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), `"title":"from curl"`) {
		t.Fatalf("%d %s", res.StatusCode, body)
	}

	res, err = post(notev1connect.NoteServiceGetNoteProcedure, `{"id":"999"}`)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("not found over Connect/JSON: %d", res.StatusCode)
	}
}

// Tenant B must not be able to see or touch tenant A's notes, whatever its role.
// Other tenants' notes must look missing (NotFound), not forbidden.
func TestTenantIsolation(t *testing.T) {
	c := newClient(newTestServer(t))
	alice := as("alice", "tenant-a", authz.RoleOwner)
	bob := as("bob", "tenant-b", authz.RoleOwner)

	created, err := c.CreateNote(alice, &notev1.CreateNoteRequest{Title: "secret plan"})
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetNote().GetId()

	if _, err := c.GetNote(bob, &notev1.GetNoteRequest{Id: id}); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("get: %v", err)
	}
	if _, err := c.UpdateNote(bob, &notev1.UpdateNoteRequest{Id: id, Title: proto.String("pwned")}); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("update: %v", err)
	}
	if _, err := c.DeleteNote(bob, &notev1.DeleteNoteRequest{Id: id}); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("delete: %v", err)
	}

	list, err := c.ListNotes(bob, &notev1.ListNotesRequest{})
	if err != nil || len(list.GetNotes()) != 0 || list.GetTotal() != 0 {
		t.Errorf("list: %v %v", list, err)
	}

	// bob's own note doesn't show up for alice either
	if _, err := c.CreateNote(bob, &notev1.CreateNoteRequest{Title: "bob's"}); err != nil {
		t.Fatal(err)
	}
	list, _ = c.ListNotes(alice, &notev1.ListNotesRequest{})
	if list.GetTotal() != 1 || list.GetNotes()[0].GetTitle() != "secret plan" {
		t.Errorf("alice list: %v", list)
	}

	// and alice's note is untouched
	got, err := c.GetNote(alice, &notev1.GetNoteRequest{Id: id})
	if err != nil || got.GetNote().GetTitle() != "secret plan" {
		t.Fatalf("alice get: %v %v", got, err)
	}
}

func TestRolesOverGRPC(t *testing.T) {
	c := newClient(newTestServer(t))
	member := as("m1", "t1", authz.RoleMember)
	other := as("m2", "t1", authz.RoleMember)
	viewer := as("v1", "t1", authz.RoleViewer)

	created, err := c.CreateNote(member, &notev1.CreateNoteRequest{Title: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetNote().GetId()

	// another member can read but not modify it
	if _, err := c.GetNote(other, &notev1.GetNoteRequest{Id: id}); err != nil {
		t.Errorf("member read: %v", err)
	}
	_, err = c.UpdateNote(other, &notev1.UpdateNoteRequest{Id: id, Content: proto.String("x")})
	if !errors.Is(err, apperr.ErrForbidden) || connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("member update others: %v", err)
	}
	if _, err := c.DeleteNote(other, &notev1.DeleteNoteRequest{Id: id}); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("member delete others: %v", err)
	}

	if _, err := c.CreateNote(viewer, &notev1.CreateNoteRequest{Title: "nope"}); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("viewer create: %v", err)
	}

	// the creator still can
	if _, err := c.UpdateNote(member, &notev1.UpdateNoteRequest{Id: id, Content: proto.String("edited")}); err != nil {
		t.Errorf("member update own: %v", err)
	}

	// no identity at all
	_, err = c.ListNotes(context.Background(), &notev1.ListNotesRequest{})
	if !errors.Is(err, apperr.ErrUnauthenticated) || connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("anonymous: %v", err)
	}
}

// Postgres row-level security backs up the repository: even a query that forgets
// tenancy.Scope (a bug) only sees the caller's tenant.
func TestRowLevelSecurityBacksUpRepository(t *testing.T) {
	url, db := newTestServerWithDB(t)
	c := newClient(url)
	alice := as("alice", "tenant-a", authz.RoleOwner)
	bob := as("bob", "tenant-b", authz.RoleOwner)
	for _, ctx := range []context.Context{alice, alice, bob} {
		if _, err := c.CreateNote(ctx, &notev1.CreateNoteRequest{Title: "x"}); err != nil {
			t.Fatal(err)
		}
	}

	countUnscoped := func(ctx context.Context) int64 {
		var n int64
		if err := uow.New(db).Tx(ctx, func(tx *gorm.DB) error {
			return tx.Raw("SELECT count(*) FROM notes").Scan(&n).Error // no tenant filter
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := countUnscoped(alice); n != 2 {
		t.Errorf("tenant-a sees %d notes, want 2", n)
	}
	if n := countUnscoped(bob); n != 1 {
		t.Errorf("tenant-b sees %d notes, want 1", n)
	}

	// without the tenant setting (e.g. a query outside the unit of work), nothing is visible
	var n int64
	db.Raw("SELECT count(*) FROM notes").Scan(&n)
	if n != 0 {
		t.Errorf("query outside the unit of work sees %d notes", n)
	}
}

// fakeAuthz records the tuples the outbox relay delivers.
type fakeAuthz struct {
	authzv1connect.AuthzServiceClient
	mu             sync.Mutex
	writes         []*authzv1.Tuple
	deletes        []*authzv1.Tuple
	deletedObjects []string
}

func (f *fakeAuthz) WriteTuples(_ context.Context, req *authzv1.WriteTuplesRequest) (*authzv1.WriteTuplesResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, req.GetWrites()...)
	f.deletes = append(f.deletes, req.GetDeletes()...)
	f.deletedObjects = append(f.deletedObjects, req.GetDeleteObjects()...)
	return &authzv1.WriteTuplesResponse{}, nil
}

// Creating and deleting a note records its relationships in the outbox, in the
// same transaction, and the relay delivers them to the authz service.
func TestRelationshipsSyncThroughOutbox(t *testing.T) {
	fake := &fakeAuthz{}
	setup := authzSetup{client: fake, enforce: authzc.EnforceRBAC}
	url, db, c := newTestServerWith(t, setup)
	client := newClient(url)
	ctx := as("u1", "t1", authz.RoleMember)

	created, err := client.CreateNote(ctx, &notev1.CreateNoteRequest{Title: "synced"})
	if err != nil {
		t.Fatal(err)
	}
	// a failed create leaves no message behind
	if _, err := client.CreateNote(ctx, &notev1.CreateNoteRequest{Title: " "}); err == nil {
		t.Fatal("blank title accepted")
	}
	var pending int64
	db.Raw(`SELECT count(*) FROM outbox_messages WHERE processed_at IS NULL`).Scan(&pending)
	if pending != 1 {
		t.Fatalf("pending outbox messages: %d", pending)
	}

	relay := di.MustGet[*outbox.Outbox](c).NewRelay(db, zaplogger.NewZapLogger().GetLogger("test"), outbox.RelayOptions{})
	relay.Handle(authz.TuplesTopic, authz.DeliverTuples(setup.writeTuples))
	if n, err := relay.RunOnce(context.Background()); err != nil || n != 1 {
		t.Fatalf("relay: %d %v", n, err)
	}

	object := fmt.Sprintf("note:%d", created.GetNote().GetId())
	got := map[string]bool{}
	for _, w := range fake.writes {
		got[w.GetObject()+"#"+w.GetRelation()+"@"+w.GetSubject()+" ("+w.GetTenantId()+")"] = true
	}
	for _, want := range []string{object + "#tenant@tenant:t1 (t1)", object + "#owner@user:u1 (t1)"} {
		if !got[want] {
			t.Errorf("missing %s in %v", want, got)
		}
	}

	if _, err := client.DeleteNote(ctx, &notev1.DeleteNoteRequest{Id: created.GetNote().GetId()}); err != nil {
		t.Fatal(err)
	}
	if n, _ := relay.RunOnce(context.Background()); n != 1 || len(fake.deletedObjects) != 1 || fake.deletedObjects[0] != object {
		t.Fatalf("delete not synced: %d %v", n, fake.deletedObjects)
	}
}

// fakeBatchAuthz answers BatchCheck: allows note:1, reports an error for "note:bad".
type fakeBatchAuthz struct {
	authzv1connect.AuthzServiceClient
	got *authzv1.BatchCheckRequest
}

func (f *fakeBatchAuthz) BatchCheck(_ context.Context, req *authzv1.BatchCheckRequest) (*authzv1.BatchCheckResponse, error) {
	f.got = req
	res := &authzv1.BatchCheckResponse{}
	for _, c := range req.GetChecks() {
		r := &authzv1.BatchCheckResult{Allowed: c.GetObject() == "note:1"}
		if c.GetObject() == "note:bad" {
			r = &authzv1.BatchCheckResult{Error: &authzv1.CheckError{Code: "invalid_input", Message: "bad object"}}
		}
		res.Results = append(res.Results, r)
	}
	return res, nil
}

// With ReBAC enforced, authz.CheckMany is one BatchCheck call.
func TestCheckManyUsesBatchCheck(t *testing.T) {
	fake := &fakeBatchAuthz{}
	az := authzSetup{client: fake, enforce: authzc.EnforceReBAC}.authorizer()
	res := func(id string) authz.Resource { return authz.Resource{Type: "note", ID: id, TenantID: "t1"} }

	errs := authz.CheckMany(as("u1", "t1", ""), az, []authz.Request{
		{Action: authz.Read, Resource: res("1")},
		{Action: authz.Read, Resource: res("2")},
		{Action: authz.Read, Resource: res("bad")},
	})
	if errs[0] != nil || !errors.Is(errs[1], apperr.ErrForbidden) || !errors.Is(errs[2], apperr.ErrInvalid) {
		t.Fatalf("got %v", errs)
	}
	if len(fake.got.GetChecks()) != 3 || fake.got.GetChecks()[1].GetRelation() != "can_read" ||
		fake.got.GetConsistency() != authzv1.Consistency_CONSISTENCY_MINIMIZE_LATENCY {
		t.Errorf("request: %v", fake.got)
	}
}
