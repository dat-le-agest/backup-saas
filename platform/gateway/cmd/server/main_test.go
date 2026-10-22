package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	iamv1 "api/gen/go/platform/iam/v1"
	"api/gen/go/platform/iam/v1/iamv1connect"
	"core/apperr"
	zaplogger "core/logger/zap"
	"core/reqctx"
	"sdk/gatewaytest"

	"github.com/gin-gonic/gin"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fakeIam adds the RPCs the platform gateway exposes to the shared fake (tokens,
// JWKS, membership versions). It records the identity of the last call.
type fakeIam struct {
	*gatewaytest.Iam

	mu         sync.Mutex
	lastID     reqctx.Identity
	lastUpdate *iamv1.UpdateMemberRoleRequest
}

func (f *fakeIam) record(ctx context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastID, _ = reqctx.IdentityFrom(ctx)
}

func (f *fakeIam) pair(userID string) *iamv1.TokenPair {
	access, exp, _ := f.Signer.Sign(userID, "", "", 0)
	return &iamv1.TokenPair{
		AccessToken: access, AccessTokenExpireTime: timestamppb.New(exp),
		RefreshToken: "refresh-" + userID, RefreshTokenExpireTime: timestamppb.New(exp.Add(time.Hour)),
	}
}

func (f *fakeIam) Register(_ context.Context, req *iamv1.RegisterRequest) (*iamv1.RegisterResponse, error) {
	return &iamv1.RegisterResponse{
		User:   &iamv1.User{Id: "usr_1", Email: req.GetEmail(), CreateTime: timestamppb.Now()},
		Tokens: f.pair("usr_1"),
	}, nil
}

func (f *fakeIam) Login(_ context.Context, req *iamv1.LoginRequest) (*iamv1.LoginResponse, error) {
	if req.GetPassword() != "correct horse battery" {
		return nil, fmt.Errorf("%w: invalid email or password", apperr.ErrUnauthenticated)
	}
	return &iamv1.LoginResponse{Tokens: f.pair("usr_1")}, nil
}

func (f *fakeIam) GetMe(ctx context.Context, _ *iamv1.GetMeRequest) (*iamv1.GetMeResponse, error) {
	id, _ := reqctx.IdentityFrom(ctx)
	return &iamv1.GetMeResponse{User: &iamv1.User{Id: id.UserID, Email: "alice@example.com", CreateTime: timestamppb.Now()}}, nil
}

func (f *fakeIam) DevLogin(_ context.Context, req *iamv1.DevLoginRequest) (*iamv1.DevLoginResponse, error) {
	return &iamv1.DevLoginResponse{Tokens: f.pair(req.GetUserId())}, nil
}

func (f *fakeIam) RefreshTokens(_ context.Context, req *iamv1.RefreshTokensRequest) (*iamv1.RefreshTokensResponse, error) {
	if !strings.HasPrefix(req.GetRefreshToken(), "refresh-") {
		return nil, fmt.Errorf("%w: invalid or expired refresh token", apperr.ErrUnauthenticated)
	}
	return &iamv1.RefreshTokensResponse{Tokens: f.pair(strings.TrimPrefix(req.GetRefreshToken(), "refresh-"))}, nil
}

func (f *fakeIam) RevokeRefreshToken(context.Context, *iamv1.RevokeRefreshTokenRequest) (*iamv1.RevokeRefreshTokenResponse, error) {
	return &iamv1.RevokeRefreshTokenResponse{}, nil
}

func (f *fakeIam) ListMyTenants(ctx context.Context, _ *iamv1.ListMyTenantsRequest) (*iamv1.ListMyTenantsResponse, error) {
	f.record(ctx)
	return &iamv1.ListMyTenantsResponse{Tenants: []*iamv1.MyTenant{{
		Tenant: &iamv1.Tenant{Id: "t1", Name: "Acme", CreateTime: timestamppb.Now()}, Role: "owner",
	}}}, nil
}

func (f *fakeIam) ListMembers(ctx context.Context, _ *iamv1.ListMembersRequest) (*iamv1.ListMembersResponse, error) {
	f.record(ctx)
	return &iamv1.ListMembersResponse{}, nil
}

func (f *fakeIam) UpdateMemberRole(ctx context.Context, req *iamv1.UpdateMemberRoleRequest) (*iamv1.UpdateMemberRoleResponse, error) {
	f.record(ctx)
	f.mu.Lock()
	f.lastUpdate = req
	f.mu.Unlock()
	if req.GetRole() == "owner" {
		return nil, fmt.Errorf("%w: only owners can grant the owner role", apperr.ErrForbidden)
	}
	return &iamv1.UpdateMemberRoleResponse{Member: &iamv1.Member{UserId: req.GetUserId(), Role: req.GetRole(), CreateTime: timestamppb.Now(), UpdateTime: timestamppb.Now()}}, nil
}

type stack struct {
	gateway *gin.Engine
	iam     *fakeIam
}

type stackOptions struct {
	devLogin bool
	// auth rate limit; zero uses the defaults
	ratePerMinute, rateBurst int
}

func newStack(t *testing.T, o stackOptions) *stack {
	t.Helper()
	log := zaplogger.NewZapLogger().GetLogger("test")

	iam := &fakeIam{Iam: gatewaytest.NewIam()}
	iamURL, jwksURL := gatewaytest.ServeIam(t, iam.Iam, iam, log)
	iamClient := gatewaytest.GRPCClient(iamURL, iamv1connect.NewIamServiceClient)

	c, err := buildContainer(iamClient)
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	s := &stack{gateway: gin.New(), iam: iam}
	registerRoutes(s.gateway, c, routeOptions{
		auth: gatewaytest.Authenticator(t, jwksURL, iamClient, nil, log), devLogin: o.devLogin, logger: log,
		authRatePerMinute: o.ratePerMinute, authRateBurst: o.rateBurst,
	})
	return s
}

func (s *stack) do(method, path, body, bearer string, headers ...string) gatewaytest.Result {
	return gatewaytest.Do(s.gateway, method, path, body, bearer, headers...)
}

func TestGatewayErrors(t *testing.T) {
	s := newStack(t, stackOptions{})
	cases := []struct {
		name, method, path, body, bearer string
		status                           int
		code                             string
	}{
		{"no auth", "GET", "/v1/me", "", "", 401, "unauthenticated"},
		{"garbage token", "GET", "/v1/me", "", "not.a.jwt", 401, "unauthenticated"},
		{"bad json", "POST", "/v1/auth/login", `{`, "", 400, "invalid_input"},
		{"unknown route", "GET", "/v2/nothing", "", "", 404, "not_found"},
		// app routes live on their app gateways
		{"no app routes here", "GET", "/v1/notes", "", s.iam.Token("u1", "", "", 0), 404, "not_found"},
		{"dev login disabled", "POST", "/v1/auth/dev-login", `{"user_id":"u1"}`, "", 404, "not_found"},
	}
	for _, tc := range cases {
		res := s.do(tc.method, tc.path, tc.body, tc.bearer)
		if res.Status != tc.status || res.ErrCode() != tc.code {
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

func TestAuthAndTenantEndpoints(t *testing.T) {
	s := newStack(t, stackOptions{devLogin: true})

	res := s.do("POST", "/v1/auth/dev-login", `{"user_id":"alice"}`, "")
	pair := res.Data()
	if res.Status != 200 || pair["token_type"] != "Bearer" || pair["access_token"] == "" || pair["refresh_token"] != "refresh-alice" {
		t.Fatalf("dev login: %d %v", res.Status, res.Body)
	}
	access := pair["access_token"].(string)

	// a tenant-less token reaches tenant-less endpoints without a version check
	res = s.do("GET", "/v1/me/tenants", "", access)
	if list, _ := res.Body["data"].([]any); res.Status != 200 || len(list) != 1 {
		t.Fatalf("my tenants: %d %v", res.Status, res.Body)
	}
	if s.iam.lastID.UserID != "alice" || s.iam.lastID.TenantID != "" || s.iam.VersionCalls.Load() != 0 {
		t.Fatalf("identity %+v, version calls %d", s.iam.lastID, s.iam.VersionCalls.Load())
	}

	if res := s.do("POST", "/v1/auth/refresh", `{"refresh_token":"refresh-alice"}`, ""); res.Status != 200 {
		t.Fatalf("refresh: %d %v", res.Status, res.Body)
	}
	if res := s.do("POST", "/v1/auth/refresh", `{"refresh_token":"stolen"}`, ""); res.Status != 401 {
		t.Fatalf("bad refresh: %d", res.Status)
	}
	if res := s.do("POST", "/v1/auth/logout", `{"refresh_token":"refresh-alice"}`, ""); res.Status != 204 {
		t.Fatalf("logout: %d", res.Status)
	}

	owner := s.iam.Member("alice", "t1", "owner", 1)
	res = s.do("PATCH", "/v1/members/max", `{"role":"viewer"}`, owner)
	if res.Status != 200 || res.Data()["role"] != "viewer" || s.iam.lastUpdate.GetUserId() != "max" || s.iam.lastID.TenantID != "t1" {
		t.Fatalf("update member: %d %v", res.Status, res.Body)
	}
	if res := s.do("PATCH", "/v1/members/max", `{"role":"owner"}`, owner); res.Status != 403 || res.ErrCode() != "forbidden" {
		t.Fatalf("iam rule surfaced: %d %v", res.Status, res.Body)
	}
}

// Tenant-scoped platform routes check the membership version like app gateways.
func TestStaleTokenRejectedOnMembers(t *testing.T) {
	s := newStack(t, stackOptions{})
	tok := s.iam.Member("u1", "t1", "admin", 1)
	if res := s.do("GET", "/v1/members", "", tok); res.Status != 200 {
		t.Fatalf("before change: %d %v", res.Status, res.Body)
	}
	s.iam.SetVersion("t1", "u1", 2)
	if res := s.do("GET", "/v1/members", "", tok); res.Status != 401 || !strings.Contains(res.ErrMsg(), "refresh") {
		t.Fatalf("stale token: %d %v", res.Status, res.Body)
	}
}

func TestPasswordEndpoints(t *testing.T) {
	s := newStack(t, stackOptions{})

	res := s.do("POST", "/v1/auth/register", `{"email":"alice@example.com","password":"correct horse battery"}`, "")
	reg := res.Data()
	user, _ := reg["user"].(map[string]any)
	tokens, _ := reg["tokens"].(map[string]any)
	if res.Status != 201 || user["email"] != "alice@example.com" || tokens["access_token"] == "" {
		t.Fatalf("register: %d %v", res.Status, res.Body)
	}

	res = s.do("POST", "/v1/auth/login", `{"email":"alice@example.com","password":"correct horse battery"}`, "")
	if res.Status != 200 || res.Data()["token_type"] != "Bearer" {
		t.Fatalf("login: %d %v", res.Status, res.Body)
	}
	access := res.Data()["access_token"].(string)

	res = s.do("POST", "/v1/auth/login", `{"email":"alice@example.com","password":"nope"}`, "")
	if res.Status != 401 || res.ErrMsg() != "unauthenticated: invalid email or password" {
		t.Fatalf("bad login: %d %v", res.Status, res.Body)
	}

	if res := s.do("GET", "/v1/me", "", access); res.Status != 200 || res.Data()["id"] != "usr_1" {
		t.Fatalf("me: %d %v", res.Status, res.Body)
	}
	if res := s.do("GET", "/v1/me", "", ""); res.Status != 401 {
		t.Fatalf("me without token: %d", res.Status)
	}
}

func TestAuthEndpointsAreRateLimited(t *testing.T) {
	s := newStack(t, stackOptions{ratePerMinute: 60, rateBurst: 3})
	body := `{"email":"alice@example.com","password":"guess"}`

	for i := 0; i < 3; i++ {
		if res := s.do("POST", "/v1/auth/login", body, ""); res.Status != 401 {
			t.Fatalf("attempt %d: %d", i, res.Status)
		}
	}
	res := s.do("POST", "/v1/auth/login", body, "")
	if res.Status != http.StatusTooManyRequests || res.ErrCode() != "too_many_requests" || res.Header.Get("Retry-After") == "" {
		t.Fatalf("over limit: %d %v %v", res.Status, res.Body, res.Header)
	}
	// register and refresh share the budget
	if res := s.do("POST", "/v1/auth/register", `{}`, ""); res.Status != http.StatusTooManyRequests {
		t.Fatalf("register: %d", res.Status)
	}

	// authenticated APIs are not limited by it
	if res := s.do("GET", "/v1/me/tenants", "", s.iam.Token("u1", "", "", 0)); res.Status != 200 {
		t.Fatalf("my tenants: %d", res.Status)
	}
}
