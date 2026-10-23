package authz

import (
	"context"
	"fmt"

	"core/apperr"
	"core/reqctx"
)

// RelationChecker asks a relationship-based authorization engine (e.g. the
// embedded core/authz/rebac, whose Engine.Authorizer wires this) whether
// subject has relation on object. Strings use the "type:id" /
// "type:id#relation" notation.
type RelationChecker interface {
	Check(ctx context.Context, object, relation, subject string, c Consistency) (bool, error)
}

// CheckFunc adapts a function to RelationChecker.
type CheckFunc func(ctx context.Context, object, relation, subject string, c Consistency) (bool, error)

func (f CheckFunc) Check(ctx context.Context, object, relation, subject string, c Consistency) (bool, error) {
	return f(ctx, object, relation, subject, c)
}

// Consistency is how fresh the relationships behind a decision must be
// (the engine's Check consistency).
type Consistency int

const (
	// MinimizeLatency may answer from the engine's cache: a change made
	// on another replica is seen within about a second.
	MinimizeLatency Consistency = iota
	// Fresh sees every relationship change the engine has committed.
	// Costs a database read and a cold cache on that request.
	Fresh
)

// DefaultFreshActions: actions that change data must not run on a revoked
// permission, even briefly. Reads take the cache.
var DefaultFreshActions = []string{Create, Update, Delete}

type freshKey struct{}

// WithFresh makes every ReBAC check made with ctx use Fresh, for a read that
// must not lag behind a permission change (e.g. exporting data, or reading
// right after revoking access).
func WithFresh(ctx context.Context) context.Context {
	return context.WithValue(ctx, freshKey{}, true)
}

func isFresh(ctx context.Context) bool {
	fresh, _ := ctx.Value(freshKey{}).(bool)
	return fresh
}

// RelationMapper turns an Authorizer request into the relation to check.
type RelationMapper func(action string, res Resource) (object, relation string)

// DefaultRelations is the kit's naming convention between Check(action, resource)
// and the authorization model (core/authz/rebac/base.yml + the service's authz/*.yml):
//
//	object-level  Check(Update, {Type: "note", ID: "42"})      → note:42#can_update
//	type-level    Check(Create, {Type: "note", TenantID: "t"}) → tenant:t#can_create_note
//
// Type-level checks (create, list) ask the tenant, since the object doesn't exist yet.
func DefaultRelations(action string, res Resource) (object, relation string) {
	if res.ID == "" {
		return "tenant:" + res.TenantID, "can_" + action + "_" + res.Type
	}
	return res.Type + ":" + res.ID, "can_" + action
}

type rebac struct {
	checker   RelationChecker
	batch     BatchCheckFunc // nil: CheckMany asks one by one
	relations RelationMapper
	fresh     map[string]bool
}

type ReBACOption func(*rebac)

// WithRelations overrides DefaultRelations.
func WithRelations(m RelationMapper) ReBACOption { return func(r *rebac) { r.relations = m } }

// WithFreshActions replaces DefaultFreshActions: the actions checked with Fresh.
// Pass none to let every check use the cache (WithFresh still applies).
func WithFreshActions(actions ...string) ReBACOption {
	return func(r *rebac) { r.fresh = actionSet(actions) }
}

func actionSet(actions []string) map[string]bool {
	set := make(map[string]bool, len(actions))
	for _, a := range actions {
		set[a] = true
	}
	return set
}

// NewReBAC returns an Authorizer that delegates decisions to a ReBAC
// engine. The caller (user) comes from ctx, errors
// wrap apperr sentinels, and a failing engine denies (fail closed).
//
// Unlike RBAC it ignores Resource.OwnerID: ownership is a tuple (note:42#owner@user:u)
// and the model decides what owners may do.
//
// Actions in DefaultFreshActions (create, update, delete) are checked with
// Fresh, reads with MinimizeLatency; see WithFreshActions and WithFresh.
// Fresh only covers what the engine has: relationships still waiting in
// a service's outbox are not seen by either.
func NewReBAC(checker RelationChecker, opts ...ReBACOption) Authorizer {
	r := &rebac{checker: checker, relations: DefaultRelations, fresh: actionSet(DefaultFreshActions)}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

func (r *rebac) Check(ctx context.Context, action string, res Resource) error {
	q, err := r.prepare(ctx, action, res)
	if err != nil {
		return err
	}
	consistency := MinimizeLatency
	if q.fresh {
		consistency = Fresh
	}
	allowed, err := r.checker.Check(ctx, q.Object, q.Relation, q.Subject, consistency)
	return q.decide(allowed, err)
}

// query is one Check turned into a relation check.
type query struct {
	RelationCheck
	action, resType string
	fresh           bool
}

// Guard runs the checks every authorizer makes before deciding: a caller, a
// selected tenant, and a resource of that tenant (another tenant's resource
// looks missing: defense in depth, repositories and row-level security already
// scope reads to the caller's tenant). It returns res with TenantID set.
func Guard(ctx context.Context, res Resource) (reqctx.Identity, Resource, error) {
	id, ok := reqctx.IdentityFrom(ctx)
	if !ok || id.UserID == "" {
		return id, res, fmt.Errorf("%w: no caller identity", apperr.ErrUnauthenticated)
	}
	if id.TenantID == "" {
		return id, res, fmt.Errorf("%w: no tenant selected", apperr.ErrForbidden)
	}
	if res.TenantID != "" && res.TenantID != id.TenantID {
		return id, res, apperr.ErrNotFound
	}
	if res.TenantID == "" {
		res.TenantID = id.TenantID
	}
	return id, res, nil
}

// prepare runs the local guards (identity, tenant) and maps the request to a relation.
func (r *rebac) prepare(ctx context.Context, action string, res Resource) (query, error) {
	id, res, err := Guard(ctx, res)
	if err != nil {
		return query{}, err
	}

	object, relation := r.relations(action, res)
	return query{
		RelationCheck: RelationCheck{Object: object, Relation: relation, Subject: "user:" + id.UserID},
		action:        action,
		resType:       res.Type,
		fresh:         r.fresh[action] || isFresh(ctx),
	}, nil
}

// decide turns the engine's answer into Check's error.
func (q query) decide(allowed bool, err error) error {
	if err != nil {
		return fmt.Errorf("authz check %s#%s: %w", q.Object, q.Relation, err)
	}
	if !allowed {
		return fmt.Errorf("%w: cannot %s %s", apperr.ErrForbidden, q.action, q.resType)
	}
	return nil
}
