package authn_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"core/apperr"
	"core/authn"
	"core/authn/migrations"
	"core/authn/model"
	"core/authn/password"
	"core/authn/service"
	"core/authz"
	zaplogger "core/logger/zap"
	"core/reqctx"
	"core/testdb"
	"core/token"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const app = "notes"

// env is an app's authentication on a real Postgres, with Redis.
type env struct {
	m        *authn.Module
	db       *gorm.DB
	redis    *miniredis.Miniredis
	verifier *token.Verifier
}

func newEnv(t *testing.T) *env {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	key, _ := token.GenerateKey()
	signer := token.NewSigner(key, "saas-iam", 10*time.Minute)
	db := testdb.New(t, migrations.FS)
	m, err := authn.New(authn.Config{
		App: app, DB: db, Signer: signer, Redis: rdb,
		// cheap parameters keep tests fast; production uses password.DefaultParams
		Hasher:   password.NewHasher(password.Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}, 0),
		DevLogin: true, Logger: zaplogger.NewZapLogger().GetLogger("test"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &env{m: m, db: db, redis: mr, verifier: signer.Verifier(app)}
}

// as verifies an access token and returns a context carrying its identity,
// like the gateway does.
func (e *env) as(t *testing.T, access string) context.Context {
	t.Helper()
	c, err := e.verifier.Verify(access)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	return reqctx.WithIdentity(context.Background(), reqctx.Identity{AppID: c.AppID(), UserID: c.UserID(), TenantID: c.TenantID, Role: c.Role})
}

func (e *env) login(t *testing.T, userID, tenantID string) *model.TokenPair {
	t.Helper()
	pair, err := e.m.Tokens.DevLogin(context.Background(), userID, tenantID)
	if err != nil {
		t.Fatalf("login %s: %v", userID, err)
	}
	return pair
}

// setupTenant: alice creates a tenant and gets an owner token for it.
func (e *env) setupTenant(t *testing.T) (tenantID string, owner context.Context) {
	t.Helper()
	alice := e.as(t, e.login(t, "alice", "").AccessToken)
	tenant, err := e.m.Tenants.Create(alice, " Acme ")
	if err != nil {
		t.Fatal(err)
	}
	return tenant.ID, e.as(t, e.login(t, "alice", tenant.ID).AccessToken)
}

func (e *env) cachedVersion(t *testing.T, tenantID, userID string) int64 {
	t.Helper()
	v, err := e.redis.Get(token.MembershipVersionKey(app, tenantID, userID))
	if err != nil {
		t.Fatalf("no cached version for %s/%s", tenantID, userID)
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

func expect(t *testing.T, name string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Errorf("%s: expected %v, got %v", name, want, err)
	}
}

func TestMemberManagementRules(t *testing.T) {
	e := newEnv(t)
	tenantID, owner := e.setupTenant(t)
	add := func(ctx context.Context, user, role string) error {
		_, err := e.m.Members.Add(ctx, service.MemberRef{UserID: user}, role)
		return err
	}
	update := func(ctx context.Context, user, role string) error {
		_, err := e.m.Members.UpdateRole(ctx, user, role)
		return err
	}
	tokenFor := func(user string) context.Context { return e.as(t, e.login(t, user, tenantID).AccessToken) }
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
	expect(t, "admin removes owner", e.m.Members.Remove(admin, "alice"), apperr.ErrForbidden)
	expect(t, "member adds", add(member, "eve", authz.RoleViewer), apperr.ErrForbidden)
	expect(t, "member removes others", e.m.Members.Remove(member, "ann"), apperr.ErrForbidden)
	if _, err := e.m.Members.List(member); err != nil {
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
	expect(t, "last owner leaves", e.m.Members.Remove(owner, "alice"), apperr.ErrInvalid)
	if err := update(owner, "ann", authz.RoleOwner); err != nil {
		t.Fatalf("owner promotes admin: %v", err)
	}
	if err := update(owner, "alice", authz.RoleAdmin); err != nil {
		t.Fatalf("demote with another owner: %v", err)
	}

	// any member may leave; re-adding continues the version sequence
	if err := e.m.Members.Remove(tokenFor("max"), "max"); err != nil {
		t.Fatalf("leave: %v", err)
	}
	if v := e.cachedVersion(t, tenantID, "max"); v != token.RemovedMembershipVersion {
		t.Fatalf("removed version: %d", v)
	}
	_, err := e.m.Tokens.MembershipVersion(context.Background(), tenantID, "max")
	expect(t, "version after removal", err, apperr.ErrNotFound)
	if err := add(tokenFor("ann"), "max", authz.RoleMember); err != nil {
		t.Fatal(err)
	}
	if v, _ := e.m.Tokens.MembershipVersion(context.Background(), tenantID, "max"); v <= 3 {
		t.Fatalf("version restarted: %d", v)
	}
	if list, _ := e.m.Members.List(owner); len(list) != 3 {
		t.Fatalf("members: %v", list)
	}
}

// A demoted admin still holding an admin token must not keep admin powers:
// the caller's role is re-read from the database.
func TestCallerRoleComesFromDatabase(t *testing.T) {
	e := newEnv(t)
	tenantID, owner := e.setupTenant(t)
	e.login(t, "ann", "")
	e.login(t, "eve", "")
	if _, err := e.m.Members.Add(owner, service.MemberRef{UserID: "ann"}, authz.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	staleAdmin := e.as(t, e.login(t, "ann", tenantID).AccessToken)
	if _, err := e.m.Members.UpdateRole(owner, "ann", authz.RoleViewer); err != nil {
		t.Fatal(err)
	}
	_, err := e.m.Members.Add(staleAdmin, service.MemberRef{UserID: "eve"}, authz.RoleMember)
	expect(t, "stale admin token", err, apperr.ErrForbidden)
}

func TestRegisterValidation(t *testing.T) {
	e := newEnv(t)
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
		_, _, err := e.m.Auth.Register(context.Background(), c[0], c[1])
		expect(t, name, err, apperr.ErrInvalid)
	}
}

func TestLoginThrottlePerEmail(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	const pw = "correct horse battery"
	if _, _, err := e.m.Auth.Register(ctx, "alice@example.com", pw); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		_, err := e.m.Auth.Login(ctx, "alice@example.com", "wrong password!", "")
		expect(t, "wrong attempt", err, apperr.ErrUnauthenticated)
	}
	// locked: even the right password is refused
	_, err := e.m.Auth.Login(ctx, "alice@example.com", pw, "")
	expect(t, "locked", err, apperr.ErrTooManyRequests)

	// unknown emails lock the same way, so the lock doesn't reveal registered accounts
	for i := 0; i < 5; i++ {
		_, _ = e.m.Auth.Login(ctx, "ghost@example.com", "wrong password!", "")
	}
	_, err = e.m.Auth.Login(ctx, "ghost@example.com", "wrong password!", "")
	expect(t, "unknown email locked", err, apperr.ErrTooManyRequests)

	// after the lock expires, the right password works and resets the counter
	e.db.Model(&model.LoginThrottle{}).Where("email = ?", "alice@example.com").Update("locked_until", time.Now().Add(-time.Second))
	if _, err := e.m.Auth.Login(ctx, "alice@example.com", pw, ""); err != nil {
		t.Fatalf("after lock expiry: %v", err)
	}
	var n int64
	e.db.Model(&model.LoginThrottle{}).Where("email = ?", "alice@example.com").Count(&n)
	if n != 0 {
		t.Fatal("throttle not reset after successful login")
	}
}

func TestChangePasswordEndsSessions(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	const oldPW, newPW = "correct horse battery", "battery staple horse"
	_, reg, err := e.m.Auth.Register(ctx, "alice@example.com", oldPW)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := e.m.Auth.Login(ctx, "alice@example.com", oldPW, "")
	me := e.as(t, reg.AccessToken)

	expect(t, "wrong current", e.m.Auth.ChangePassword(me, "not my password", newPW), apperr.ErrForbidden)
	expect(t, "weak new", e.m.Auth.ChangePassword(me, oldPW, "short"), apperr.ErrInvalid)
	if err := e.m.Auth.ChangePassword(me, oldPW, newPW); err != nil {
		t.Fatal(err)
	}
	for _, rt := range []string{reg.RefreshToken, other.RefreshToken} {
		_, err := e.m.Tokens.Refresh(ctx, rt)
		expect(t, "old session", err, apperr.ErrUnauthenticated)
	}
	if _, err := e.m.Auth.Login(ctx, "alice@example.com", newPW, ""); err != nil {
		t.Fatalf("new password: %v", err)
	}
}

func TestAddMemberByEmail(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, owner := e.setupTenant(t)
	max, _, err := e.m.Auth.Register(ctx, "max@example.com", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.m.Members.Add(owner, service.MemberRef{Email: " MAX@example.com"}, authz.RoleMember)
	if err != nil || m.UserID != max.ID || m.Email != "max@example.com" {
		t.Fatalf("member: %+v %v", m, err)
	}
	_, err = e.m.Members.Add(owner, service.MemberRef{Email: "nobody@example.com"}, authz.RoleMember)
	expect(t, "unknown email", err, apperr.ErrNotFound)
	_, err = e.m.Members.Add(owner, service.MemberRef{UserID: max.ID, Email: "max@example.com"}, authz.RoleMember)
	expect(t, "both id and email", err, apperr.ErrInvalid)

	// non-admins can't use Add to probe which emails are registered
	maxTok, _ := e.m.Auth.Login(ctx, "max@example.com", "correct horse battery", m.TenantID)
	_, err = e.m.Members.Add(e.as(t, maxTok.AccessToken), service.MemberRef{Email: "nobody@example.com"}, authz.RoleViewer)
	expect(t, "member probing", err, apperr.ErrForbidden)
}

func parallel(n int, f func(i int) error) []error {
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = f(i)
		}(i)
	}
	close(start)
	wg.Wait()
	return errs
}

func count(errs []error, match func(error) bool) int {
	n := 0
	for _, err := range errs {
		if match(err) {
			n++
		}
	}
	return n
}

func TestConcurrentRefreshIsSingleUse(t *testing.T) {
	e := newEnv(t)
	pair := e.login(t, "alice", "")
	errs := parallel(10, func(int) error {
		_, err := e.m.Tokens.Refresh(context.Background(), pair.RefreshToken)
		return err
	})
	ok := count(errs, func(err error) bool { return err == nil })
	rejected := count(errs, func(err error) bool { return errors.Is(err, apperr.ErrUnauthenticated) })
	if ok != 1 || rejected != 9 {
		t.Fatalf("ok=%d rejected=%d errs=%v", ok, rejected, errs)
	}
}

// Two owners demoting each other at the same time: one must be refused so the
// tenant keeps an owner.
func TestConcurrentDemotionKeepsAnOwner(t *testing.T) {
	e := newEnv(t)
	tenantID, alice := e.setupTenant(t)
	e.login(t, "bob", "")
	if _, err := e.m.Members.Add(alice, service.MemberRef{UserID: "bob"}, authz.RoleOwner); err != nil {
		t.Fatal(err)
	}
	bob := e.as(t, e.login(t, "bob", tenantID).AccessToken)

	for round := 0; round < 5; round++ {
		ctxs := []context.Context{alice, bob}
		targets := []string{"bob", "alice"}
		errs := parallel(2, func(i int) error {
			_, err := e.m.Members.UpdateRole(ctxs[i], targets[i], authz.RoleAdmin)
			return err
		})
		members, err := e.m.Members.List(alice)
		if err != nil {
			t.Fatal(err)
		}
		owner, demoted, owners := alice, "", 0
		for _, m := range members {
			if m.Role == authz.RoleOwner {
				owners++
				if m.UserID == "alice" {
					owner, demoted = alice, "bob"
				} else {
					owner, demoted = bob, "alice"
				}
			}
		}
		if owners != 1 {
			t.Fatalf("round %d: %d owners left (errs %v)", round, owners, errs)
		}
		if _, err := e.m.Members.UpdateRole(owner, demoted, authz.RoleOwner); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConcurrentAddSameMember(t *testing.T) {
	e := newEnv(t)
	_, owner := e.setupTenant(t)
	e.login(t, "max", "")
	errs := parallel(8, func(int) error {
		_, err := e.m.Members.Add(owner, service.MemberRef{UserID: "max"}, authz.RoleMember)
		return err
	})
	ok := count(errs, func(err error) bool { return err == nil })
	conflicts := count(errs, func(err error) bool { return errors.Is(err, apperr.ErrConflict) })
	if ok != 1 || conflicts != 7 {
		t.Fatalf("ok=%d conflicts=%d errs=%v", ok, conflicts, errs)
	}
}

func TestConcurrentDevLoginCreatesUserOnce(t *testing.T) {
	e := newEnv(t)
	errs := parallel(8, func(int) error {
		_, err := e.m.Tokens.DevLogin(context.Background(), "newcomer", "")
		return err
	})
	if n := count(errs, func(err error) bool { return err != nil }); n != 0 {
		t.Fatalf("%d logins failed: %v", n, errs)
	}
}

// Disabling a user stops sign-in, refresh and tenant tokens; enabling restores sign-in.
func TestDisabledUser(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tenantID, owner := e.setupTenant(t)
	max, _, _ := e.m.Auth.Register(ctx, "max@example.com", "correct horse battery")
	if _, err := e.m.Members.Add(owner, service.MemberRef{UserID: max.ID}, authz.RoleMember); err != nil {
		t.Fatal(err)
	}
	pair, _ := e.m.Auth.Login(ctx, "max@example.com", "correct horse battery", tenantID)

	if _, err := e.m.Admin.SetUserDisabled(ctx, max.ID, true); err != nil {
		t.Fatal(err)
	}
	_, err := e.m.Auth.Login(ctx, "max@example.com", "correct horse battery", "")
	expect(t, "login", err, apperr.ErrForbidden)
	_, err = e.m.Tokens.Refresh(ctx, pair.RefreshToken)
	expect(t, "refresh", err, apperr.ErrUnauthenticated)
	if v := e.cachedVersion(t, tenantID, max.ID); v != 2 {
		t.Fatalf("tenant token not made stale: version %d", v)
	}

	if _, err := e.m.Admin.SetUserDisabled(ctx, max.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Auth.Login(ctx, "max@example.com", "correct horse battery", ""); err != nil {
		t.Fatalf("login after enable: %v", err)
	}
}
