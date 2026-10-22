package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	authzv1 "api/gen/go/platform/authz/v1"
	"api/gen/go/platform/authz/v1/authzv1connect"
	iamv1 "api/gen/go/platform/iam/v1"
	"api/gen/go/platform/iam/v1/iamv1connect"
	"core/apperr"
	"core/authz"
	"core/di"
	zaplogger "core/logger/zap"
	"core/outbox"
	"core/reqctx"
	"core/rpc"
	"core/testdb"
	"core/token"
	"iam/internal/model"
	"iam/internal/password"
	"iam/internal/service"
	grpctransport "iam/internal/transport/rpc"
	"iam/migrations"

	"connectrpc.com/connect"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"gorm.io/gorm"
)

type env struct {
	c        *di.Container
	db       *gorm.DB
	client   iamv1connect.IamServiceClient
	verifier *token.Verifier
	redis    *miniredis.Miniredis
	jwksURL  string
}

func newEnv(t *testing.T, devLogin bool) *env {
	t.Helper()
	return newEnvWith(t, devLogin, nil)
}

func newEnvWith(t *testing.T, devLogin bool, authzClient authzv1connect.AuthzServiceClient) *env {
	t.Helper()

	db := testdb.New(t, migrations.FS) // real Postgres with the service's migrations

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })

	key, _ := token.GenerateKey()
	signer := token.NewSigner(key, "iam", "api", 10*time.Minute)

	c, err := buildContainer(deps{
		db: db, redis: rdb, signer: signer,
		// cheap parameters keep tests fast; production uses password.DefaultParams
		hasher:     password.NewHasher(password.Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}, 0),
		tokens:     service.TokenConfig{DevLogin: devLogin},
		versionTTL: time.Hour,
		logger:     zaplogger.NewZapLogger().GetLogger("test"),
		authz:      authzClient,
	})
	if err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	jwks := signer.PublicKeys()
	r.GET("/.well-known/jwks.json", func(c *gin.Context) { c.JSON(http.StatusOK, jwks) })
	di.MustGet[*grpctransport.IamHandler](c).Mount(r)

	srv := httptest.NewUnstartedServer(r)
	srv.Config.Protocols = new(http.Protocols)
	srv.Config.Protocols.SetHTTP1(true)
	srv.Config.Protocols.SetUnencryptedHTTP2(true)
	srv.Start()
	t.Cleanup(srv.Close)

	p := new(http.Protocols)
	p.SetUnencryptedHTTP2(true)
	client := iamv1connect.NewIamServiceClient(
		&http.Client{Transport: &http.Transport{Protocols: p}}, srv.URL,
		connect.WithGRPC(),
		connect.WithInterceptors(rpc.ClientMetadataInterceptor(), rpc.ClientErrorInterceptor()),
	)

	return &env{
		c:        c,
		db:       db,
		client:   client,
		verifier: token.NewVerifier(token.NewStaticKeys(key.Public().(ed25519.PublicKey)), "iam", "api"),
		redis:    mr,
		jwksURL:  srv.URL + "/.well-known/jwks.json",
	}
}

// as verifies an access token and returns a context carrying its identity,
// like the gateway does before forwarding a request.
func (e *env) as(t *testing.T, access string) context.Context {
	t.Helper()
	c, err := e.verifier.Verify(access)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	return reqctx.WithIdentity(context.Background(), reqctx.Identity{UserID: c.UserID(), TenantID: c.TenantID, Role: c.Role})
}

