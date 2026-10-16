package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	iamv1 "api/gen/go/iam/v1"
	"api/gen/go/iam/v1/iamv1connect"
	notev1 "api/gen/go/note/v1"
	"api/gen/go/note/v1/notev1connect"
	"core/apperr"
	zaplogger "core/logger/zap"
	"core/reqctx"
	"core/rpc"
	"core/token"
	"gateway/internal/authn"

	"connectrpc.com/connect"
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

// fakeIam signs real tokens, serves its JWKS and keeps membership versions in memory.
type fakeIam struct {
	iamv1connect.UnimplementedIamServiceHandler

	signer *token.Signer

	mu           sync.Mutex
	versions     map[string]int64 // tenant/user → version
	versionCalls atomic.Int32
	lastID       reqctx.Identity
	lastUpdate   *iamv1.UpdateMemberRoleRequest
}

func (f *fakeIam) setVersion(tenantID, userID string, v int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.versions[tenantID+"/"+userID] = v
}

func (f *fakeIam) GetMembershipVersion(_ context.Context, req *iamv1.GetMembershipVersionRequest) (*iamv1.GetMembershipVersionResponse, error) {
	f.versionCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.versions[req.GetTenantId()+"/"+req.GetUserId()]
	if !ok {
		return nil, apperr.ErrNotFound
	}
	return &iamv1.GetMembershipVersionResponse{Version: v}, nil
}

func (f *fakeIam) pair(userID, tenantID, role string, v int64) *iamv1.TokenPair {
	access, exp, _ := f.signer.Sign(userID, tenantID, role, v)
	return &iamv1.TokenPair{
		AccessToken: access, AccessTokenExpireTime: timestamppb.New(exp),
		RefreshToken: "refresh-" + userID, RefreshTokenExpireTime: timestamppb.New(exp.Add(time.Hour)),
	}
}

func (f *fakeIam) Register(_ context.Context, req *iamv1.RegisterRequest) (*iamv1.RegisterResponse, error) {
	return &iamv1.RegisterResponse{
		User:   &iamv1.User{Id: "usr_1", Email: req.GetEmail(), CreateTime: timestamppb.Now()},
		Tokens: f.pair("usr_1", "", "", 0),
	}, nil
}

func (f *fakeIam) Login(_ context.Context, req *iamv1.LoginRequest) (*iamv1.LoginResponse, error) {
	if req.GetPassword() != "correct horse battery" {
		return nil, fmt.Errorf("%w: invalid email or password", apperr.ErrUnauthenticated)
	}
	return &iamv1.LoginResponse{Tokens: f.pair("usr_1", "", "", 0)}, nil
}

func (f *fakeIam) GetMe(ctx context.Context, _ *iamv1.GetMeRequest) (*iamv1.GetMeResponse, error) {
	id, _ := reqctx.IdentityFrom(ctx)
	return &iamv1.GetMeResponse{User: &iamv1.User{Id: id.UserID, Email: "alice@example.com", CreateTime: timestamppb.Now()}}, nil
}

func (f *fakeIam) DevLogin(_ context.Context, req *iamv1.DevLoginRequest) (*iamv1.DevLoginResponse, error) {
	return &iamv1.DevLoginResponse{Tokens: f.pair(req.GetUserId(), "", "", 0)}, nil
}

func (f *fakeIam) RefreshTokens(_ context.Context, req *iamv1.RefreshTokensRequest) (*iamv1.RefreshTokensResponse, error) {
	if !strings.HasPrefix(req.GetRefreshToken(), "refresh-") {
		return nil, fmt.Errorf("%w: invalid or expired refresh token", apperr.ErrUnauthenticated)
	}
	return &iamv1.RefreshTokensResponse{Tokens: f.pair(strings.TrimPrefix(req.GetRefreshToken(), "refresh-"), "", "", 0)}, nil
}

func (f *fakeIam) RevokeRefreshToken(context.Context, *iamv1.RevokeRefreshTokenRequest) (*iamv1.RevokeRefreshTokenResponse, error) {
	return &iamv1.RevokeRefreshTokenResponse{}, nil
}

