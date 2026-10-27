// Package mail sends email. Code that sends mail depends on Sender only:
//
//   - Queue stores each message in the transactional outbox (core/outbox), in
//     the caller's unit of work: a mail exists if and only if the data change
//     that triggered it committed, and a slow or failing provider never slows
//     or fails the request. The outbox relay delivers it (Deliver).
//   - the delivering Sender is a provider: mail/smtp (local tests against
//     Mailpit), LogSender (dev without a mail server), mail/memory (tests).
//     A provider's HTTP API is another Sender (core/components/mailc picks one).
package mail

import (
	"context"
	"errors"
	"fmt"
	netmail "net/mail"
	"strings"
)

// Message is one email to one recipient.
type Message struct {
	// From is optional: the sender's default (MAIL_FROM) is used when empty.
	From    string            `json:"from,omitempty"`
	To      string            `json:"to"`
	Subject string            `json:"subject"`
	Text    string            `json:"text,omitempty"`
	HTML    string            `json:"html,omitempty"`
	Headers map[string]string `json:"headers,omitempty"` // e.g. List-Unsubscribe
}

// Sender sends a message. Errors wrapping ErrPermanent will fail again
// (rejected recipient...), others may succeed on a retry.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// ErrPermanent: retrying the same message will not help.
var ErrPermanent = errors.New("mail: permanent failure")

// Validate checks what every sender needs: a single valid recipient, a
// subject, a body, and no line breaks in header values (header injection).
func (m Message) Validate() error {
	if err := validateAddress("to", m.To); err != nil {
		return err
	}
	if m.From != "" {
		if err := validateAddress("from", m.From); err != nil {
			return err
		}
	}
	if strings.TrimSpace(m.Subject) == "" {
		return fmt.Errorf("%w: subject is required", ErrPermanent)
	}
	if m.Text == "" && m.HTML == "" {
		return fmt.Errorf("%w: a text or html body is required", ErrPermanent)
	}
	if strings.ContainsAny(m.Subject, "\r\n") {
		return fmt.Errorf("%w: subject must be a single line", ErrPermanent)
	}
	for k, v := range m.Headers {
		if k == "" || strings.ContainsAny(k, "\r\n: ") || strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("%w: invalid header %q", ErrPermanent, k)
		}
	}
	return nil
}

func validateAddress(field, addr string) error {
	if strings.ContainsAny(addr, "\r\n") {
		return fmt.Errorf("%w: %s must be a single line", ErrPermanent, field)
	}
	if _, err := netmail.ParseAddress(addr); err != nil {
		return fmt.Errorf("%w: invalid %s address %q", ErrPermanent, field, addr)
	}
	return nil
}

// MaskAddress hides most of the local part (j***@example.com), for logs and
// outbox keys: enough to recognize a recipient, not to collect addresses.
func MaskAddress(addr string) string {
	if a, err := netmail.ParseAddress(addr); err == nil {
		addr = a.Address
	}
	local, domain, ok := strings.Cut(addr, "@")
	if !ok || local == "" {
		return "***"
	}
	return local[:1] + "***@" + domain
}
