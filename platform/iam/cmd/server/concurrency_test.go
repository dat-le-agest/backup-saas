package main

import (
	"context"
	"errors"
	"sync"
	"testing"

	iamv1 "api/gen/go/platform/iam/v1"
	"core/apperr"
	"core/authz"
)

// parallel runs fn n times at once and returns the errors.
func parallel(n int, fn func(i int) error) []error {
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = fn(i)
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

// The same refresh token presented N times at once: exactly one exchange wins.
func TestConcurrentRefreshIsSingleUse(t *testing.T) {
	e := newEnv(t, true)
	pair := e.login(t, "alice", "")

	errs := parallel(10, func(int) error {
		_, err := e.client.RefreshTokens(context.Background(), &iamv1.RefreshTokensRequest{RefreshToken: pair.GetRefreshToken()})
		return err
	})
	ok := count(errs, func(err error) bool { return err == nil })
	rejected := count(errs, func(err error) bool { return errors.Is(err, apperr.ErrUnauthenticated) })
	if ok != 1 || rejected != 9 {
		t.Fatalf("ok=%d rejected=%d errs=%v", ok, rejected, errs)
	}
}

// A parallel brute force must be fully counted (no lost updates) and lock the email.
func TestConcurrentFailedLoginsAllCount(t *testing.T) {
	e := newEnv(t, false)
	const pw = "correct horse battery"
	e.register(t, "alice@example.com", pw)

	errs := parallel(20, func(int) error {
		_, err := e.passwordLogin("alice@example.com", "wrong password!", "")
		return err
	})
	if n := count(errs, func(err error) bool { return err == nil }); n != 0 {
		t.Fatalf("%d wrong passwords accepted", n)
	}

	_, err := e.passwordLogin("alice@example.com", pw, "")
	expect(t, "locked after parallel attempts", err, apperr.ErrTooManyRequests)
}

// Two owners demoting each other at the same time: one must be refused so the
// tenant keeps an owner.
func TestConcurrentDemotionKeepsAnOwner(t *testing.T) {
	e := newEnv(t, true)
	tenantID, alice := e.setupTenant(t)
	e.login(t, "bob", "")
	if _, err := e.client.AddMember(alice, &iamv1.AddMemberRequest{UserId: "bob", Role: authz.RoleOwner}); err != nil {
		t.Fatal(err)
	}
	bob := e.as(t, e.login(t, "bob", tenantID).GetAccessToken())

	for round := 0; round < 5; round++ {
		ctxs := []context.Context{alice, bob}
		targets := []string{"bob", "alice"}
		errs := parallel(2, func(i int) error {
			_, err := e.client.UpdateMemberRole(ctxs[i], &iamv1.UpdateMemberRoleRequest{UserId: targets[i], Role: authz.RoleAdmin})
			return err
		})

		members, err := e.client.ListMembers(alice, &iamv1.ListMembersRequest{})
		if err != nil {
			// alice was demoted and her (now stale) role is re-read from the DB: list still works
			t.Fatal(err)
		}
		owners := 0
		for _, m := range members.GetMembers() {
			if m.GetRole() == authz.RoleOwner {
				owners++
			}
		}
		if owners != 1 {
			t.Fatalf("round %d: %d owners left (errs %v)", round, owners, errs)
		}

		// restore two owners for the next round
		var owner context.Context
		var demoted string
		for _, m := range members.GetMembers() {
			if m.GetRole() == authz.RoleOwner {
				if m.GetUserId() == "alice" {
					owner, demoted = alice, "bob"
				} else {
					owner, demoted = bob, "alice"
				}
			}
		}
		if _, err := e.client.UpdateMemberRole(owner, &iamv1.UpdateMemberRoleRequest{UserId: demoted, Role: authz.RoleOwner}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConcurrentAddSameMember(t *testing.T) {
	e := newEnv(t, true)
	_, owner := e.setupTenant(t)
	e.login(t, "max", "")

	errs := parallel(8, func(int) error {
		_, err := e.client.AddMember(owner, &iamv1.AddMemberRequest{UserId: "max", Role: authz.RoleMember})
		return err
	})
	ok := count(errs, func(err error) bool { return err == nil })
	conflicts := count(errs, func(err error) bool { return errors.Is(err, apperr.ErrConflict) })
	if ok != 1 || conflicts != 7 {
		t.Fatalf("ok=%d conflicts=%d errs=%v", ok, conflicts, errs)
	}
}

func TestConcurrentDevLoginCreatesUserOnce(t *testing.T) {
	e := newEnv(t, true)
	errs := parallel(8, func(int) error {
		_, err := e.client.DevLogin(context.Background(), &iamv1.DevLoginRequest{UserId: "newcomer"})
		return err
	})
	if n := count(errs, func(err error) bool { return err != nil }); n != 0 {
		t.Fatalf("%d logins failed: %v", n, errs)
	}
}
