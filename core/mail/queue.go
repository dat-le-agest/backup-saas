package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"core/logger"
	"core/outbox"
)

// Topic of the outbox messages holding mail. Their payload contains links with
// live tokens (password reset...): the relay registers it with HandleSensitive,
// which empties the payload once delivered.
const Topic = "mail.send"

// Queue is a Sender storing mail in the outbox, in the transaction of ctx: Send
// must run inside a unit of work (uow.Do), next to the change the mail is
// about. The relay delivers it with Deliver.
type Queue struct {
	outbox *outbox.Outbox
}

func NewQueue(o *outbox.Outbox) *Queue {
	return &Queue{outbox: o}
}

func (q *Queue) Send(ctx context.Context, m Message) error {
	if err := m.Validate(); err != nil {
		return err
	}
	return q.outbox.Publish(ctx, Topic, MaskAddress(m.To), m)
}

// Deliver is the relay's handler of Topic, sending with s:
//
//	relay.HandleSensitive(mail.Topic, mail.Deliver(smtpSender, log))
//
// Permanent failures (ErrPermanent) set the message aside at once instead of
// retrying it.
func Deliver(s Sender, l logger.Logger) outbox.Handler {
	return func(ctx context.Context, msg outbox.Message) error {
		var m Message
		if err := json.Unmarshal(msg.Payload, &m); err != nil {
			return outbox.Permanent(fmt.Errorf("decode mail: %w", err))
		}
		err := s.Send(ctx, m)
		switch {
		case err == nil:
			logger.WithContext(ctx, l).WithFields(logger.Fields{"to": msg.Key, "outbox_id": msg.ID}).Info("mail sent")
			return nil
		case errors.Is(err, ErrPermanent):
			return outbox.Permanent(err)
		}
		return err
	}
}
