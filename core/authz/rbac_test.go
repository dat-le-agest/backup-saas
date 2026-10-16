package authz

import (
	"context"
	"errors"
	"testing"

	"core/apperr"
	"core/reqctx"
)

var policy = Policy{
	RoleAdmin: {
		Perm("note", Create): Any, Perm("note", Read): Any,
		Perm("note", Update): Any, Perm("note", Delete): Any,
	},
	RoleMember: {
		Perm("note", Create): Own, Perm("note", Read): Any,
		Perm("note", Update): Own, Perm("note", Delete): Own,
	},
	RoleViewer: {
		Perm("note", Read): Any,
	},
}

func as(userID, tenantID, role string) context.Context {
	return reqctx.WithIdentity(context.Background(), reqctx.Identity{UserID: userID, TenantID: tenantID, Role: role})
}

func TestRBAC(t *testing.T) {
	az := NewRBAC(policy)
	mine := Resource{Type: "note", ID: "1", TenantID: "t1", OwnerID: "u1"}
	theirs := Resource{Type: "note", ID: "2", TenantID: "t1", OwnerID: "u2"}
	otherTenant := Resource{Type: "note", ID: "3", TenantID: "t2", OwnerID: "u1"}
	createAsMe := Resource{Type: "note", TenantID: "t1", OwnerID: "u1"}
	list := Resource{Type: "note", TenantID: "t1"}

	cases := []struct {
		name   string
		ctx    context.Context
		action string
		res    Resource
		want   error // nil = allowed
	}{
		{"admin updates others", as("u1", "t1", RoleAdmin), Update, theirs, nil},
		{"member updates own", as("u1", "t1", RoleMember), Update, mine, nil},
		{"member updates others", as("u1", "t1", RoleMember), Update, theirs, apperr.ErrForbidden},
		{"member deletes others", as("u1", "t1", RoleMember), Delete, theirs, apperr.ErrForbidden},
		{"member reads others", as("u1", "t1", RoleMember), Read, theirs, nil},
		{"member creates", as("u1", "t1", RoleMember), Create, createAsMe, nil},
		{"member lists", as("u1", "t1", RoleMember), Read, list, nil},
		{"viewer reads", as("u1", "t1", RoleViewer), Read, theirs, nil},
		{"viewer creates", as("u1", "t1", RoleViewer), Create, createAsMe, apperr.ErrForbidden},
		{"viewer updates own", as("u1", "t1", RoleViewer), Update, mine, apperr.ErrForbidden},
		{"unknown role", as("u1", "t1", "superuser"), Read, mine, apperr.ErrForbidden},
		{"no role", as("u1", "t1", ""), Read, mine, apperr.ErrForbidden},
		{"unknown permission", as("u1", "t1", RoleAdmin), "archive", mine, apperr.ErrForbidden},
		{"other tenant looks missing", as("u1", "t1", RoleAdmin), Read, otherTenant, apperr.ErrNotFound},
		{"no tenant", as("u1", "", RoleAdmin), Read, mine, apperr.ErrForbidden},
		{"anonymous", context.Background(), Read, mine, apperr.ErrUnauthenticated},
		// Own on a type-level resource without OwnerID must not grant access to everything
		{"own scope needs owner", as("u1", "t1", RoleMember), Update, Resource{Type: "note", TenantID: "t1"}, apperr.ErrForbidden},
	}
	for _, tc := range cases {
		err := az.Check(tc.ctx, tc.action, tc.res)
		if tc.want == nil && err != nil {
			t.Errorf("%s: expected allowed, got %v", tc.name, err)
		}
		if tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("%s: expected %v, got %v", tc.name, tc.want, err)
		}
	}
}
