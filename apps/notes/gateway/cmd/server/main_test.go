package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	notev1 "api/gen/go/apps/notes/note/v1"
	"api/gen/go/apps/notes/note/v1/notev1connect"
	"api/gen/go/platform/iam/v1/iamv1connect"
	"core/apperr"
	zaplogger "core/logger/zap"
	"core/reqctx"
	"core/rpc"
	"sdk/gatewaytest"
	"sdk/token"

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

type stack struct {
	gateway *gin.Engine
	notes   *fakeNotes
	iam     *gatewaytest.Iam
	redis   *miniredis.Miniredis // nil when the gateway runs without Redis
}

type stackOptions struct {
	redis   bool
	iamDown bool
}

func newStack(t *testing.T, o stackOptions) *stack {
	t.Helper()
	log := zaplogger.NewZapLogger().GetLogger("test")

	notes := &fakeNotes{notes: map[uint64]*notev1.Note{}}
	nmux := http.NewServeMux()
	nmux.Handle(notev1connect.NewNoteServiceHandler(notes, rpc.HandlerOptions(log)))
	notesURL := gatewaytest.ServeH2C(t, nmux)

	iam := gatewaytest.NewIam()
	iamURL, jwksURL := gatewaytest.ServeIam(t, iam, iam, log)
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
	// only tokens of this gateway's app (gatewaytest.App stands for notes)
	auth := gatewaytest.Authenticator(t, jwksURL, gatewaytest.App, gatewaytest.GRPCClient(iamURL, iamv1connect.NewIamServiceClient), rdb, log)

	c, err := buildContainer(gatewaytest.GRPCClient(notesURL, notev1connect.NewNoteServiceClient))
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	s.gateway = gin.New()
	registerRoutes(s.gateway, c, routeOptions{auth: auth, logger: log})
	return s
}

func (s *stack) do(method, path, body, bearer string, headers ...string) gatewaytest.Result {
	return gatewaytest.Do(s.gateway, method, path, body, bearer, headers...)
}

