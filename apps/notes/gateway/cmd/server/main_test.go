package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	notev1 "api/gen/go/apps/notes/note/v1"
	"api/gen/go/apps/notes/note/v1/notev1connect"
	"core/apperr"
	"core/authn"
	authnmigrations "core/authn/migrations"
	"core/authn/password"
	"core/authz/rebac"
	"core/httpx/httpxtest"
	"core/infra"
	zaplogger "core/logger/zap"
	"core/reqctx"
	"core/rpc"
	"core/testdb"
	"core/token"
	"notes-gateway/config"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fakeNotes is an in-memory note service. It records the metadata of the last call
// to check that the gateway forwards identity and request id.
type fakeNotes struct {
	notev1connect.UnimplementedNoteServiceHandler

	mu      sync.Mutex
	notes   map[uint64]*notev1.Note
	nextID  uint64
	lastID  reqctx.Identity
	lastReq string
}

func (f *fakeNotes) record(ctx context.Context) {
	f.lastID, _ = reqctx.IdentityFrom(ctx)
	f.lastReq = reqctx.RequestIDFrom(ctx)
}

func (f *fakeNotes) CreateNote(ctx context.Context, req *notev1.CreateNoteRequest) (*notev1.CreateNoteResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(ctx)
	if strings.TrimSpace(req.GetTitle()) == "" {
		return nil, apperr.Invalid("title", "is required")
	}
	if req.GetTitle() == "explode" {
		return nil, errors.New("db: connection refused")
	}
	f.nextID++
	now := timestamppb.New(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	n := &notev1.Note{Id: f.nextID, Title: req.GetTitle(), Content: req.GetContent(), CreatedBy: f.lastID.UserID, CreateTime: now, UpdateTime: now}
	f.notes[n.Id] = n
	return &notev1.CreateNoteResponse{Note: n}, nil
}

func (f *fakeNotes) GetNote(ctx context.Context, req *notev1.GetNoteRequest) (*notev1.GetNoteResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(ctx)
	n, ok := f.notes[req.GetId()]
	if !ok {
		return nil, apperr.ErrNotFound
	}
	return &notev1.GetNoteResponse{Note: n}, nil
}

func (f *fakeNotes) ListNotes(ctx context.Context, req *notev1.ListNotesRequest) (*notev1.ListNotesResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := &notev1.ListNotesResponse{Page: max(req.GetPage(), 1), Limit: 20, Total: int64(len(f.notes))}
	for _, n := range f.notes {
		listed := proto.Clone(n).(*notev1.Note)
		listed.Permissions = &notev1.NotePermissions{CanUpdate: true}
		res.Notes = append(res.Notes, listed)
	}
	return res, nil
}

func (f *fakeNotes) UpdateNote(ctx context.Context, req *notev1.UpdateNoteRequest) (*notev1.UpdateNoteResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.notes[req.GetId()]
	if !ok {
		return nil, apperr.ErrNotFound
	}
	if req.Title != nil {
		n.Title = *req.Title
	}
	if req.Content != nil {
		n.Content = *req.Content
	}
	return &notev1.UpdateNoteResponse{Note: n}, nil
}

func (f *fakeNotes) DeleteNote(ctx context.Context, req *notev1.DeleteNoteRequest) (*notev1.DeleteNoteResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.notes[req.GetId()]; !ok {
		return nil, apperr.ErrNotFound
	}
	delete(f.notes, req.GetId())
	return &notev1.DeleteNoteResponse{}, nil
}

// stack is the gateway with its real authentication (Postgres, optional Redis)
// in front of a fake note service.
type stack struct {
	gateway *gin.Engine
	admin   *gin.Engine
	notes   *fakeNotes
	auth    *authn.Module
	signer  *token.Signer
	// internal tokens of the calls to the note service, which enforces them
	internal *token.Signer
	redis    *miniredis.Miniredis // nil without Redis
}

type stackOptions struct {
	redis    bool
	devLogin bool
	// auth rate limit; zero uses the defaults
	ratePerMinute, rateBurst int
}

const adminKey = "test-admin-key"

func newStack(t *testing.T, o stackOptions) *stack {
	t.Helper()
	log := zaplogger.NewZapLogger().GetLogger("test")

	internalKey, _ := token.GenerateKey()
	internal := token.NewSigner(internalKey, "saas-iam", time.Minute)

	// the fake note service authenticates the gateway like the real one (enforce)
	notes := &fakeNotes{notes: map[uint64]*notev1.Note{}}
	nmux := http.NewServeMux()
	nmux.Handle(notev1connect.NewNoteServiceHandler(notes, rpc.HandlerOptions(log, rpc.InternalAuth{
		Mode: rpc.InternalAuthEnforce, Verifier: token.NewInternalVerifier(internal.Keys(), "saas-iam", "note"),
	})))
	notesURL := httpxtest.ServeH2C(t, nmux)

	s := &stack{notes: notes, internal: internal}
	var rdb *redis.Client
	if o.redis {
		s.redis = miniredis.RunT(t)
		rdb = redis.NewClient(&redis.Options{Addr: s.redis.Addr(), MaxRetries: -1})
		t.Cleanup(func() { rdb.Close() })
	}

	key, _ := token.GenerateKey()
	s.signer = token.NewSigner(key, "saas-iam", 10*time.Minute)
	auth, err := authn.New(authn.Config{
		App: config.App, DB: testdb.New(t, authnmigrations.FS), Signer: s.signer, Redis: rdb,
		// cheap parameters keep tests fast; production uses password.DefaultParams
		Hasher:   password.NewHasher(password.Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}, 0),
		DevLogin: o.devLogin, AdminKey: adminKey, Logger: log,
	})
	if err != nil {
		t.Fatal(err)
	}
	s.auth = auth

	c, err := buildContainer(httpxtest.GRPCClient(notesURL, notev1connect.NewNoteServiceClient, internal, "note"))
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	s.gateway, s.admin = gin.New(), gin.New()
	registerRoutes(s.gateway, c, routeOptions{
		auth: auth, devLogin: o.devLogin, logger: log,
		authRatePerMinute: o.ratePerMinute, authRateBurst: o.rateBurst,
	})
	// the note service isn't running here: forwarding fails (503)
	authzAdmin, err := rebac.NewAdminProxy(adminKey, map[string]string{"note": "http://127.0.0.1:1"}, http.DefaultTransport, log)
	if err != nil {
		t.Fatal(err)
	}
	authDB := testdb.New(t, authnmigrations.FS)
	outboxAdmin, err := newOutboxAdmin(adminKey, authDB, map[string]string{"note": "http://127.0.0.1:1"}, http.DefaultTransport, log)
	if err != nil {
		t.Fatal(err)
	}
	infraProbe := infra.NewProbe(authDB, rdb, adminKey)
	registerAdminRoutes(s.admin, auth, authzAdmin, outboxAdmin, infraProbe, internal.PublicKeys, adminKey, log)
	return s
}

