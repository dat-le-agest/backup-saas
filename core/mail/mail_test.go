package mail_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"core/logger"
	"core/mail"
	"core/mail/memory"
	"core/outbox"
	"core/testdb"
	"core/uow"
)

type nopLogger struct{}

func (nopLogger) Debug(...interface{})                     {}
func (nopLogger) Info(...interface{})                      {}
func (nopLogger) Warn(...interface{})                      {}
func (nopLogger) Error(...interface{})                     {}
func (nopLogger) Debugf(string, ...interface{})            {}
func (nopLogger) Infof(string, ...interface{})             {}
func (nopLogger) Warnf(string, ...interface{})             {}
func (nopLogger) Errorf(string, ...interface{})            {}
func (l nopLogger) With(string, interface{}) logger.Logger { return l }
func (l nopLogger) WithFields(logger.Fields) logger.Logger { return l }

func TestValidate(t *testing.T) {
	ok := mail.Message{To: "ana@example.com", Subject: "Hi", Text: "body"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(m *mail.Message){
		"no recipient":        func(m *mail.Message) { m.To = "" },
		"two recipients":      func(m *mail.Message) { m.To = "a@x.y, b@x.y" },
		"invalid from":        func(m *mail.Message) { m.From = "nope" },
		"no subject":          func(m *mail.Message) { m.Subject = " " },
		"no body":             func(m *mail.Message) { m.Text = "" },
		"subject injection":   func(m *mail.Message) { m.Subject = "Hi\r\nBcc: evil@x.y" },
		"recipient injection": func(m *mail.Message) { m.To = "ana@example.com\r\nBcc: evil@x.y" },
		"header injection":    func(m *mail.Message) { m.Headers = map[string]string{"X-A": "1\r\nBcc: evil@x.y"} },
		"header name":         func(m *mail.Message) { m.Headers = map[string]string{"Bcc: evil@x.y\r\nX": "1"} },
	}
	for name, change := range bad {
		m := ok
		change(&m)
		if err := m.Validate(); !errors.Is(err, mail.ErrPermanent) {
			t.Errorf("%s: expected ErrPermanent, got %v", name, err)
		}
	}
}

func TestMaskAddress(t *testing.T) {
	for in, want := range map[string]string{
		"ana@example.com":       "a***@example.com",
		"Ana <ana@example.com>": "a***@example.com",
		"nope":                  "***",
	} {
		if got := mail.MaskAddress(in); got != want {
			t.Errorf("%s: got %s", in, got)
		}
	}
}

func setup(t *testing.T) (*uow.Gorm, *outbox.Outbox, *outbox.Relay, *memory.Sender) {
	t.Helper()
	db := testdb.New(t)
	up := outbox.MigrationSQL[:strings.Index(outbox.MigrationSQL, "-- +goose Down")]
	if err := db.Exec(up).Error; err != nil {
		t.Fatal(err)
	}
	u := uow.New(db)
	ob := outbox.New(u)
	sender := memory.New()
	relay := ob.NewRelay(db, nopLogger{}, outbox.RelayOptions{})
	relay.HandleSensitive(mail.Topic, mail.Deliver(sender, nopLogger{}))
	return u, ob, relay, sender
}

// Mail is queued with the change it is about and sent after commit, never
// for a rolled back change; its payload (links, tokens) is gone once sent.
func TestQueueDeliversAfterCommit(t *testing.T) {
	u, ob, relay, sender := setup(t)
	q := mail.NewQueue(ob)
	ctx := context.Background()
	reset := mail.Message{To: "ana@example.com", Subject: "Reset", Text: "https://notes.test/reset?token=secret"}

	if err := q.Send(ctx, reset); !errors.Is(err, outbox.ErrNotInTransaction) {
		t.Fatalf("outside a unit of work: %v", err)
	}

	boom := errors.New("business failure")
	_ = u.Do(ctx, func(ctx context.Context) error {
		if err := q.Send(ctx, mail.Message{To: "rolled@back.test", Subject: "x", Text: "x"}); err != nil {
			t.Fatal(err)
		}
		return boom
	})
	if err := u.Do(ctx, func(ctx context.Context) error { return q.Send(ctx, reset) }); err != nil {
		t.Fatal(err)
	}
	if err := u.Do(ctx, func(ctx context.Context) error {
		return q.Send(ctx, mail.Message{To: "ana@example.com", Subject: "", Text: "x"})
	}); !errors.Is(err, mail.ErrPermanent) {
		t.Fatalf("an invalid message is refused when queued: %v", err)
	}

	if _, err := relay.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	sent := sender.Sent()
	if len(sent) != 1 || sent[0].To != "ana@example.com" || sent[0].Text != reset.Text {
		t.Fatalf("sent %+v", sent)
	}

	var key, payload string
	_ = u.DB(ctx).Raw(`SELECT key, payload::text FROM outbox_messages WHERE processed_at IS NOT NULL`).Row().Scan(&key, &payload)
	if key != "a***@example.com" || payload != "{}" {
		t.Fatalf("delivered row: key=%q payload=%s", key, payload)
	}
}

func TestDeliverFailures(t *testing.T) {
	u, ob, relay, sender := setup(t)
	q := mail.NewQueue(ob)
	ctx := context.Background()
	// e.g. smtp's 550 for a recipient that doesn't exist
	sender.Fail = func(mail.Message) error { return fmt.Errorf("%w: 550 mailbox unavailable", mail.ErrPermanent) }
	if err := u.Do(ctx, func(ctx context.Context) error {
		return q.Send(ctx, mail.Message{To: "rejected@example.com", Subject: "x", Text: "x"})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := relay.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}

	var attempts int
	var failed bool
	_ = u.DB(ctx).Raw(`SELECT attempts, failed_at IS NOT NULL FROM outbox_messages`).Row().Scan(&attempts, &failed)
	if attempts != 1 || !failed {
		t.Fatalf("permanent failure: attempts=%d set aside=%v", attempts, failed)
	}
}
