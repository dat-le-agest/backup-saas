package mail

import (
	"context"

	"core/logger"
)

// LogSender writes mail to the log instead of sending it: dev without a mail
// server. The log holds the bodies, links and tokens included, so it is
// refused outside dev (core/components/mailc).
type LogSender struct {
	logger logger.Logger
}

func NewLogSender(l logger.Logger) *LogSender {
	return &LogSender{logger: l}
}

func (s *LogSender) Send(ctx context.Context, m Message) error {
	if err := m.Validate(); err != nil {
		return err
	}
	logger.WithContext(ctx, s.logger).WithFields(logger.Fields{
		"to": m.To, "subject": m.Subject,
	}).Infof("mail (not sent, MAIL_DRIVER=log):\n%s", m.Text)
	return nil
}