func (s *stack) do(method, path, body, bearer string, headers ...string) httpxtest.Result {
	return httpxtest.Do(s.gateway, method, path, body, bearer, headers...)
}

func (s *stack) adminDo(method, path, body string) httpxtest.Result {
	return httpxtest.Do(s.admin, method, path, body, adminKey)
}

// signIn registers email and returns its user id and tenant-less access token.
func (s *stack) signIn(t *testing.T, email string) (userID, access string) {
	t.Helper()
	res := s.do("POST", "/v1/auth/register", `{"email":"`+email+`","password":"correct horse battery"}`, "")
	if res.Status != 201 {
		t.Fatalf("register %s: %d %v", email, res.Status, res.Body)
	}
	user, _ := res.Data()["user"].(map[string]any)
	tokens, _ := res.Data()["tokens"].(map[string]any)
	return user["id"].(string), tokens["access_token"].(string)
}

// owner: email registers, creates a tenant and switches into it.
func (s *stack) owner(t *testing.T, email string) (userID, tenantID, access string) {
	t.Helper()
	userID, tenantless := s.signIn(t, email)
	res := s.do("POST", "/v1/tenants", `{"name":"Acme"}`, tenantless)
	if res.Status != 201 {
		t.Fatalf("create tenant: %d %v", res.Status, res.Body)
	}
	tenantID = res.Data()["id"].(string)
	return userID, tenantID, s.switchTo(t, tenantless, tenantID)
}