func (f *fakeIam) ListMyTenants(ctx context.Context, _ *iamv1.ListMyTenantsRequest) (*iamv1.ListMyTenantsResponse, error) {
	f.mu.Lock()
	f.lastID, _ = reqctx.IdentityFrom(ctx)
	f.mu.Unlock()
	return &iamv1.ListMyTenantsResponse{Tenants: []*iamv1.MyTenant{{
		Tenant: &iamv1.Tenant{Id: "t1", Name: "Acme", CreateTime: timestamppb.Now()}, Role: "owner",
	}}}, nil
}

func (f *fakeIam) UpdateMemberRole(ctx context.Context, req *iamv1.UpdateMemberRoleRequest) (*iamv1.UpdateMemberRoleResponse, error) {
	f.mu.Lock()
	f.lastID, _ = reqctx.IdentityFrom(ctx)
	f.lastUpdate = req
	f.mu.Unlock()
	if req.GetRole() == "owner" {
		return nil, fmt.Errorf("%w: only owners can grant the owner role", apperr.ErrForbidden)
	}
	return &iamv1.UpdateMemberRoleResponse{Member: &iamv1.Member{UserId: req.GetUserId(), Role: req.GetRole(), CreateTime: timestamppb.Now(), UpdateTime: timestamppb.Now()}}, nil
}

// grpcClient mirrors what rpcc builds: gRPC over h2c + core/rpc client interceptors.
func grpcClient[T any](url string, ctor func(connect.HTTPClient, string, ...connect.ClientOption) T) T {
	p := new(http.Protocols)
	p.SetUnencryptedHTTP2(true)
	return ctor(
		&http.Client{Transport: &http.Transport{Protocols: p}},
		url,
		connect.WithGRPC(),
		connect.WithInterceptors(
			rpc.ClientMetadataInterceptor(),
			rpc.ClientTimeoutInterceptor(2*time.Second),
			rpc.ClientErrorInterceptor(),
		),
	)
}

func serveH2C(t *testing.T, h http.Handler) string {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	srv.Config.Protocols = new(http.Protocols)
	srv.Config.Protocols.SetHTTP1(true)
	srv.Config.Protocols.SetUnencryptedHTTP2(true)
	srv.Start()
	t.Cleanup(srv.Close)
	return srv.URL
}

type stack struct {
	gateway *gin.Engine
	notes   *fakeNotes
	iam     *fakeIam
	redis   *miniredis.Miniredis // nil when the gateway runs without Redis
}

type stackOptions struct {
	redis    bool
	devLogin bool
	iamDown  bool
	// auth rate limit; zero uses the defaults
	ratePerMinute, rateBurst int
}

func newStack(t *testing.T, o stackOptions) *stack {
	t.Helper()
	log := zaplogger.NewZapLogger().GetLogger("test")

	notes := &fakeNotes{notes: map[uint64]*notev1.Note{}}
	nmux := http.NewServeMux()
	nmux.Handle(notev1connect.NewNoteServiceHandler(notes, rpc.HandlerOptions(log)))
	notesURL := serveH2C(t, nmux)

	key, _ := token.GenerateKey()
	iam := &fakeIam{signer: token.NewSigner(key, "iam", "api", 10*time.Minute), versions: map[string]int64{}}
	imux := http.NewServeMux()
	imux.Handle(iamv1connect.NewIamServiceHandler(iam, rpc.HandlerOptions(log)))
	jwks := iam.signer.PublicKeys()
	imux.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jwks)
	})
	iamURL := serveH2C(t, imux)
	jwksURL := iamURL + "/.well-known/jwks.json"
	if o.iamDown {
		// JWKS stays reachable, but gRPC calls go to an address nobody listens on
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		iamURL = "http://" + ln.Addr().String()
		ln.Close()
	}

	s := &stack{notes: notes, iam: iam}
	var rdb *redis.Client
	if o.redis {
		s.redis = miniredis.RunT(t)
		rdb = redis.NewClient(&redis.Options{Addr: s.redis.Addr(), MaxRetries: -1})
		t.Cleanup(func() { rdb.Close() })
	}

	iamClient := grpcClient(iamURL, iamv1connect.NewIamServiceClient)
	jwksSource := token.NewRemoteJWKS(jwksURL, nil)
	if err := jwksSource.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	auth := authn.NewJWTAuthenticator(
		token.NewVerifier(jwksSource, "iam", "api"),
		authn.NewVersionLookup(rdb, iamClient, time.Hour, log),
	)

	c, err := buildContainer(grpcClient(notesURL, notev1connect.NewNoteServiceClient), iamClient)
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	s.gateway = gin.New()
	registerRoutes(s.gateway, c, routeOptions{
		auth: auth, devLogin: o.devLogin, logger: log,
		authRatePerMinute: o.ratePerMinute, authRateBurst: o.rateBurst,
	})
	return s
}