func (e *env) claims(t *testing.T, access string) *token.Claims {
	t.Helper()
	c, err := e.verifier.Verify(access)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (e *env) cachedVersion(t *testing.T, tenantID, userID string) int64 {
	t.Helper()
	v, err := e.redis.Get(token.MembershipVersionKey(tenantID, userID))
	if err != nil {
		t.Fatalf("no cached version for %s/%s", tenantID, userID)
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

func (e *env) login(t *testing.T, userID, tenantID string) *iamv1.TokenPair {
	t.Helper()
	res, err := e.client.DevLogin(context.Background(), &iamv1.DevLoginRequest{UserId: userID, TenantId: tenantID})
	if err != nil {
		t.Fatalf("login %s: %v", userID, err)
	}
	return res.GetTokens()
}

// setupTenant: alice creates a tenant and gets an owner token for it.
func (e *env) setupTenant(t *testing.T) (tenantID string, ownerCtx context.Context) {
	t.Helper()
	pair := e.login(t, "alice", "")
	res, err := e.client.CreateTenant(e.as(t, pair.GetAccessToken()), &iamv1.CreateTenantRequest{Name: " Acme "})
	if err != nil {
		t.Fatal(err)
	}
	tenantID = res.GetTenant().GetId()
	switched, err := e.client.SwitchTenant(e.as(t, pair.GetAccessToken()), &iamv1.SwitchTenantRequest{TenantId: tenantID})
	if err != nil {
		t.Fatal(err)
	}
	return tenantID, e.as(t, switched.GetTokens().GetAccessToken())
}

func expect(t *testing.T, name string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Errorf("%s: expected %v, got %v", name, want, err)
	}
}

func TestLoginTenantAndSwitch(t *testing.T) {
	e := newEnv(t, true)

	pair := e.login(t, "alice", "")
	c := e.claims(t, pair.GetAccessToken())
	if c.UserID() != "alice" || c.TenantID != "" || pair.GetRefreshToken() == "" {
		t.Fatalf("tenant-less login: %+v", c)
	}

	res, err := e.client.CreateTenant(e.as(t, pair.GetAccessToken()), &iamv1.CreateTenantRequest{Name: " Acme "})
	if err != nil {
		t.Fatal(err)
	}
	tenant := res.GetTenant()
	if tenant.GetName() != "Acme" || len(tenant.GetId()) < 8 {
		t.Fatalf("tenant: %v", tenant)
	}
	if v := e.cachedVersion(t, tenant.GetId(), "alice"); v != 1 {
		t.Fatalf("cached version %d", v)
	}

	mine, err := e.client.ListMyTenants(e.as(t, pair.GetAccessToken()), &iamv1.ListMyTenantsRequest{})
	if err != nil || len(mine.GetTenants()) != 1 || mine.GetTenants()[0].GetRole() != authz.RoleOwner {
		t.Fatalf("my tenants: %v %v", mine, err)
	}

	switched, err := e.client.SwitchTenant(e.as(t, pair.GetAccessToken()), &iamv1.SwitchTenantRequest{TenantId: tenant.GetId()})
	if err != nil {
		t.Fatal(err)
	}
	c = e.claims(t, switched.GetTokens().GetAccessToken())
	if c.TenantID != tenant.GetId() || c.Role != authz.RoleOwner || c.MembershipVersion != 1 {
		t.Fatalf("switched claims: %+v", c)
	}

	// someone else can't switch into alice's tenant, and can't tell it exists
	bob := e.login(t, "bob", "")
	_, err = e.client.SwitchTenant(e.as(t, bob.GetAccessToken()), &iamv1.SwitchTenantRequest{TenantId: tenant.GetId()})
	expect(t, "bob switch", err, apperr.ErrNotFound)
	_, err = e.client.DevLogin(context.Background(), &iamv1.DevLoginRequest{UserId: "bob", TenantId: tenant.GetId()})
	expect(t, "bob login into tenant", err, apperr.ErrNotFound)

	// the published JWKS verifies issued tokens
	jwksVerifier := token.NewVerifier(token.NewRemoteJWKS(e.jwksURL, nil), "iam", "api")
	if _, err := jwksVerifier.Verify(switched.GetTokens().GetAccessToken()); err != nil {
		t.Fatalf("verify via jwks: %v", err)
	}
	r, _ := http.Get(e.jwksURL)
	var set token.JWKS
	_ = json.NewDecoder(r.Body).Decode(&set)
	r.Body.Close()
	if len(set.Keys) != 1 || set.Keys[0].Kty != "OKP" {
		t.Fatalf("jwks: %+v", set)
	}
}

func TestMemberManagementRules(t *testing.T) {
	e := newEnv(t, true)
	tenantID, owner := e.setupTenant(t)
	c := e.client

	add := func(ctx context.Context, user, role string) error {
		_, err := c.AddMember(ctx, &iamv1.AddMemberRequest{UserId: user, Role: role})
		return err
	}
	update := func(ctx context.Context, user, role string) error {
		_, err := c.UpdateMemberRole(ctx, &iamv1.UpdateMemberRoleRequest{UserId: user, Role: role})
		return err
	}
	remove := func(ctx context.Context, user string) error {
		_, err := c.RemoveMember(ctx, &iamv1.RemoveMemberRequest{UserId: user})
		return err
	}
	tokenFor := func(user string) context.Context {
		return e.as(t, e.login(t, user, tenantID).GetAccessToken())
	}
	for _, u := range []string{"ann", "max", "eve", "zed"} {
		e.login(t, u, "") // dev login creates the user
	}

	if err := add(owner, "ann", authz.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := add(owner, "max", authz.RoleMember); err != nil {
		t.Fatal(err)
	}
	expect(t, "duplicate", add(owner, "max", authz.RoleViewer), apperr.ErrConflict)
	expect(t, "bad role", add(owner, "zed", "root"), apperr.ErrInvalid)
	expect(t, "bad user id", add(owner, "no spaces", authz.RoleViewer), apperr.ErrInvalid)

	admin, member := tokenFor("ann"), tokenFor("max")

	expect(t, "admin grants owner", add(admin, "eve", authz.RoleOwner), apperr.ErrForbidden)
	expect(t, "admin demotes owner", update(admin, "alice", authz.RoleViewer), apperr.ErrForbidden)
	expect(t, "admin removes owner", remove(admin, "alice"), apperr.ErrForbidden)
	expect(t, "member adds", add(member, "eve", authz.RoleViewer), apperr.ErrForbidden)
	expect(t, "member removes others", remove(member, "ann"), apperr.ErrForbidden)

	if _, err := c.ListMembers(member, &iamv1.ListMembersRequest{}); err != nil {
		t.Errorf("member lists: %v", err)
	}

	// a role change bumps the version: max's current token (mv=1) is now stale
	if err := update(admin, "max", authz.RoleViewer); err != nil {
		t.Fatal(err)
	}
	if v := e.cachedVersion(t, tenantID, "max"); v != 2 {
		t.Fatalf("max version after change: %d", v)
	}

	// last owner protection
	expect(t, "demote last owner", update(owner, "alice", authz.RoleAdmin), apperr.ErrInvalid)
	expect(t, "last owner leaves", remove(owner, "alice"), apperr.ErrInvalid)
	if err := update(owner, "ann", authz.RoleOwner); err != nil {
		t.Fatalf("owner promotes admin: %v", err)
	}
	if err := update(owner, "alice", authz.RoleAdmin); err != nil {
		t.Fatalf("demote with another owner: %v", err)
	}

	// any member may leave
	if err := remove(tokenFor("max"), "max"); err != nil {
		t.Fatalf("leave: %v", err)
	}
	if v := e.cachedVersion(t, tenantID, "max"); v != token.RemovedMembershipVersion {
		t.Fatalf("removed version: %d", v)
	}
	_, err := c.GetMembershipVersion(context.Background(), &iamv1.GetMembershipVersionRequest{TenantId: tenantID, UserId: "max"})
	expect(t, "version after removal", err, apperr.ErrNotFound)

	// re-adding continues the version sequence: tokens from before can never match again
	if err := add(tokenFor("ann"), "max", authz.RoleMember); err != nil {
		t.Fatal(err)
	}
	res, _ := c.GetMembershipVersion(context.Background(), &iamv1.GetMembershipVersionRequest{TenantId: tenantID, UserId: "max"})
	if res.GetVersion() <= 3 {
		t.Fatalf("version restarted: %d", res.GetVersion())
	}

	list, _ := c.ListMembers(owner, &iamv1.ListMembersRequest{})
	if len(list.GetMembers()) != 3 {
		t.Fatalf("members: %v", list)
	}
}

// A demoted admin still holding an admin token must not keep admin powers:
// iam re-reads the caller's role from the database.
func TestCallerRoleComesFromDatabase(t *testing.T) {
	e := newEnv(t, true)
	tenantID, owner := e.setupTenant(t)
	e.login(t, "ann", "")
	e.login(t, "eve", "")

	if _, err := e.client.AddMember(owner, &iamv1.AddMemberRequest{UserId: "ann", Role: authz.RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	staleAdmin := e.as(t, e.login(t, "ann", tenantID).GetAccessToken())

	if _, err := e.client.UpdateMemberRole(owner, &iamv1.UpdateMemberRoleRequest{UserId: "ann", Role: authz.RoleViewer}); err != nil {
		t.Fatal(err)
	}

	_, err := e.client.AddMember(staleAdmin, &iamv1.AddMemberRequest{UserId: "eve", Role: authz.RoleMember})
	expect(t, "stale admin token", err, apperr.ErrForbidden)
}

func TestRefreshRotationAndReuse(t *testing.T) {
	e := newEnv(t, true)
	tenantID, owner := e.setupTenant(t)
	e.login(t, "max", "")
	if _, err := e.client.AddMember(owner, &iamv1.AddMemberRequest{UserId: "max", Role: authz.RoleMember}); err != nil {
		t.Fatal(err)
	}
	first := e.login(t, "max", tenantID)

	refresh := func(raw string) (*iamv1.TokenPair, error) {
		res, err := e.client.RefreshTokens(context.Background(), &iamv1.RefreshTokensRequest{RefreshToken: raw})
		return res.GetTokens(), err
	}

	second, err := refresh(first.GetRefreshToken())
	if err != nil {
		t.Fatal(err)
	}
	if second.GetRefreshToken() == first.GetRefreshToken() {
		t.Fatal("refresh token not rotated")
	}

	// role change, then refresh: new token carries the new role and version
	if _, err := e.client.UpdateMemberRole(owner, &iamv1.UpdateMemberRoleRequest{UserId: "max", Role: authz.RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	third, err := refresh(second.GetRefreshToken())
	if err != nil {
		t.Fatal(err)
	}
	if c := e.claims(t, third.GetAccessToken()); c.Role != authz.RoleAdmin || c.MembershipVersion != 2 {
		t.Fatalf("claims after refresh: %+v", c)
	}

	// replaying an already used token revokes the whole family, including the latest one
	_, err = refresh(first.GetRefreshToken())
	expect(t, "reuse", err, apperr.ErrUnauthenticated)
	_, err = refresh(third.GetRefreshToken())
	expect(t, "family revoked", err, apperr.ErrUnauthenticated)

	_, err = refresh("garbage")
	expect(t, "unknown token", err, apperr.ErrUnauthenticated)
}

func TestLogoutAndRemovalEndSessions(t *testing.T) {
	e := newEnv(t, true)
	tenantID, owner := e.setupTenant(t)
	e.login(t, "max", "")
	if _, err := e.client.AddMember(owner, &iamv1.AddMemberRequest{UserId: "max", Role: authz.RoleMember}); err != nil {
		t.Fatal(err)
	}

	// logout
	pair := e.login(t, "max", tenantID)
	if _, err := e.client.RevokeRefreshToken(context.Background(), &iamv1.RevokeRefreshTokenRequest{RefreshToken: pair.GetRefreshToken()}); err != nil {
		t.Fatal(err)
	}
	_, err := e.client.RefreshTokens(context.Background(), &iamv1.RefreshTokensRequest{RefreshToken: pair.GetRefreshToken()})
	expect(t, "after logout", err, apperr.ErrUnauthenticated)

	// removal revokes every session of that user in the tenant
	a, b := e.login(t, "max", tenantID), e.login(t, "max", tenantID)
	if _, err := e.client.RemoveMember(owner, &iamv1.RemoveMemberRequest{UserId: "max"}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []*iamv1.TokenPair{a, b} {
		_, err := e.client.RefreshTokens(context.Background(), &iamv1.RefreshTokensRequest{RefreshToken: p.GetRefreshToken()})
		expect(t, "after removal", err, apperr.ErrUnauthenticated)
	}
}

func TestDevLoginDisabled(t *testing.T) {
	e := newEnv(t, false)
	_, err := e.client.DevLogin(context.Background(), &iamv1.DevLoginRequest{UserId: "alice"})
	expect(t, "dev login off", err, apperr.ErrForbidden)
}

func (e *env) register(t *testing.T, email, pw string) *iamv1.RegisterResponse {
	t.Helper()
	res, err := e.client.Register(context.Background(), &iamv1.RegisterRequest{Email: email, Password: pw})
	if err != nil {
		t.Fatalf("register %s: %v", email, err)
	}
	return res
}

func (e *env) passwordLogin(email, pw, tenantID string) (*iamv1.TokenPair, error) {
	res, err := e.client.Login(context.Background(), &iamv1.LoginRequest{Email: email, Password: pw, TenantId: tenantID})
	return res.GetTokens(), err
}

func TestRegisterAndLogin(t *testing.T) {
	e := newEnv(t, false) // password login works without dev login
	const pw = "correct horse battery"

	reg := e.register(t, "  Alice@Example.COM ", pw)
	user := reg.GetUser()
	if user.GetEmail() != "alice@example.com" || !strings.HasPrefix(user.GetId(), "usr_") {
		t.Fatalf("user: %v", user)
	}
	if c := e.claims(t, reg.GetTokens().GetAccessToken()); c.UserID() != user.GetId() || c.TenantID != "" {
		t.Fatalf("register tokens: %+v", c)
	}

	me, err := e.client.GetMe(e.as(t, reg.GetTokens().GetAccessToken()), &iamv1.GetMeRequest{})
	if err != nil || me.GetUser().GetEmail() != "alice@example.com" {
		t.Fatalf("me: %v %v", me, err)
	}

	_, err = e.client.Register(context.Background(), &iamv1.RegisterRequest{Email: "ALICE@example.com", Password: "another password"})
	expect(t, "duplicate email", err, apperr.ErrConflict)

	pair, err := e.passwordLogin("alice@EXAMPLE.com", pw, "")
	if err != nil {
		t.Fatal(err)
	}
	if e.claims(t, pair.GetAccessToken()).UserID() != user.GetId() {
		t.Fatal("login issued a token for someone else")
	}

	// login straight into a tenant
	created, err := e.client.CreateTenant(e.as(t, pair.GetAccessToken()), &iamv1.CreateTenantRequest{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	pair, err = e.passwordLogin("alice@example.com", pw, created.GetTenant().GetId())
	if err != nil {
		t.Fatal(err)
	}
	if c := e.claims(t, pair.GetAccessToken()); c.TenantID != created.GetTenant().GetId() || c.Role != authz.RoleOwner {
		t.Fatalf("tenant login: %+v", c)
	}

	// wrong password and unknown email are indistinguishable
	_, errWrong := e.passwordLogin("alice@example.com", "wrong password!", "")
	_, errUnknown := e.passwordLogin("nobody@example.com", pw, "")
	expect(t, "wrong password", errWrong, apperr.ErrUnauthenticated)
	expect(t, "unknown email", errUnknown, apperr.ErrUnauthenticated)
	if errWrong.Error() != errUnknown.Error() {
		t.Fatalf("errors differ: %q vs %q", errWrong, errUnknown)
	}

	// dev users have no password
	devEnv := newEnv(t, true)
	devEnv.login(t, "bob", "")
	_, err = devEnv.passwordLogin("bob", "whatever-password", "")
	expect(t, "dev user password login", err, apperr.ErrUnauthenticated)
}

func TestRegisterValidation(t *testing.T) {
	e := newEnv(t, false)
	cases := map[string][2]string{
		"no at":          {"alice.example.com", "long enough password"},
		"display name":   {"Alice <alice@example.com>", "long enough password"},
		"no tld":         {"alice@localhost", "long enough password"},
		"short password": {"alice@example.com", "short"},
		"blank password": {"alice@example.com", "              "},
		"email as pw":    {"alice@example.com", "ALICE@example.com"},
		"local part pw":  {"alicealice@example.com", "AliceAlice"},
		"too long":       {"alice@example.com", strings.Repeat("x", 129)},
	}
	for name, c := range cases {
		_, err := e.client.Register(context.Background(), &iamv1.RegisterRequest{Email: c[0], Password: c[1]})
		expect(t, name, err, apperr.ErrInvalid)
	}
}

func TestLoginThrottlePerEmail(t *testing.T) {
	e := newEnv(t, false)
	const pw = "correct horse battery"
	e.register(t, "alice@example.com", pw)

	for i := 0; i < 5; i++ {
		_, err := e.passwordLogin("alice@example.com", "wrong password!", "")
		expect(t, "wrong attempt", err, apperr.ErrUnauthenticated)
	}
	// locked: even the right password is refused
	_, err := e.passwordLogin("alice@example.com", pw, "")
	expect(t, "locked", err, apperr.ErrTooManyRequests)

	// unknown emails lock the same way, so the lock doesn't reveal registered accounts
	for i := 0; i < 5; i++ {
		_, _ = e.passwordLogin("ghost@example.com", "wrong password!", "")
	}
	_, err = e.passwordLogin("ghost@example.com", "wrong password!", "")
	expect(t, "unknown email locked", err, apperr.ErrTooManyRequests)

	// other accounts are unaffected
	e.register(t, "bob@example.com", pw)
	if _, err := e.passwordLogin("bob@example.com", pw, ""); err != nil {
		t.Fatalf("bob: %v", err)
	}

	// after the lock expires, the right password works and resets the counter
	e.db.Model(&model.LoginThrottle{}).Where("email = ?", "alice@example.com").Update("locked_until", time.Now().Add(-time.Second))
	if _, err := e.passwordLogin("alice@example.com", pw, ""); err != nil {
		t.Fatalf("after lock expiry: %v", err)
	}
	var n int64
	e.db.Model(&model.LoginThrottle{}).Where("email = ?", "alice@example.com").Count(&n)
	if n != 0 {
		t.Fatal("throttle not reset after successful login")
	}
}

func TestChangePassword(t *testing.T) {
	e := newEnv(t, false)
	const oldPW, newPW = "correct horse battery", "battery staple horse"
	reg := e.register(t, "alice@example.com", oldPW)
	ctx := e.as(t, reg.GetTokens().GetAccessToken())
	other, _ := e.passwordLogin("alice@example.com", oldPW, "")

	change := func(current, next string) error {
		_, err := e.client.ChangePassword(ctx, &iamv1.ChangePasswordRequest{CurrentPassword: current, NewPassword: next})
		return err
	}
	expect(t, "wrong current", change("not my password", newPW), apperr.ErrForbidden)
	expect(t, "weak new", change(oldPW, "short"), apperr.ErrInvalid)
	if err := change(oldPW, newPW); err != nil {
		t.Fatal(err)
	}

	// every session of the user ended
	for _, rt := range []string{reg.GetTokens().GetRefreshToken(), other.GetRefreshToken()} {
		_, err := e.client.RefreshTokens(context.Background(), &iamv1.RefreshTokensRequest{RefreshToken: rt})
		expect(t, "old session", err, apperr.ErrUnauthenticated)
	}
	_, err := e.passwordLogin("alice@example.com", oldPW, "")
	expect(t, "old password", err, apperr.ErrUnauthenticated)
	if _, err := e.passwordLogin("alice@example.com", newPW, ""); err != nil {
		t.Fatalf("new password: %v", err)
	}
}

func TestAddMemberByEmail(t *testing.T) {
	e := newEnv(t, true)
	_, owner := e.setupTenant(t)
	max := e.register(t, "max@example.com", "correct horse battery").GetUser()

	res, err := e.client.AddMember(owner, &iamv1.AddMemberRequest{Email: " MAX@example.com", Role: authz.RoleMember})
	if err != nil {
		t.Fatal(err)
	}
	if res.GetMember().GetUserId() != max.GetId() || res.GetMember().GetEmail() != "max@example.com" {
		t.Fatalf("member: %v", res.GetMember())
	}

	_, err = e.client.AddMember(owner, &iamv1.AddMemberRequest{Email: "nobody@example.com", Role: authz.RoleMember})
	expect(t, "unknown email", err, apperr.ErrNotFound)
	_, err = e.client.AddMember(owner, &iamv1.AddMemberRequest{UserId: max.GetId(), Email: "max@example.com", Role: authz.RoleMember})
	expect(t, "both id and email", err, apperr.ErrInvalid)

	// non-admins can't use AddMember to probe which emails are registered
	maxTokens, _ := e.passwordLogin("max@example.com", "correct horse battery", res.GetMember().GetTenantId())
	_, err = e.client.AddMember(e.as(t, maxTokens.GetAccessToken()), &iamv1.AddMemberRequest{Email: "nobody@example.com", Role: authz.RoleViewer})
	expect(t, "member probing", err, apperr.ErrForbidden)

	list, _ := e.client.ListMembers(owner, &iamv1.ListMembersRequest{})
	emails := map[string]string{}
	for _, m := range list.GetMembers() {
		emails[m.GetUserId()] = m.GetEmail()
	}
	if emails[max.GetId()] != "max@example.com" || emails["alice"] != "" {
		t.Fatalf("emails in member list: %v", emails)
	}
}

func TestAuthMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	t.Cleanup(func() { otel.SetMeterProvider(prev) })

	e := newEnv(t, false) // services pick up the meter provider set above
	e.register(t, "alice@example.com", "correct horse battery")
	_, _ = e.passwordLogin("alice@example.com", "correct horse battery", "")
	_, _ = e.passwordLogin("alice@example.com", "wrong password!", "")
	_, _ = e.passwordLogin("nobody@example.com", "wrong password!", "")

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	logins := map[string]int64{}
	var registrations int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			for _, dp := range sum.DataPoints {
				switch m.Name {
				case "iam.auth.logins":
					result, _ := dp.Attributes.Value("result")
					logins[result.AsString()] += dp.Value
				case "iam.auth.registrations":
					registrations += dp.Value
				}
			}
		}
	}
	if registrations != 1 || logins["success"] != 1 || logins["invalid_credentials"] != 2 {
		t.Fatalf("registrations=%d logins=%v", registrations, logins)
	}
}

// fakeAuthz records each WriteTuples call the outbox relay makes.
type fakeAuthz struct {
	authzv1connect.AuthzServiceClient
	calls [][2][]string // writes, deletes
}

func (f *fakeAuthz) WriteTuples(_ context.Context, req *authzv1.WriteTuplesRequest) (*authzv1.WriteTuplesResponse, error) {
	str := func(ts []*authzv1.Tuple) []string {
		out := []string{}
		for _, t := range ts {
			out = append(out, t.GetObject()+"#"+t.GetRelation()+"@"+t.GetSubject())
		}
		return out
	}
	f.calls = append(f.calls, [2][]string{str(req.GetWrites()), str(req.GetDeletes())})
	return &authzv1.WriteTuplesResponse{}, nil
}

// Membership changes reach the authz service through the outbox, in order;
// a role change is a single atomic change.
func TestMembershipsSyncThroughOutbox(t *testing.T) {
	fake := &fakeAuthz{}
	e := newEnvWith(t, true, fake)
	tenantID, owner := e.setupTenant(t)
	e.login(t, "max", "")

	if _, err := e.client.AddMember(owner, &iamv1.AddMemberRequest{UserId: "max", Role: authz.RoleMember}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.client.UpdateMemberRole(owner, &iamv1.UpdateMemberRoleRequest{UserId: "max", Role: authz.RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.client.RemoveMember(owner, &iamv1.RemoveMemberRequest{UserId: "max"}); err != nil {
		t.Fatal(err)
	}
	// refused changes leave nothing behind
	if _, err := e.client.UpdateMemberRole(owner, &iamv1.UpdateMemberRoleRequest{UserId: "alice", Role: authz.RoleViewer}); err == nil {
		t.Fatal("last owner demoted")
	}

	relay := di.MustGet[*outbox.Outbox](e.c).NewRelay(e.db, zaplogger.NewZapLogger().GetLogger("test"), outbox.RelayOptions{})
	relay.Handle(authz.TuplesTopic, authz.DeliverTuples(deps{authz: fake}.writeTuples))
	if n, err := relay.RunOnce(context.Background()); err != nil || n != 4 {
		t.Fatalf("relay: %d %v", n, err)
	}

	tn := "tenant:" + tenantID
	want := [][2][]string{
		{{tn + "#owner@user:alice"}, {}},
		{{tn + "#member@user:max"}, {}},
		{{tn + "#admin@user:max"}, {tn + "#member@user:max"}}, // one message
		{{}, {tn + "#admin@user:max"}},
	}
	if !reflect.DeepEqual(fake.calls, want) {
		t.Fatalf("delivered\n %v\nwant\n %v", fake.calls, want)
	}
}