func (s *stack) switchTo(t *testing.T, access, tenantID string) string {
	t.Helper()
	res := s.do("POST", "/v1/auth/switch-tenant", `{"tenant_id":"`+tenantID+`"}`, access)
	if res.Status != 200 {
		t.Fatalf("switch tenant: %d %v", res.Status, res.Body)
	}
	return res.Data()["access_token"].(string)
}

// member: email registers and is added to tenantID with role by the owner.
func (s *stack) member(t *testing.T, ownerToken, tenantID, email, role string) (userID, access string) {
	t.Helper()
	userID, tenantless := s.signIn(t, email)
	if res := s.do("POST", "/v1/members", `{"email":"`+email+`","role":"`+role+`"}`, ownerToken); res.Status != 201 {
		t.Fatalf("add member: %d %v", res.Status, res.Body)
	}
	return userID, s.switchTo(t, tenantless, tenantID)
}

func TestNotesThroughGateway(t *testing.T) {
	s := newStack(t, stackOptions{redis: true})
	_, tenantID, owner := s.owner(t, "olivia@example.com")
	userID, tok := s.member(t, owner, tenantID, "mia@example.com", "member")

	res := s.do(http.MethodPost, "/v1/notes", `{"title":"first","content":"hello"}`, tok, reqctx.HeaderRequestID, "trace-abc-123")
	if res.Status != http.StatusCreated {
		t.Fatalf("create: %d %v", res.Status, res.Body)
	}
	note := res.Data()
	if note["id"] != float64(1) || note["title"] != "first" || note["created_by"] != userID || note["created_at"] != "2026-01-02T03:04:05Z" {
		t.Fatalf("create body: %v", res.Body)
	}
	// identity (app, user, tenant, role) from the verified token and the request id reached the upstream
	if s.notes.lastID != (reqctx.Identity{AppID: config.App, UserID: userID, TenantID: tenantID, Role: "member"}) || s.notes.lastReq != "trace-abc-123" {
		t.Fatalf("upstream metadata: %+v %q", s.notes.lastID, s.notes.lastReq)
	}
	if res.Header.Get(reqctx.HeaderRequestID) != "trace-abc-123" {
		t.Fatalf("request id not echoed: %v", res.Header)
	}

	res = s.do(http.MethodGet, "/v1/notes?page=1", "", tok)
	if paging, _ := res.Body["paging"].(map[string]any); res.Status != 200 || paging["total"] != float64(1) {
		t.Fatalf("list: %d %v", res.Status, res.Body)
	}
	items, _ := res.Body["data"].([]any)
	if len(items) != 1 || !reflect.DeepEqual(items[0].(map[string]any)["permissions"], map[string]any{"can_update": true, "can_delete": false}) {
		t.Fatalf("list permissions: %v", res.Body["data"])
	}
	res = s.do(http.MethodPatch, "/v1/notes/1", `{"content":"edited"}`, tok)
	if res.Status != 200 || res.Data()["content"] != "edited" || res.Data()["title"] != "first" {
		t.Fatalf("update: %d %v", res.Status, res.Body)
	}
	if res = s.do(http.MethodDelete, "/v1/notes/1", "", tok); res.Status != http.StatusNoContent {
		t.Fatalf("delete: %d", res.Status)
	}
	if res = s.do(http.MethodGet, "/v1/notes/1", "", tok); res.Status != 404 || res.ErrCode() != "not_found" {
		t.Fatalf("get after delete: %d %v", res.Status, res.Body)
	}

}

