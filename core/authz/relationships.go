package authz

import "context"

// Tuple is a relationship for a ReBAC engine (core/authz/rebac), in its notation:
// object "note:42", relation "owner", subject "user:usr_1" or "team:eng#member".
type Tuple struct {
	Object   string `json:"object"`
	Relation string `json:"relation"`
	Subject  string `json:"subject"`
	// tenant owning the object
	TenantID string `json:"tenant_id"`
}

// Relationships records relationship changes of a service's own data (who owns
// a note, which tenant it belongs to...) for its ReBAC engine. Call it inside
// the unit of work that changes the data.
type Relationships interface {
	Write(ctx context.Context, tuples ...Tuple) error
	Delete(ctx context.Context, tuples ...Tuple) error
	// Change applies deletes and writes atomically, e.g. moving an object: no
	// moment with both parents or with none.
	Change(ctx context.Context, writes, deletes []Tuple) error
	// DeleteObjects forgets objects that no longer exist ("note:8"): every tuple
	// on them (owner, tenant, shares...) and every tuple naming them as subject.
	DeleteObjects(ctx context.Context, objects ...string) error
}

// NoRelationships is used when the service doesn't record relationships.
type NoRelationships struct{}

func (NoRelationships) Write(context.Context, ...Tuple) error          { return nil }
func (NoRelationships) Delete(context.Context, ...Tuple) error         { return nil }
func (NoRelationships) Change(context.Context, []Tuple, []Tuple) error { return nil }
func (NoRelationships) DeleteObjects(context.Context, ...string) error { return nil }
