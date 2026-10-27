package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"core/logger"
	"core/reqctx"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"gorm.io/gorm"
)

// Message is what handlers receive.
type Message struct {
	ID       int64
	Topic    string
	Key      string
	Payload  json.RawMessage
	Attempts int // previous failed attempts
}

// Handler delivers one message. It must be idempotent: a message can be
// delivered more than once (e.g. if the relay stops right after delivering).
// Return Permanent(err) when retrying can't help: the message is set aside at once.
type Handler func(ctx context.Context, msg Message) error

// ErrPermanent marks a delivery failure that retrying won't fix (see Permanent).
var ErrPermanent = errors.New("outbox: permanent failure")

// Permanent wraps err so the relay sets the message aside without retrying it.
func Permanent(err error) error {
	if errors.Is(err, ErrPermanent) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrPermanent, err)
}

// RelayOptions tune delivery. Zero values use the defaults.
type RelayOptions struct {
	PollInterval time.Duration // default 1s (commits also wake the relay)
	BatchSize    int           // default 50
	MaxAttempts  int           // default 25, then the message is set aside (failed_at)
	MaxBackoff   time.Duration // default 5m
	Timeout      time.Duration // per delivery, default 10s
	Retention    time.Duration // delivered messages are kept this long, default 7 days
}

// Relay delivers committed messages, in order, one replica at a time.
//
// Ordering: a transaction-scoped advisory lock lets a single relay work at a
// time, and messages are taken in id (commit) order. When a delivery fails the
// batch stops, so later messages never overtake it (head-of-line blocking is
// the price of order). After MaxAttempts the message is set aside (failed_at)
// and delivery continues; requeue it with:
//
//	UPDATE outbox_messages SET failed_at = NULL, attempts = 0, next_attempt_at = now() WHERE id = ...;
type Relay struct {
	db       *gorm.DB
	logger   logger.Logger
	notify   chan struct{}
	opts     RelayOptions
	handlers map[string]Handler
	// topics whose payload is emptied once delivered
	sensitive map[string]bool

	cancel context.CancelFunc
	done   sync.WaitGroup
}

// advisory lock id of the relay (per database)
const relayLockID int64 = 0x6f7574626f78 // "outbox"

func (o *Outbox) NewRelay(db *gorm.DB, l logger.Logger, opts RelayOptions) *Relay {
	if opts.PollInterval <= 0 {
		opts.PollInterval = time.Second
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 50
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 25
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = 5 * time.Minute
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	if opts.Retention <= 0 {
		opts.Retention = 7 * 24 * time.Hour
	}
	return &Relay{db: db, logger: l, notify: o.notify, opts: opts, handlers: map[string]Handler{}, sensitive: map[string]bool{}}
}

// Handle registers the handler of a topic. Messages of topics without a
// handler stay pending (another release may handle them).
func (r *Relay) Handle(topic string, h Handler) {
	r.handlers[topic] = h
}

// HandleSensitive is Handle for topics whose payload must not outlive its
// delivery (e.g. mail with a password reset link): the payload is replaced by
// {} when the message is delivered. Messages set aside keep it, to be retried.
// Hide it from the admin API too (WithSensitiveTopics).
func (r *Relay) HandleSensitive(topic string, h Handler) {
	r.handlers[topic] = h
	r.sensitive[topic] = true
}

// Start runs the relay in the background until Stop.
func (r *Relay) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.done.Add(1)
	go func() {
		defer r.done.Done()
		r.loop(ctx)
	}()
}

// Stop ends the relay and waits for the current batch.
func (r *Relay) Stop() {
	if r.cancel != nil {
		r.cancel()
		r.done.Wait()
	}
}

func (r *Relay) loop(ctx context.Context) {
	ticker := time.NewTicker(r.opts.PollInterval)
	defer ticker.Stop()
	lastCleanup := time.Time{}

	for {
		n, err := r.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			r.logger.Errorf("outbox relay: %v", err)
		}
		if time.Since(lastCleanup) > time.Hour {
			r.cleanup(ctx)
			lastCleanup = time.Now()
		}
		if n == r.opts.BatchSize {
			continue // more may be waiting
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-r.notify:
		}
	}
}

type row struct {
	ID       int64
	Topic    string
	Key      string
	Payload  string
	Headers  string
	Attempts int
	Due      bool
}