func TestAuthTenantsAndMembers(t *testing.T) {
	s := newStack(t, stackOptions{devLogin: true})

	res := s.do("POST", "/v1/auth/dev-login", `{"user_id":"alice"}`, "")
	pair := res.Data()
	if res.Status != 200 || pair["token_type"] != "Bearer" || pair["access_token"] == "" || pair["refresh_token"] == "" {
		t.Fatalf("dev login: %d %v", res.Status, res.Body)
	}
	claims, err := s.signer.Verifier(config.App).Verify(pair["access_token"].(string))
	if err != nil || claims.UserID() != "alice" || claims.AppID() != config.App {
		t.Fatalf("token: %+v %v", claims, err)
	}

	// a tenant-less token reaches tenant-less endpoints
	access := pair["access_token"].(string)
	if res := s.do("GET", "/v1/me/tenants", "", access); res.Status != 200 {
		t.Fatalf("my tenants: %d %v", res.Status, res.Body)
	}
	if res := s.do("GET", "/v1/members", "", access); res.Status != 403 {
		t.Fatalf("members without a tenant: %d %v", res.Status, res.Body)
	}

	refreshed := s.do("POST", "/v1/auth/refresh", `{"refresh_token":"`+pair["refresh_token"].(string)+`"}`, "")
	if refreshed.Status != 200 {
		t.Fatalf("refresh: %d %v", refreshed.Status, refreshed.Body)
	}
	// single use: replaying the first refresh token revokes the family
	if res := s.do("POST", "/v1/auth/refresh", `{"refresh_token":"`+pair["refresh_token"].(string)+`"}`, ""); res.Status != 401 {
		t.Fatalf("replayed refresh: %d", res.Status)
	}
	if res := s.do("POST", "/v1/auth/refresh", `{"refresh_token":"`+refreshed.Data()["refresh_token"].(string)+`"}`, ""); res.Status != 401 {
		t.Fatalf("family not revoked: %d", res.Status)
	}

	// members: owner/admin rules come from the services
	_, tenantID, owner := s.owner(t, "olivia@example.com")
	maxID, maxTok := s.member(t, owner, tenantID, "max@example.com", "member")
	res = s.do("PATCH", "/v1/members/"+maxID, `{"role":"viewer"}`, owner)
	if res.Status != 200 || res.Data()["role"] != "viewer" || res.Data()["email"] != "max@example.com" {
		t.Fatalf("update member: %d %v", res.Status, res.Body)
	}
	// max's token still says member: stale
	if res := s.do("GET", "/v1/members", "", maxTok); res.Status != 401 || !strings.Contains(res.ErrMsg(), "refresh") {
		t.Fatalf("stale token after role change: %d %v", res.Status, res.Body)
	}
	if res := s.do("DELETE", "/v1/members/"+maxID, "", owner); res.Status != 204 {
		t.Fatalf("remove member: %d %v", res.Status, res.Body)
	}
	if res := s.do("GET", "/v1/members", "", owner); res.Status != 200 || len(res.Body["data"].([]any)) != 1 {
		t.Fatalf("members after remove: %d %v", res.Status, res.Body)
	}
}

