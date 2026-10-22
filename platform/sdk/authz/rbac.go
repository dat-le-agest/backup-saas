package authz

import (
	"context"
	"fmt"

	"core/apperr"
	"core/reqctx"
)

// Scope tells how far a granted permission reaches.
type Scope int

const (
	None Scope = iota
	// Own: only resources whose OwnerID is the caller.
	Own
	// Any: every resource of the type in the caller's tenant.
	Any
)

// Policy maps role → permission (Perm(type, action)) → scope.
// Missing roles or permissions mean None: the policy is deny by default.
type Policy map[string]map[string]Scope

type rbac struct {
	policy Policy
}

// NewRBAC returns an Authorizer backed by a static role → permission policy.
// No storage involved: the caller's role comes from the identity in ctx.
func NewRBAC(policy Policy) Authorizer {
	return &rbac{policy: policy}
}

func (a *rbac) Check(ctx context.Context, action string, res Resource) error {
	id, ok := reqctx.IdentityFrom(ctx)
	if !ok || id.UserID == "" {
		return fmt.Errorf("%w: no caller identity", apperr.ErrUnauthenticated)
	}
	if id.TenantID == "" {
		return fmt.Errorf("%w: no tenant selected", apperr.ErrForbidden)
	}

	// Defense in depth: repositories already filter by tenant. Report another
	// tenant's resource as missing so its existence isn't revealed.
	if res.TenantID != "" && res.TenantID != id.TenantID {
		return apperr.ErrNotFound
	}

	perm := Perm(res.Type, action)
	switch a.policy[id.Role][perm] {
	case Any:
		return nil
	case Own:
		if res.OwnerID != "" && res.OwnerID == id.UserID {
			return nil
		}
		return fmt.Errorf("%w: role %q can only %s own %s", apperr.ErrForbidden, id.Role, action, res.Type)
	default:
		return fmt.Errorf("%w: role %q cannot %s %s", apperr.ErrForbidden, id.Role, action, res.Type)
	}
}
