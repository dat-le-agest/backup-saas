// Package outbox delivers messages reliably after a database commit
// (transactional outbox).
//
// A use case writes its data and its messages in the same transaction, so a
// message exists if and only if the data change committed:
//
//	err := s.uow.Do(ctx, func(ctx context.Context) error {
//		if err := s.repo.Create(ctx, note); err != nil { return err }
//		return s.outbox.Publish(ctx, "authz.tuples", note.ID, payload)
//	})
//
// A Relay then delivers committed messages to handlers, at least once and in
// order. Handlers must be idempotent.
//
// Each service needs the outbox_messages table in its own database: copy
// MigrationSQL into a goose migration (templates/service already has it).
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"core/reqctx"
	"core/uow"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// MigrationSQL creates the outbox table (goose format).
const MigrationSQL = `-- +goose Up
CREATE TABLE outbox_messages (
    id              bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    topic           text        NOT NULL,
    key             text        NOT NULL DEFAULT '',
    payload         jsonb       NOT NULL,
    headers         jsonb       NOT NULL DEFAULT '{}',
    attempts        integer     NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    last_error      text        NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    processed_at    timestamptz,
    failed_at       timestamptz
);
-- pending messages, in delivery order
CREATE INDEX outbox_messages_pending ON outbox_messages (id)
    WHERE processed_at IS NULL AND failed_at IS NULL;
-- cleanup of delivered messages
CREATE INDEX outbox_messages_processed_at ON outbox_messages (processed_at)
    WHERE processed_at IS NOT NULL;

-- +goose Down
DROP TABLE outbox_messages;
`

// ErrNotInTransaction: Publish was called outside a unit of work, where the
// message would not be atomic with the data change.
var ErrNotInTransaction = errors.New("outbox: Publish must run inside a unit of work (uow.Do)")

// Outbox writes messages in the caller's transaction.
type Outbox struct {
	uow    *uow.Gorm
	notify chan struct{}
}

func New(u *uow.Gorm) *Outbox {
	return &Outbox{uow: u, notify: make(chan struct{}, 1)}
}

// Publish stores a message for topic in the transaction of ctx. key groups
// related messages (e.g. an object id) for logs and debugging; delivery order
// is the commit order of the whole outbox. payload is JSON-encoded.
//
// The request id, the caller's user id and the trace context are kept with
// the message, so the delivery shows up in the same trace.
func (o *Outbox) Publish(ctx context.Context, topic string, key any, payload any) error {
	if !uow.InTx(ctx) {
		return ErrNotInTransaction
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("outbox: encode %s payload: %w", topic, err)
	}

	headers := map[string]string{}
	otel.GetTextMapPropagator().Inject(ctx, propagation.MapCarrier(headers))
	if id := reqctx.RequestIDFrom(ctx); id != "" {
		headers[headerRequestID] = id
	}
	if id, ok := reqctx.IdentityFrom(ctx); ok && id.UserID != "" {
		headers[headerUserID] = id.UserID
	}
	rawHeaders, _ := json.Marshal(headers)

	err = o.uow.DB(ctx).Exec(
		`INSERT INTO outbox_messages (topic, key, payload, headers) VALUES (?, ?, ?::jsonb, ?::jsonb)`,
		topic, keyString(key), string(body), string(rawHeaders),
	).Error
	if err != nil {
		return fmt.Errorf("outbox: store %s message: %w", topic, err)
	}

	// wake the relay as soon as the data is visible, instead of waiting for its poll
	uow.AfterCommit(ctx, func(context.Context) {
		select {
		case o.notify <- struct{}{}:
		default:
		}
	})
	return nil
}

const (
	headerRequestID = "x-request-id"
	headerUserID    = "x-user-id"
)

func keyString(key any) string {
	switch k := key.(type) {
	case string:
		return k
	case fmt.Stringer:
		return k.String()
	case int:
		return strconv.Itoa(k)
	case int64:
		return strconv.FormatInt(k, 10)
	case uint:
		return strconv.FormatUint(uint64(k), 10)
	case uint64:
		return strconv.FormatUint(k, 10)
	case nil:
		return ""
	}
	return fmt.Sprint(key)
}