func TestPasswordEndpoints(t *testing.T) {
	s := newStack(t, stackOptions{})
	_, access := s.signIn(t, "alice@example.com")

	res := s.do("POST", "/v1/auth/login", `{"email":"alice@example.com","password":"correct horse battery"}`, "")
	if res.Status != 200 || res.Data()["token_type"] != "Bearer" {
		t.Fatalf("login: %d %v", res.Status, res.Body)
	}
	res = s.do("POST", "/v1/auth/login", `{"email":"alice@example.com","password":"nope nope nope"}`, "")
	if res.Status != 401 || res.ErrMsg() != "unauthenticated: invalid email or password" {
		t.Fatalf("bad login: %d %v", res.Status, res.Body)
	}
	if res := s.do("GET", "/v1/me", "", access); res.Status != 200 || res.Data()["email"] != "alice@example.com" {
		t.Fatalf("me: %d %v", res.Status, res.Body)
	}
	if res := s.do("POST", "/v1/auth/change-password", `{"current_password":"correct horse battery","new_password":"a brand new passphrase"}`, access); res.Status != 204 {
		t.Fatalf("change password: %d %v", res.Status, res.Body)
	}
	if res := s.do("POST", "/v1/auth/login", `{"email":"alice@example.com","password":"a brand new passphrase"}`, ""); res.Status != 200 {
		t.Fatalf("login with new password: %d %v", res.Status, res.Body)
	}
}

func TestGatewayErrors(t *testing.T) {
	s := newStack(t, stackOptions{})
	_, _, tok := s.owner(t, "olivia@example.com")

	other, _ := token.GenerateKey()
	forged, _, _ := token.NewSigner(other, "saas-iam", time.Minute).Sign("u1", config.App, "t1", "owner", 1)
	// a valid signature, but issued for another app
	otherApp, _, _ := s.signer.Sign("u1", "crm", "", "", 0)

	cases := []struct {
		name, method, path, body, bearer string
		headers                          []string
		status                           int
		code, msg                        string
	}{
		{"no auth", "GET", "/v1/notes", "", "", nil, 401, "unauthenticated", ""},
		{"basic auth", "GET", "/v1/notes", "", "", []string{"Authorization", "Basic dTE6cHc="}, 401, "unauthenticated", ""},
		{"garbage token", "GET", "/v1/notes", "", "not.a.jwt", nil, 401, "unauthenticated", ""},
		{"forged token", "GET", "/v1/notes", "", forged, nil, 401, "unauthenticated", ""},
		{"token of another app", "GET", "/v1/me", "", otherApp, nil, 401, "unauthenticated", ""},
		{"upstream validation", "POST", "/v1/notes", `{"title":" "}`, tok, nil, 400, "invalid_input", "invalid input: title is required"},
		{"bad json", "POST", "/v1/notes", `{`, tok, nil, 400, "invalid_input", "invalid input: invalid JSON body"},
		{"no title", "POST", "/v1/notes", `{"content":"x"}`, tok, nil, 400, "invalid_input", "invalid input: title is required"},
		{"title too long", "POST", "/v1/notes", `{"title":"` + strings.Repeat("a", 256) + `"}`, tok, nil, 400, "invalid_input", "invalid input: title must be at most 255 characters"},
		{"title not a string", "POST", "/v1/notes", `{"title":1}`, tok, nil, 400, "invalid_input", "invalid input: title must be a string"},
		{"empty title update", "PATCH", "/v1/notes/1", `{"title":""}`, tok, nil, 400, "invalid_input", "invalid input: title must be at least 1 character"},
		{"limit too big", "GET", "/v1/notes?limit=500", "", tok, nil, 400, "invalid_input", "invalid input: limit must be at most 100"},
		{"limit not a number", "GET", "/v1/notes?limit=x", "", tok, nil, 400, "invalid_input", "invalid input: invalid query parameters"},
		{"bad id", "GET", "/v1/notes/abc", "", tok, nil, 400, "invalid_input", ""},
		{"missing", "PATCH", "/v1/notes/42", `{}`, tok, nil, 404, "not_found", "not found"},
		{"unknown route", "GET", "/v2/nothing", "", "", nil, 404, "not_found", ""},
		{"dev login disabled", "POST", "/v1/auth/dev-login", `{"user_id":"u1"}`, "", nil, 404, "not_found", ""},
		{"admin API not public", "GET", "/admin/v1/users", "", "", nil, 404, "not_found", ""},
		{"upstream internal", "POST", "/v1/notes", `{"title":"explode"}`, tok, nil, 500, "internal", "Internal Server Error"},
	}
	for _, tc := range cases {
		res := s.do(tc.method, tc.path, tc.body, tc.bearer, tc.headers...)
		if res.Status != tc.status || res.ErrCode() != tc.code || (tc.msg != "" && res.ErrMsg() != tc.msg) {
			t.Errorf("%s: got %d %v", tc.name, res.Status, res.Body)
		}
		if e, _ := res.Body["error"].(map[string]any); e["request_id"] == "" || e["request_id"] == nil {
			t.Errorf("%s: missing request_id in error body", tc.name)
		}
	}
	if res := s.do("GET", "/healthz", "", ""); res.Status != 200 {
		t.Fatalf("healthz: %d", res.Status)
	}
}

