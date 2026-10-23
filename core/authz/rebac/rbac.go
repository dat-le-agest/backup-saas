package rebac

import (
	"context"
	"fmt"

	"core/apperr"
	"core/authz"
	"core/authz/rebac/engine"
)

// NewRBAC decides with the model alone, without stored relationships: each
// check evaluates the model over the relationships implied by the request,
//
//	tenant:<tenant>#<role>@user:<caller>   the caller's role, from their access token
//	<type>:<id>#tenant@tenant:<tenant>     the resource's tenant
//	<type>:<id>#owner@user:<owner>         its owner (Resource.OwnerID), if any
//
// so rules like "admin from tenant" or "owner and member from tenant" give role
// based permissions from the same file as ReBAC: the two can't disagree, ReBAC
// only adds what stored relationships grant (e.g. sharing). Object types name
// these relations "tenant" and "owner" (core/authz.DefaultRelations maps actions).
func NewRBAC(m *engine.Model) authz.Authorizer {
	return &modelRBAC{model: m}
}

type modelRBAC struct{ model *engine.Model }

func (r *modelRBAC) Check(ctx context.Context, action string, res authz.Resource) error {
	id, res, err := authz.Guard(ctx, res)
	if err != nil {
		return err
	}
	object, relation := authz.DefaultRelations(action, res)
	obj, err := engine.ParseObject("object", object)
	if err != nil {
		return err
	}
	if !r.model.HasRelation(obj.Type, relation) {
		// the model says nothing about this action: nobody may
		return fmt.Errorf("%w: cannot %s %s", apperr.ErrForbidden, action, res.Type)
	}
	user := engine.Subject{Type: "user", ID: id.UserID}

	var implied []engine.Tuple
	tenant := engine.Object{Type: "tenant", ID: res.TenantID}
	if id.Role != "" {
		implied = append(implied, engine.Tuple{Object: tenant, Relation: id.Role, Subject: user, TenantID: res.TenantID})
	}
	if res.ID != "" {
		implied = append(implied, engine.Tuple{Object: obj, Relation: "tenant", Subject: engine.Subject{Type: "tenant", ID: res.TenantID}, TenantID: res.TenantID})
		if res.OwnerID != "" {
			implied = append(implied, engine.Tuple{Object: obj, Relation: "owner", Subject: engine.Subject{Type: "user", ID: res.OwnerID}, TenantID: res.TenantID})
		}
	}

	allowed, err := engine.NewChecker(r.model, engine.NewMemReader(implied...)).Check(ctx, obj, relation, user)
	if err != nil {
		return fmt.Errorf("authz check %s#%s: %w", object, relation, err)
	}
	if !allowed {
		return fmt.Errorf("%w: role %q cannot %s %s", apperr.ErrForbidden, id.Role, action, res.Type)
	}
	return nil
}