// tokenFor issues a tenant-scoped access token and registers the membership.
func (s *stack) tokenFor(userID, tenantID, role string, version int64) string {
	s.iam.setVersion(tenantID, userID, version)
	access, _, _ := s.iam.signer.Sign(userID, tenantID, role, version)
	return access
}

type result struct {
	status int
	header http.Header
	body   map[string]any
}

func (s *stack) do(method, path, body, bearer string, headers ...string) result {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	s.gateway.ServeHTTP(w, req)

	var parsed map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &parsed)
	return result{status: w.Code, header: w.Header(), body: parsed}
}

func data(r result) map[string]any { m, _ := r.body["data"].(map[string]any); return m }
func errCode(r result) string {
	e, _ := r.body["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}
func errMsg(r result) string {
	e, _ := r.body["error"].(map[string]any)
	s, _ := e["message"].(string)
	return s
}

func TestNotesThroughGateway(t *testing.T) {
	s := newStack(t, stackOptions{redis: true})
	tok := s.tokenFor("u1", "t1", "member", 1)

	res := s.do(http.MethodPost, "/v1/notes", `{"title":"first","content":"hello"}`, tok, reqctx.HeaderRequestID, "trace-abc-123")
	if res.status != http.StatusCreated {
		t.Fatalf("create: %d %v", res.status, res.body)
	}
	note := data(res)
	if note["id"] != float64(1) || note["title"] != "first" || note["created_by"] != "u1" || note["created_at"] != "2026-01-02T03:04:05Z" {
		t.Fatalf("create body: %v", res.body)
	}
	// identity from the verified token and the request id reached the upstream
	if s.notes.lastID != (reqctx.Identity{UserID: "u1", TenantID: "t1", Role: "member"}) || s.notes.lastReq != "trace-abc-123" {
		t.Fatalf("upstream metadata: %+v %q", s.notes.lastID, s.notes.lastReq)
	}
	if res.header.Get(reqctx.HeaderRequestID) != "trace-abc-123" {
		t.Fatalf("request id not echoed: %v", res.header)
	}

	res = s.do(http.MethodGet, "/v1/notes?page=1", "", tok)
	if paging, _ := res.body["paging"].(map[string]any); res.status != 200 || paging["total"] != float64(1) {
		t.Fatalf("list: %d %v", res.status, res.body)
	}
	items, _ := res.body["data"].([]any)
	if len(items) != 1 || !reflect.DeepEqual(items[0].(map[string]any)["permissions"], map[string]any{"can_update": true, "can_delete": false}) {
		t.Fatalf("list permissions: %v", res.body["data"])
	}
	res = s.do(http.MethodPatch, "/v1/notes/1", `{"content":"edited"}`, tok)
	if res.status != 200 || data(res)["content"] != "edited" || data(res)["title"] != "first" {
		t.Fatalf("update: %d %v", res.status, res.body)
	}
	if res = s.do(http.MethodDelete, "/v1/notes/1", "", tok); res.status != http.StatusNoContent {
		t.Fatalf("delete: %d", res.status)
	}
	if res = s.do(http.MethodGet, "/v1/notes/1", "", tok); res.status != 404 || errCode(res) != "not_found" {
		t.Fatalf("get after delete: %d %v", res.status, res.body)
	}
}

func TestGatewayErrors(t *testing.T) {
	s := newStack(t, stackOptions{})
	tok := s.tokenFor("u1", "t1", "member", 1)

	other, _ := token.GenerateKey()
	forged, _, _ := token.NewSigner(other, "iam", "api", time.Minute).Sign("u1", "t1", "owner", 1)

	cases := []struct {
		name, method, path, body, bearer string
		headers                          []string
		status                           int
		code, msg                        string
	}{
		{"no auth", "GET", "/v1/notes", "", "", nil, 401, "unauthenticated", ""},
		{"basic auth", "GET", "/v1/notes", "", "", []string{"Authorization", "Basic dTE6cHc="}, 401, "unauthenticated", ""},
		{"old dev header", "GET", "/v1/notes", "", "", []string{"Authorization", "Dev u1:t1:owner"}, 401, "unauthenticated", ""},
		{"garbage token", "GET", "/v1/notes", "", "not.a.jwt", nil, 401, "unauthenticated", ""},
		{"forged token", "GET", "/v1/notes", "", forged, nil, 401, "unauthenticated", ""},
		{"upstream validation", "POST", "/v1/notes", `{"title":" "}`, tok, nil, 400, "invalid_input", "invalid input: title is required"},
		{"bad json", "POST", "/v1/notes", `{`, tok, nil, 400, "invalid_input", "invalid input: invalid JSON body"},
		{"bad id", "GET", "/v1/notes/abc", "", tok, nil, 400, "invalid_input", ""},
		{"missing", "PATCH", "/v1/notes/42", `{}`, tok, nil, 404, "not_found", "not found"},
		{"unknown route", "GET", "/v2/nothing", "", "", nil, 404, "not_found", ""},
		{"dev login disabled", "POST", "/v1/auth/dev-login", `{"user_id":"u1"}`, "", nil, 404, "not_found", ""},
		{"upstream internal", "POST", "/v1/notes", `{"title":"explode"}`, tok, nil, 500, "internal", "Internal Server Error"},
	}
	for _, tc := range cases {
		res := s.do(tc.method, tc.path, tc.body, tc.bearer, tc.headers...)
		if res.status != tc.status || errCode(res) != tc.code || (tc.msg != "" && errMsg(res) != tc.msg) {
			t.Errorf("%s: got %d %v", tc.name, res.status, res.body)
		}
		if e, _ := res.body["error"].(map[string]any); e["request_id"] == "" || e["request_id"] == nil {
			t.Errorf("%s: missing request_id in error body", tc.name)
		}
	}

	if res := s.do("GET", "/healthz", "", ""); res.status != 200 {
		t.Fatalf("healthz: %d", res.status)
	}
}

// A role change or removal bumps the membership version: tokens issued before
// are rejected right away, not when they expire.
// Field errors from a service travel over gRPC (google.rpc.BadRequest) into "details".
func TestFieldErrorDetails(t *testing.T) {
	s := newStack(t, stackOptions{})
	res := s.do("POST", "/v1/notes", `{"title":" "}`, s.tokenFor("u1", "t1", "member", 1))
	e, _ := res.body["error"].(map[string]any)
	details, _ := e["details"].([]any)
	if res.status != 400 || len(details) != 1 {
		t.Fatalf("got %d %v", res.status, res.body)
	}
	if d := details[0].(map[string]any); d["field"] != "title" || d["description"] != "is required" {
		t.Fatalf("detail %v", d)
	}

	res = s.do("GET", "/v1/notes/abc", "", s.tokenFor("u1", "t1", "member", 1))
	e, _ = res.body["error"].(map[string]any)
	if details, _ := e["details"].([]any); len(details) != 1 || details[0].(map[string]any)["field"] != "id" {
		t.Fatalf("gateway-side field error: %v", res.body)
	}
}

func TestStaleTokensRejected(t *testing.T) {
	for _, withRedis := range []bool{true, false} {
		t.Run(fmt.Sprintf("redis=%v", withRedis), func(t *testing.T) {
			s := newStack(t, stackOptions{redis: withRedis})
			tok := s.tokenFor("u1", "t1", "admin", 1)

			if res := s.do("GET", "/v1/notes", "", tok); res.status != 200 {
				t.Fatalf("before change: %d %v", res.status, res.body)
			}

			// iam demotes u1: bumps the version and publishes it (write-through)
			s.iam.setVersion("t1", "u1", 2)
			if s.redis != nil {
				s.redis.Set(token.MembershipVersionKey("t1", "u1"), "2")
			}
			res := s.do("GET", "/v1/notes", "", tok)
			if res.status != 401 || !strings.Contains(errMsg(res), "refresh") {
				t.Fatalf("stale token: %d %v", res.status, res.body)
			}

			// a token with the new version works
			if res := s.do("GET", "/v1/notes", "", s.tokenFor("u1", "t1", "viewer", 2)); res.status != 200 {
				t.Fatalf("fresh token: %d %v", res.status, res.body)
			}

			// removal
			s.iam.mu.Lock()
			delete(s.iam.versions, "t1/u1")
			s.iam.mu.Unlock()
			if s.redis != nil {
				s.redis.Set(token.MembershipVersionKey("t1", "u1"), strconv.FormatInt(token.RemovedMembershipVersion, 10))
			}
			fresh, _, _ := s.iam.signer.Sign("u1", "t1", "viewer", 2)
			if res := s.do("GET", "/v1/notes", "", fresh); res.status != 401 {
				t.Fatalf("removed member: %d %v", res.status, res.body)
			}
		})
	}
}

func TestVersionCacheAndFallbacks(t *testing.T) {
	s := newStack(t, stackOptions{redis: true})
	tok := s.tokenFor("u1", "t1", "member", 1)

	for i := 0; i < 5; i++ {
		if res := s.do("GET", "/v1/notes", "", tok); res.status != 200 {
			t.Fatalf("request %d: %d", i, res.status)
		}
	}
	if n := s.iam.versionCalls.Load(); n != 1 {
		t.Fatalf("iam asked %d times, want 1 (then served from Redis)", n)
	}
	if v, _ := s.redis.Get(token.MembershipVersionKey("t1", "u1")); v != "1" {
		t.Fatalf("cached %q", v)
	}

	// the version published in Redis is authoritative: iam's stale answer is never consulted
	s.redis.Set(token.MembershipVersionKey("t1", "u2"), "5")
	if res := s.do("GET", "/v1/notes", "", s.tokenFor("u2", "t1", "member", 4)); res.status != 401 {
		t.Fatalf("redis must win over a stale token: %d", res.status)
	}

	// Redis down: still works by asking iam
	s.redis.Close()
	if res := s.do("GET", "/v1/notes", "", tok); res.status != 200 {
		t.Fatalf("redis down: %d %v", res.status, res.body)
	}
}

func TestIamDownFailsClosed(t *testing.T) {
	s := newStack(t, stackOptions{iamDown: true})
	tok, _, _ := s.iam.signer.Sign("u1", "t1", "owner", 1)
	res := s.do("GET", "/v1/notes", "", tok)
	if res.status != http.StatusServiceUnavailable {
		t.Fatalf("got %d %v", res.status, res.body)
	}
}

func TestAuthAndTenantEndpoints(t *testing.T) {
	s := newStack(t, stackOptions{devLogin: true})

	res := s.do("POST", "/v1/auth/dev-login", `{"user_id":"alice"}`, "")
	pair := data(res)
	if res.status != 200 || pair["token_type"] != "Bearer" || pair["access_token"] == "" || pair["refresh_token"] != "refresh-alice" {
		t.Fatalf("dev login: %d %v", res.status, res.body)
	}
	access := pair["access_token"].(string)

	// a tenant-less token reaches tenant-less endpoints without a version check
	res = s.do("GET", "/v1/me/tenants", "", access)
	if list, _ := res.body["data"].([]any); res.status != 200 || len(list) != 1 {
		t.Fatalf("my tenants: %d %v", res.status, res.body)
	}
	if s.iam.lastID.UserID != "alice" || s.iam.lastID.TenantID != "" || s.iam.versionCalls.Load() != 0 {
		t.Fatalf("identity %+v, version calls %d", s.iam.lastID, s.iam.versionCalls.Load())
	}

	if res := s.do("POST", "/v1/auth/refresh", `{"refresh_token":"refresh-alice"}`, ""); res.status != 200 {
		t.Fatalf("refresh: %d %v", res.status, res.body)
	}
	if res := s.do("POST", "/v1/auth/refresh", `{"refresh_token":"stolen"}`, ""); res.status != 401 {
		t.Fatalf("bad refresh: %d", res.status)
	}
	if res := s.do("POST", "/v1/auth/logout", `{"refresh_token":"refresh-alice"}`, ""); res.status != 204 {
		t.Fatalf("logout: %d", res.status)
	}

	owner := s.tokenFor("alice", "t1", "owner", 1)
	res = s.do("PATCH", "/v1/members/max", `{"role":"viewer"}`, owner)
	if res.status != 200 || data(res)["role"] != "viewer" || s.iam.lastUpdate.GetUserId() != "max" || s.iam.lastID.TenantID != "t1" {
		t.Fatalf("update member: %d %v", res.status, res.body)
	}
	if res := s.do("PATCH", "/v1/members/max", `{"role":"owner"}`, owner); res.status != 403 || errCode(res) != "forbidden" {
		t.Fatalf("iam rule surfaced: %d %v", res.status, res.body)
	}
}

func TestPasswordEndpoints(t *testing.T) {
	s := newStack(t, stackOptions{})

	res := s.do("POST", "/v1/auth/register", `{"email":"alice@example.com","password":"correct horse battery"}`, "")
	reg := data(res)
	user, _ := reg["user"].(map[string]any)
	tokens, _ := reg["tokens"].(map[string]any)
	if res.status != 201 || user["email"] != "alice@example.com" || tokens["access_token"] == "" {
		t.Fatalf("register: %d %v", res.status, res.body)
	}

	res = s.do("POST", "/v1/auth/login", `{"email":"alice@example.com","password":"correct horse battery"}`, "")
	if res.status != 200 || data(res)["token_type"] != "Bearer" {
		t.Fatalf("login: %d %v", res.status, res.body)
	}
	access := data(res)["access_token"].(string)

	res = s.do("POST", "/v1/auth/login", `{"email":"alice@example.com","password":"nope"}`, "")
	if res.status != 401 || errMsg(res) != "unauthenticated: invalid email or password" {
		t.Fatalf("bad login: %d %v", res.status, res.body)
	}

	if res := s.do("GET", "/v1/me", "", access); res.status != 200 || data(res)["id"] != "usr_1" {
		t.Fatalf("me: %d %v", res.status, res.body)
	}
	if res := s.do("GET", "/v1/me", "", ""); res.status != 401 {
		t.Fatalf("me without token: %d", res.status)
	}
}

func TestAuthEndpointsAreRateLimited(t *testing.T) {
	s := newStack(t, stackOptions{ratePerMinute: 60, rateBurst: 3})
	body := `{"email":"alice@example.com","password":"guess"}`

	for i := 0; i < 3; i++ {
		if res := s.do("POST", "/v1/auth/login", body, ""); res.status != 401 {
			t.Fatalf("attempt %d: %d", i, res.status)
		}
	}
	res := s.do("POST", "/v1/auth/login", body, "")
	if res.status != http.StatusTooManyRequests || errCode(res) != "too_many_requests" || res.header.Get("Retry-After") == "" {
		t.Fatalf("over limit: %d %v %v", res.status, res.body, res.header)
	}
	// register and refresh share the budget
	if res := s.do("POST", "/v1/auth/register", `{}`, ""); res.status != http.StatusTooManyRequests {
		t.Fatalf("register: %d", res.status)
	}

	// authenticated APIs are not limited by it
	tok := s.tokenFor("u1", "t1", "member", 1)
	if res := s.do("GET", "/v1/notes", "", tok); res.status != 200 {
		t.Fatalf("notes: %d", res.status)
	}
}