// Field errors from a service travel over gRPC (google.rpc.BadRequest) into "details".
func TestFieldErrorDetails(t *testing.T) {
	s := newStack(t, stackOptions{})
	_, _, tok := s.owner(t, "olivia@example.com")
	res := s.do("POST", "/v1/notes", `{"title":" "}`, tok)
	e, _ := res.Body["error"].(map[string]any)
	details, _ := e["details"].([]any)
	if res.Status != 400 || len(details) != 1 {
		t.Fatalf("got %d %v", res.Status, res.Body)
	}
	if d := details[0].(map[string]any); d["field"] != "title" || d["description"] != "is required" {
		t.Fatalf("detail %v", d)
	}
}

// Membership versions are cached in Redis; without Redis (or when it fails)
// the database answers.
func TestVersionCacheAndFallbacks(t *testing.T) {
	for _, withRedis := range []bool{true, false} {
		t.Run(fmt.Sprintf("redis=%v", withRedis), func(t *testing.T) {
			s := newStack(t, stackOptions{redis: withRedis})
			ownerID, tenantID, tok := s.owner(t, "olivia@example.com")
			for i := 0; i < 3; i++ {
				if res := s.do("GET", "/v1/notes", "", tok); res.Status != 200 {
					t.Fatalf("request %d: %d %v", i, res.Status, res.Body)
				}
			}
			if s.redis == nil {
				return
			}
			if v, _ := s.redis.Get(token.MembershipVersionKey(config.App, tenantID, ownerID)); v != "1" {
				t.Fatalf("cached %q", v)
			}
			// Redis down: the database answers
			s.redis.Close()
			if res := s.do("GET", "/v1/notes", "", tok); res.Status != 200 {
				t.Fatalf("redis down: %d %v", res.Status, res.Body)
			}
		})
	}
}

func TestAuthEndpointsAreRateLimited(t *testing.T) {
	s := newStack(t, stackOptions{ratePerMinute: 60, rateBurst: 3})
	body := `{"email":"alice@example.com","password":"guess guess guess"}`
	for i := 0; i < 3; i++ {
		if res := s.do("POST", "/v1/auth/login", body, ""); res.Status != 401 {
			t.Fatalf("attempt %d: %d", i, res.Status)
		}
	}
	res := s.do("POST", "/v1/auth/login", body, "")
	if res.Status != http.StatusTooManyRequests || res.ErrCode() != "too_many_requests" || res.Header.Get("Retry-After") == "" {
		t.Fatalf("over limit: %d %v %v", res.Status, res.Body, res.Header)
	}
}

