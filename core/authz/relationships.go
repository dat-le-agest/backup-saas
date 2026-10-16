package authz

import (
	"context"
	"encoding/json"
	"fmt"

	"core/outbox"
)

// Tuple is a relationship for the central authz service, in its notation:
// object "note:42", relation "owner", subject "user:usr_1" or "team:eng#member".
type Tuple struct {
	Object   string `json:"object"`
	Relation string `json:"relation"`
	Subject  string `json:"subject"`
	// tenant owning the object
	TenantID string `json:"tenant_id"`
}

// Relationships records relationship changes of a service's own data (who owns
// a note, which tenant it belongs to, a member's role...) for the central authz
// service. Call it inside the unit of work that changes the data.
type Relationships interface {
	Write(ctx context.Context, tuples ...Tuple) error
	Delete(ctx context.Context, tuples ...Tuple) error
	// Change applies deletes and writes atomically on the authz service (one
	// message), e.g. a role change: no moment with both roles or with none.
	Change(ctx context.Context, writes, deletes []Tuple) error
	// DeleteObjects forgets objects that no longer exist ("note:8"): every tuple
	// on them (owner, tenant, shares...) and every tuple naming them as subject.
	DeleteObjects(ctx context.Context, objects ...string) error
}

// NoRelationships is used when the service doesn't sync with the authz service.
type NoRelationships struct{}

func (NoRelationships) Write(context.Context, ...Tuple) error          { return nil }
func (NoRelationships) Delete(context.Context, ...Tuple) error         { return nil }
func (NoRelationships) Change(context.Context, []Tuple, []Tuple) error { return nil }
func (NoRelationships) DeleteObjects(context.Context, ...string) error { return nil }

// TuplesTopic is the outbox topic of relationship changes.
const TuplesTopic = "authz.tuples"

// TupleChanges is the payload of a TuplesTopic message.
type TupleChanges struct {
	Writes        []Tuple  `json:"writes,omitempty"`
	Deletes       []Tuple  `json:"deletes,omitempty"`
	DeleteObjects []string `json:"delete_objects,omitempty"`
}

// outboxRelationships writes changes to the outbox, in the caller's transaction:
// they reach the authz service if and only if the data change commits.
type outboxRelationships struct {
	outbox *outbox.Outbox
}

func NewOutboxRelationships(o *outbox.Outbox) Relationships {
	return &outboxRelationships{outbox: o}
}

func (r *outboxRelationships) Write(ctx context.Context, tuples ...Tuple) error {
	return r.publish(ctx, TupleChanges{Writes: tuples})
}

func (r *outboxRelationships) Delete(ctx context.Context, tuples ...Tuple) error {
	return r.publish(ctx, TupleChanges{Deletes: tuples})
}

func (r *outboxRelationships) Change(ctx context.Context, writes, deletes []Tuple) error {
	return r.publish(ctx, TupleChanges{Writes: writes, Deletes: deletes})
}

func (r *outboxRelationships) DeleteObjects(ctx context.Context, objects ...string) error {
	return r.publish(ctx, TupleChanges{DeleteObjects: objects})
}

func (r *outboxRelationships) publish(ctx context.Context, c TupleChanges) error {
	var key string
	switch {
	case len(c.Writes) > 0:
		key = c.Writes[0].Object
	case len(c.Deletes) > 0:
		key = c.Deletes[0].Object
	case len(c.DeleteObjects) > 0:
		key = c.DeleteObjects[0]
	default:
		return nil
	}
	return r.outbox.Publish(ctx, TuplesTopic, key, c)
}

// WriteTuplesFunc applies changes on the authz service in one call (adapter
// over the generated client, like CheckFunc). It must be idempotent, as the
// authz service's WriteTuples is.
type WriteTuplesFunc func(ctx context.Context, c TupleChanges) error

// DeliverTuples is the outbox handler of TuplesTopic.
func DeliverTuples(write WriteTuplesFunc) outbox.Handler {
	return func(ctx context.Context, m outbox.Message) error {
		var c TupleChanges
		if err := json.Unmarshal(m.Payload, &c); err != nil {
			return fmt.Errorf("decode tuple changes: %w", err)
		}
		return write(ctx, c)
	}
}
