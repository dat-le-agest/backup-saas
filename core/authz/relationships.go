package authz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"core/outbox"
	"core/reqctx"
)

// AppFrom is the app whose store a check or a relationship change goes to: the
// app of the caller's access token (set by every gateway). Adapters over the
// generated authz client pass it as the request's app.
func AppFrom(ctx context.Context) (string, error) {
	id, _ := reqctx.IdentityFrom(ctx)
	if id.AppID == "" {
		return "", errNoApp
	}
	return id.AppID, nil
}

var errNoApp = errors.New("authz: no app in the caller identity")

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
// a note, which tenant it belongs to...) for the ReBAC
// service. Call it inside the unit of work that changes the data.
type Relationships interface {
	Write(ctx context.Context, tuples ...Tuple) error
	Delete(ctx context.Context, tuples ...Tuple) error
	// Change applies deletes and writes atomically (one
	// message), e.g. a role change: no moment with both roles or with none.
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

// TuplesTopic is the outbox topic of relationship changes.
const TuplesTopic = "authz.tuples"

// TupleChanges is the payload of a TuplesTopic message. App is the store they
// go to, taken from the caller when the change was made.
type TupleChanges struct {
	App           string   `json:"app"`
	Writes        []Tuple  `json:"writes,omitempty"`
	Deletes       []Tuple  `json:"deletes,omitempty"`
	DeleteObjects []string `json:"delete_objects,omitempty"`
}

// outboxRelationships writes changes to the outbox, in the caller's transaction:
// they reach a remote engine if and only if the data change commits.
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
	app, err := AppFrom(ctx)
	if err != nil {
		return err
	}
	c.App = app
	return r.outbox.Publish(ctx, TuplesTopic, key, c)
}

// WriteTuplesFunc applies changes on a remote engine in one call (adapter
// over the generated client, like CheckFunc). It must be idempotent, as the
// engine's WriteTuples is.
type WriteTuplesFunc func(ctx context.Context, c TupleChanges) error

// DeliverTuples is the outbox handler of TuplesTopic.
func DeliverTuples(write WriteTuplesFunc) outbox.Handler {
	return func(ctx context.Context, m outbox.Message) error {
		var c TupleChanges
		if err := json.Unmarshal(m.Payload, &c); err != nil {
			return fmt.Errorf("decode tuple changes: %w", err)
		}
		if c.App == "" {
			return fmt.Errorf("tuple changes of message %d have no app", m.ID)
		}
		return write(ctx, c)
	}
}