// RunOnce delivers one batch and returns how many messages were delivered.
// Exported for tests and for running the relay from a job.
func (r *Relay) RunOnce(ctx context.Context) (int, error) {
	if len(r.handlers) == 0 {
		return 0, nil
	}
	topics := make([]string, 0, len(r.handlers))
	for t := range r.handlers {
		topics = append(topics, t)
	}

	delivered := 0
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked bool
		if err := tx.Raw(`SELECT pg_try_advisory_xact_lock(?)`, relayLockID).Scan(&locked).Error; err != nil {
			return err
		}
		if !locked {
			return nil // another replica is relaying
		}

		var rows []row
		err := tx.Raw(`
			SELECT id, topic, key, payload::text AS payload, headers::text AS headers, attempts,
			       next_attempt_at <= now() AS due
			FROM outbox_messages
			WHERE processed_at IS NULL AND failed_at IS NULL AND topic IN ?
			ORDER BY id
			LIMIT ?`, topics, r.opts.BatchSize,
		).Scan(&rows).Error
		if err != nil {
			return err
		}

		for _, m := range rows {
			// a message waiting for its retry holds back everything after it (order)
			if !m.Due {
				return nil
			}

			if err := r.deliver(ctx, m); err != nil {
				return r.recordFailure(tx, m, err)
			}
			done := `UPDATE outbox_messages SET processed_at = now(), last_error = '' WHERE id = ?`
			if r.sensitive[m.Topic] {
				done = `UPDATE outbox_messages SET processed_at = now(), last_error = '', payload = '{}'::jsonb WHERE id = ?`
			}
			if err := tx.Exec(done, m.ID).Error; err != nil {
				return err
			}
			delivered++
		}
		return nil
	})
	return delivered, err
}

func (r *Relay) deliver(ctx context.Context, m row) error {
	headers := map[string]string{}
	_ = json.Unmarshal([]byte(m.Headers), &headers)

	ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(headers))
	if id := headers[headerRequestID]; id != "" {
		ctx = reqctx.WithRequestID(ctx, id)
	}
	if id := headers[headerUserID]; id != "" {
		ctx = reqctx.WithIdentity(ctx, reqctx.Identity{UserID: id})
	}
	ctx, cancel := context.WithTimeout(ctx, r.opts.Timeout)
	defer cancel()

	h := r.handlers[m.Topic]
	err := func() (err error) {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("handler panic: %v", p)
			}
		}()
		return h(ctx, Message{ID: m.ID, Topic: m.Topic, Key: m.Key, Payload: json.RawMessage(m.Payload), Attempts: m.Attempts})
	}()
	return err
}

func (r *Relay) recordFailure(tx *gorm.DB, m row, cause error) error {
	attempts := m.Attempts + 1
	entry := r.logger.WithFields(logger.Fields{"outbox_id": m.ID, "topic": m.Topic, "key": m.Key, "attempts": attempts})

	if attempts >= r.opts.MaxAttempts || errors.Is(cause, ErrPermanent) {
		entry.Errorf("outbox message set aside after %d attempts: %v", attempts, cause)
		return tx.Exec(`UPDATE outbox_messages SET attempts = ?, last_error = ?, failed_at = now() WHERE id = ?`,
			attempts, cause.Error(), m.ID).Error
	}

	backoff := time.Second << min(attempts-1, 20)
	if backoff > r.opts.MaxBackoff {
		backoff = r.opts.MaxBackoff
	}
	entry.Warnf("outbox delivery failed, retry in %s: %v", backoff, cause)
	return tx.Exec(`
		UPDATE outbox_messages
		SET attempts = ?, last_error = ?, next_attempt_at = now() + make_interval(secs => ?)
		WHERE id = ?`, attempts, cause.Error(), backoff.Seconds(), m.ID).Error
}

func (r *Relay) cleanup(ctx context.Context) {
	res := r.db.WithContext(ctx).Exec(
		`DELETE FROM outbox_messages WHERE processed_at < now() - make_interval(secs => ?)`, r.opts.Retention.Seconds())
	if res.Error != nil && ctx.Err() == nil {
		r.logger.Warnf("outbox cleanup: %v", res.Error)
	}
}