func TestNotesThroughGateway(t *testing.T) {
	s := newStack(t, stackOptions{redis: true})
	tok := s.iam.Member("u1", "t1", "member", 1)

	res := s.do(http.MethodPost, "/v1/notes", `{"title":"first","content":"hello"}`, tok, reqctx.HeaderRequestID, "trace-abc-123")
	if res.Status != http.StatusCreated {
		t.Fatalf("create: %d %v", res.Status, res.Body)
	}
	note := res.Data()
	if note["id"] != float64(1) || note["title"] != "first" || note["created_by"] != "u1" || note["created_at"] != "2026-01-02T03:04:05Z" {
		t.Fatalf("create body: %v", res.Body)
	}
	// identity (with the app) from the verified token and the request id reached the upstream
	if s.notes.lastID != (reqctx.Identity{AppID: gatewaytest.App, UserID: "u1", TenantID: "t1", Role: "member"}) || s.notes.lastReq != "trace-abc-123" {
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

func TestGatewayErrors(t *testing.T) {
	s := newStack(t, stackOptions{})
	tok := s.iam.Member("u1", "t1", "member", 1)

	other, _ := token.GenerateKey()
	forged, _, _ := token.NewSigner(other, gatewaytest.Issuer, time.Minute).Sign("u1", gatewaytest.App, "t1", "owner", 1)
	// a valid token of another app, for a membership that exists
	s.iam.SetVersion("t1", "u1", 1)
	otherApp := s.iam.AppToken("crm", "u1", "t1", "member", 1)

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
		{"token of another app", "GET", "/v1/notes", "", otherApp, nil, 401, "unauthenticated", ""},
		{"upstream validation", "POST", "/v1/notes", `{"title":" "}`, tok, nil, 400, "invalid_input", "invalid input: title is required"},
		{"bad json", "POST", "/v1/notes", `{`, tok, nil, 400, "invalid_input", "invalid input: invalid JSON body"},
		{"bad id", "GET", "/v1/notes/abc", "", tok, nil, 400, "invalid_input", ""},
		{"missing", "PATCH", "/v1/notes/42", `{}`, tok, nil, 404, "not_found", "not found"},
		{"unknown route", "GET", "/v2/nothing", "", "", nil, 404, "not_found", ""},
		// sign-in lives on the platform gateway
		{"no auth routes here", "POST", "/v1/auth/login", `{}`, "", nil, 404, "not_found", ""},
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
	res := s.do("POST", "/v1/notes", `{"title":" "}`, s.iam.Member("u1", "t1", "member", 1))
	e, _ := res.Body["error"].(map[string]any)
	details, _ := e["details"].([]any)
	if res.Status != 400 || len(details) != 1 {
		t.Fatalf("got %d %v", res.Status, res.Body)
	}
	if d := details[0].(map[string]any); d["field"] != "title" || d["description"] != "is required" {
		t.Fatalf("detail %v", d)
	}

	res = s.do("GET", "/v1/notes/abc", "", s.iam.Member("u1", "t1", "member", 1))
	e, _ = res.Body["error"].(map[string]any)
	if details, _ := e["details"].([]any); len(details) != 1 || details[0].(map[string]any)["field"] != "id" {
		t.Fatalf("gateway-side field error: %v", res.Body)
	}
}

// A role change or removal bumps the membership version: tokens issued before
// are rejected right away, not when they expire.
func TestStaleTokensRejected(t *testing.T) {
	for _, withRedis := range []bool{true, false} {
		t.Run(fmt.Sprintf("redis=%v", withRedis), func(t *testing.T) {
			s := newStack(t, stackOptions{redis: withRedis})
			tok := s.iam.Member("u1", "t1", "admin", 1)

			if res := s.do("GET", "/v1/notes", "", tok); res.Status != 200 {
				t.Fatalf("before change: %d %v", res.Status, res.Body)
			}

			// iam demotes u1: bumps the version and publishes it (write-through)
			s.iam.SetVersion("t1", "u1", 2)
			if s.redis != nil {
				s.redis.Set(token.MembershipVersionKey(gatewaytest.App, "t1", "u1"), "2")
			}
			res := s.do("GET", "/v1/notes", "", tok)
			if res.Status != 401 || !strings.Contains(res.ErrMsg(), "refresh") {
				t.Fatalf("stale token: %d %v", res.Status, res.Body)
			}

			// a token with the new version works
			if res := s.do("GET", "/v1/notes", "", s.iam.Member("u1", "t1", "viewer", 2)); res.Status != 200 {
				t.Fatalf("fresh token: %d %v", res.Status, res.Body)
			}

			// removal
			s.iam.RemoveMembership("t1", "u1")
			if s.redis != nil {
				s.redis.Set(token.MembershipVersionKey(gatewaytest.App, "t1", "u1"), strconv.FormatInt(token.RemovedMembershipVersion, 10))
			}
			if res := s.do("GET", "/v1/notes", "", s.iam.Token("u1", "t1", "viewer", 2)); res.Status != 401 {
				t.Fatalf("removed member: %d %v", res.Status, res.Body)
			}
		})
	}
}

func TestVersionCacheAndFallbacks(t *testing.T) {
	s := newStack(t, stackOptions{redis: true})
	tok := s.iam.Member("u1", "t1", "member", 1)

	for i := 0; i < 5; i++ {
		if res := s.do("GET", "/v1/notes", "", tok); res.Status != 200 {
			t.Fatalf("request %d: %d", i, res.Status)
		}
	}
	if n := s.iam.VersionCalls.Load(); n != 1 {
		t.Fatalf("iam asked %d times, want 1 (then served from Redis)", n)
	}
	if v, _ := s.redis.Get(token.MembershipVersionKey(gatewaytest.App, "t1", "u1")); v != "1" {
		t.Fatalf("cached %q", v)
	}

	// the version published in Redis is authoritative: iam's stale answer is never consulted
	s.redis.Set(token.MembershipVersionKey(gatewaytest.App, "t1", "u2"), "5")
	if res := s.do("GET", "/v1/notes", "", s.iam.Member("u2", "t1", "member", 4)); res.Status != 401 {
		t.Fatalf("redis must win over a stale token: %d", res.Status)
	}

	// Redis down: still works by asking iam
	s.redis.Close()
	if res := s.do("GET", "/v1/notes", "", tok); res.Status != 200 {
		t.Fatalf("redis down: %d %v", res.Status, res.Body)
	}
}

func TestIamDownFailsClosed(t *testing.T) {
	s := newStack(t, stackOptions{iamDown: true})
	res := s.do("GET", "/v1/notes", "", s.iam.Token("u1", "t1", "owner", 1))
	if res.Status != http.StatusServiceUnavailable {
		t.Fatalf("got %d %v", res.Status, res.Body)
	}
}
