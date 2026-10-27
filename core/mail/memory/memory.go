// Package memory is a mail.Sender for tests: it keeps what it was asked to send.
package memory

import (
	"context"
	"sync"

	"core/mail"
)

type Sender struct {
	mu   sync.Mutex
	sent []mail.Message
	// Fail, when set, decides the result of each Send (nil: sent).
	Fail func(m mail.Message) error
}

func New() *Sender { return &Sender{} }

func (s *Sender) Send(_ context.Context, m mail.Message) error {
	if err := m.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Fail != nil {
		if err := s.Fail(m); err != nil {
			return err
		}
	}
	s.sent = append(s.sent, m)
	return nil
}

// Sent returns the messages sent so far.
func (s *Sender) Sent() []mail.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]mail.Message(nil), s.sent...)
}

// To returns the messages sent to addr.
func (s *Sender) To(addr string) []mail.Message {
	var out []mail.Message
	for _, m := range s.Sent() {
		if m.To == addr {
			out = append(out, m)
		}
	}
	return out
}
