// Package authz decides whether the caller (identity in ctx) may perform an
// action on a resource, inside the caller's tenant.
//
// Services call it after loading the resource through a tenant-scoped repository:
//
//	note, err := repo.Get(ctx, id) // already filtered by tenant (core/tenancy)
//	err = az.Check(ctx, authz.Update, authz.Resource{Type: "note", ID: "42", TenantID: note.TenantID, OwnerID: note.CreatedBy})
//
// The interface is shaped like Zanzibar (subject from ctx, action, typed resource)
// so RBAC (core/authz/rebac.NewRBAC) and ReBAC (the embedded engine) are
// interchangeable without touching service code. Tenant isolation does NOT rely on this package:
// repositories must filter by tenant regardless of the backend.
package authz

import "context"

// Default tenant roles (base model of core/authz/rebac).
const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
	RoleViewer = "viewer"
)

// Roles lists the default roles from most to least privileged.
var Roles = []string{RoleOwner, RoleAdmin, RoleMember, RoleViewer}

func IsDefaultRole(role string) bool {
	for _, r := range Roles {
		if r == role {
			return true
		}
	}
	return false
}

// Standard actions. Listing uses Read on a type-level resource (empty ID).
const (
	Create = "create"
	Read   = "read"
	Update = "update"
	Delete = "delete"
)

// Resource identifies what is accessed.
//
// Type and ID identify it (what a ReBAC backend needs). TenantID and OwnerID are
// attributes used by the RBAC backend. Leave ID empty for type-level checks:
// create (set OwnerID to the caller) and list.
type Resource struct {
	Type     string
	ID       string
	TenantID string
	OwnerID  string
}

type Authorizer interface {
	// Check returns nil if allowed, otherwise an error wrapping
	// apperr.ErrUnauthenticated, apperr.ErrForbidden or apperr.ErrNotFound.
	Check(ctx context.Context, action string, res Resource) error
}