// The admin API: on its own server, behind the app's admin key.
func TestAdminAPI(t *testing.T) {
	s := newStack(t, stackOptions{})
	ownerID, tenantID, owner := s.owner(t, "olivia@example.com")
	maxID, maxTok := s.member(t, owner, tenantID, "max@example.com", "member")

	// the key is required
	if res := httpxtest.Do(s.admin, "GET", "/admin/v1/users", "", ""); res.Status != 401 {
		t.Fatalf("no key: %d", res.Status)
	}
	if res := httpxtest.Do(s.admin, "GET", "/admin/v1/users", "", "wrong-key"); res.Status != 401 {
		t.Fatalf("wrong key: %d", res.Status)
	}

	res := s.adminDo("GET", "/admin/v1/stats", "")
	if res.Status != 200 || res.Data()["app"] != config.App || res.Data()["users"] != float64(2) || res.Data()["tenants"] != float64(1) {
		t.Fatalf("stats: %d %v", res.Status, res.Body)
	}
	res = s.adminDo("GET", "/admin/v1/users?q=max", "")
	if list, _ := res.Body["data"].([]any); res.Status != 200 || len(list) != 1 || list[0].(map[string]any)["id"] != maxID {
		t.Fatalf("search users: %d %v", res.Status, res.Body)
	}
	res = s.adminDo("GET", "/admin/v1/users/"+maxID, "")
	if ms, _ := res.Data()["memberships"].([]any); res.Status != 200 || len(ms) != 1 {
		t.Fatalf("get user: %d %v", res.Status, res.Body)
	}

	// disabling ends max's access at once and refuses sign-in
	if res := s.adminDo("PUT", "/admin/v1/users/"+maxID+"/disabled", `{"disabled":true}`); res.Status != 200 || res.Data()["disabled"] != true {
		t.Fatalf("disable: %d %v", res.Status, res.Body)
	}
	if res := s.do("GET", "/v1/notes", "", maxTok); res.Status != 401 {
		t.Fatalf("disabled user's tenant token: %d", res.Status)
	}
	if res := s.do("POST", "/v1/auth/login", `{"email":"max@example.com","password":"correct horse battery"}`, ""); res.Status != 403 {
		t.Fatalf("disabled user's login: %d %v", res.Status, res.Body)
	}
	if res := s.adminDo("PUT", "/admin/v1/users/"+maxID+"/disabled", `{"disabled":false}`); res.Status != 200 {
		t.Fatalf("enable: %d", res.Status)
	}

	// password reset by an administrator
	if res := s.adminDo("PUT", "/admin/v1/users/"+maxID+"/password", `{"password":"short"}`); res.Status != 400 {
		t.Fatalf("weak password: %d", res.Status)
	}
	if res := s.adminDo("PUT", "/admin/v1/users/"+maxID+"/password", `{"password":"reset by the admin"}`); res.Status != 204 {
		t.Fatalf("set password: %d %v", res.Status, res.Body)
	}
	if res := s.do("POST", "/v1/auth/login", `{"email":"max@example.com","password":"reset by the admin"}`, ""); res.Status != 200 {
		t.Fatalf("login after reset: %d %v", res.Status, res.Body)
	}

	// tenants and members
	res = s.adminDo("GET", "/admin/v1/tenants", "")
	if list, _ := res.Body["data"].([]any); res.Status != 200 || len(list) != 1 || list[0].(map[string]any)["members"] != float64(2) {
		t.Fatalf("tenants: %d %v", res.Status, res.Body)
	}
	if res := s.adminDo("PATCH", "/admin/v1/tenants/"+tenantID+"/members/"+ownerID, `{"role":"member"}`); res.Status != 400 {
		t.Fatalf("demoting the last owner: %d %v", res.Status, res.Body)
	}
	if res := s.adminDo("PATCH", "/admin/v1/tenants/"+tenantID+"/members/"+maxID, `{"role":"admin"}`); res.Status != 200 || res.Data()["role"] != "admin" {
		t.Fatalf("promote: %d %v", res.Status, res.Body)
	}
	if res := s.adminDo("DELETE", "/admin/v1/tenants/"+tenantID+"/members/"+maxID, ""); res.Status != 204 {
		t.Fatalf("remove member: %d %v", res.Status, res.Body)
	}

	// deleting the tenant ends the owner's access to it
	if res := s.adminDo("DELETE", "/admin/v1/tenants/"+tenantID, ""); res.Status != 204 {
		t.Fatalf("delete tenant: %d %v", res.Status, res.Body)
	}
	if res := s.do("GET", "/v1/notes", "", owner); res.Status != 401 {
		t.Fatalf("owner token after tenant deleted: %d", res.Status)
	}
	if res := s.adminDo("GET", "/admin/v1/tenants/"+tenantID, ""); res.Status != 404 {
		t.Fatalf("deleted tenant: %d", res.Status)
	}
	if res := httpxtest.Do(s.admin, "GET", "/healthz", "", ""); res.Status != 200 {
		t.Fatalf("admin healthz: %d", res.Status)
	}

	// the services' authorization engines, behind the same key
	if res := httpxtest.Do(s.admin, "GET", "/admin/v1/authz", "", ""); res.Status != 401 {
		t.Fatalf("authz without key: %d", res.Status)
	}
	if res := s.adminDo("GET", "/admin/v1/authz", ""); res.Status != 200 || fmt.Sprint(res.Body["data"]) != "[note]" {
		t.Fatalf("authz services: %d %v", res.Status, res.Body)
	}
	if res := s.adminDo("GET", "/admin/v1/authz/note/models", ""); res.Status != 503 {
		t.Fatalf("authz of an unreachable service: %d %v", res.Status, res.Body)
	}
	if res := s.adminDo("GET", "/admin/v1/authz/nope/models", ""); res.Status != 404 {
		t.Fatalf("authz of an unknown service: %d", res.Status)
	}

	if res := httpxtest.Do(s.admin, "GET", "/admin/v1/openapi", "", ""); res.Status != 401 {
		t.Fatalf("openapi without key: %d", res.Status)
	}
	res = s.adminDo("GET", "/admin/v1/openapi", "")
	if res.Status != 200 || !strings.Contains(res.Header.Get("Content-Type"), "yaml") {
		t.Fatalf("openapi spec: %d %v", res.Status, res.Header)
	}

	// the services' outbox engines
	if res := httpxtest.Do(s.admin, "GET", "/admin/v1/outbox", "", ""); res.Status != 401 {
		t.Fatalf("outbox without key: %d", res.Status)
	}
	if res := s.adminDo("GET", "/admin/v1/outbox", ""); res.Status != 200 || fmt.Sprint(res.Body["data"]) != "[gateway note]" {
		t.Fatalf("outbox services: %d %v", res.Status, res.Body)
	}
	// the gateway's own outbox (account mail) is served in-process
	if res := s.adminDo("GET", "/admin/v1/outbox/gateway/stats", ""); res.Status != 200 {
		t.Fatalf("gateway outbox: %d %v", res.Status, res.Body)
	}

	// infrastructure probe
	if res := httpxtest.Do(s.admin, "GET", "/admin/v1/infra", "", ""); res.Status != 401 {
		t.Fatalf("infra without key: %d", res.Status)
	}
	if res := s.adminDo("GET", "/admin/v1/infra", ""); res.Status != 200 || res.Body["data"] == nil {
		t.Fatalf("infra probe: %d %v", res.Status, res.Body)
	}
}

// The services verify the gateway's internal tokens with the keys it serves on
// its internal admin server, without the admin key.
func TestInternalJWKS(t *testing.T) {
	s := newStack(t, stackOptions{})
	res := httpxtest.Do(s.admin, "GET", config.InternalJWKSPath, "", "")
	if res.Status != 200 {
		t.Fatalf("%d %v", res.Status, res.Body)
	}
	keys, _ := res.Body["keys"].([]any)
	if len(keys) != 1 {
		t.Fatalf("keys %v", res.Body)
	}
	kid, _ := keys[0].(map[string]any)["kid"].(string)
	if kid != s.internal.KeyRing().ActiveKid() || kid == s.signer.KeyRing().ActiveKid() {
		t.Fatalf("expected the internal key, not the access tokens' one: %s", kid)
	}
}
