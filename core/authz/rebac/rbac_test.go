package rebac

import (
	"context"
	"errors"
	"os"
	"testing"

	"core/apperr"
	"core/authz"
	"core/reqctx"
)

// The note service's model decides RBAC from the request alone:
//
//	owner, admin  everything
//	member        create, read all, update/delete own
//	viewer        read all
func TestRBACFromModel(t *testing.T) {
	m, err := LoadModel(os.DirFS("../../../apps/notes/services/note/authz"))
	if err != nil {
		t.Fatal(err)
	}
	az := NewRBAC(m)
	as := func(role string) context.Context {
		return reqctx.WithIdentity(context.Background(), reqctx.Identity{UserID: "u1", TenantID: "t1", Role: role})
	}
	own := authz.Resource{Type: "note", ID: "1", TenantID: "t1", OwnerID: "u1"}
	other := authz.Resource{Type: "note", ID: "2", TenantID: "t1", OwnerID: "u2"}
	create := authz.Resource{Type: "note", TenantID: "t1", OwnerID: "u1"}
	list := authz.Resource{Type: "note", TenantID: "t1"}

	cases := []struct {
		role, action string
		res          authz.Resource
		allowed      bool
	}{
		{"owner", authz.Update, other, true}, {"owner", authz.Delete, other, true}, {"owner", authz.Create, create, true},
		{"admin", authz.Update, other, true}, {"admin", authz.Delete, other, true}, {"admin", authz.Create, create, true},
		{"member", authz.Create, create, true}, {"member", authz.Read, other, true}, {"member", authz.Read, list, true},
		{"member", authz.Update, own, true}, {"member", authz.Delete, own, true},
		{"member", authz.Update, other, false}, {"member", authz.Delete, other, false},
		{"viewer", authz.Read, other, true}, {"viewer", authz.Read, list, true},
		{"viewer", authz.Create, create, false}, {"viewer", authz.Update, own, false},
		{"", authz.Read, other, false},    // no role in the tenant
		{"member", "archive", own, false}, // an action the model doesn't define
	}
	for _, tc := range cases {
		err := az.Check(as(tc.role), tc.action, tc.res)
		if got := err == nil; got != tc.allowed {
			t.Errorf("%s %s %+v: allowed=%v err=%v", tc.role, tc.action, tc.res, got, err)
		}
		if err != nil && !errors.Is(err, apperr.ErrForbidden) {
			t.Errorf("%s %s: %v is not a denial", tc.role, tc.action, err)
		}
	}

	// the base model's member management (core/authn)
	for role, want := range map[string]bool{"owner": true, "admin": true, "member": false, "viewer": false} {
		err := az.Check(as(role), authz.Update, authz.Resource{Type: "member", TenantID: "t1"})
		if (err == nil) != want {
			t.Errorf("%s updates members: %v", role, err)
		}
		if err := az.Check(as(role), authz.Read, authz.Resource{Type: "member", TenantID: "t1"}); err != nil {
			t.Errorf("%s lists members: %v", role, err)
		}
	}

	// the usual guards
	if err := az.Check(context.Background(), authz.Read, list); !errors.Is(err, apperr.ErrUnauthenticated) {
		t.Errorf("anonymous: %v", err)
	}
	if err := az.Check(as("owner"), authz.Read, authz.Resource{Type: "note", ID: "9", TenantID: "t2"}); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("other tenant: %v", err)
	}
}
