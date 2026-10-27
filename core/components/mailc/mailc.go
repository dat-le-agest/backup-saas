// Package mailc picks the mail.Sender that delivers a service's mail.
//
//	--mail-driver / MAIL_DRIVER
//	  log   (default) write mail to the log instead of sending it; dev only
//	  smtp  send over SMTP: local tests against Mailpit
//	--mail-smtp-url / MAIL_SMTP_URL  smtp://[user:pass@]host[:port][?tls=starttls|implicit|none]
//	--mail-from     / MAIL_FROM      default sender, e.g. "Notes <no-reply@example.com>"
//	--mail-timeout  / MAIL_TIMEOUT   seconds per message, default 10
//
// A provider's HTTP API is another driver here: code that sends mail only
// depends on mail.Sender (and queues it, see mail.Queue).
package mailc

import (
	"flag"
	"fmt"
	"time"

	"core/logger"
	"core/mail"
	"core/mail/smtp"
	"core/sctx"
)

const (
	DriverLog  = "log"
	DriverSMTP = "smtp"

	defaultTimeout = 10
)

type MailComponent interface {
	Sender() mail.Sender
}

type mailComponent struct {
	id      string
	driver  string
	smtpURL string
	from    string
	timeout int

	logger logger.Logger
	sender mail.Sender
}

func NewMail(id string) *mailComponent {
	return &mailComponent{id: id}
}

func (m *mailComponent) ID() string { return m.id }

func (m *mailComponent) InitFlags() {
	flag.StringVar(&m.driver, "mail-driver", DriverLog,
		"how mail is delivered: log (written to the log, dev only) | smtp - Default log")
	flag.StringVar(&m.smtpURL, "mail-smtp-url", "",
		"smtp://[user:pass@]host[:port][?tls=starttls|implicit|none], e.g. smtp://localhost:1025?tls=none (Mailpit)")
	flag.StringVar(&m.from, "mail-from", "",
		`default sender, e.g. "Notes <no-reply@example.com>"`)
	flag.IntVar(&m.timeout, "mail-timeout", defaultTimeout,
		"seconds to send one message - Default 10")
}

func (m *mailComponent) Activate(sc sctx.ServiceContext) error {
	m.logger = sc.Logger(m.id)

	switch m.driver {
	case DriverLog:
		// the log would hold the bodies: links and tokens included
		if sc.EnvName() != sctx.DevEnv {
			return fmt.Errorf("mail-driver=log is only allowed with app-env=%s", sctx.DevEnv)
		}
		m.sender = mail.NewLogSender(m.logger)
		m.logger.Warn("mail is written to the log, not sent (mail-driver=log)")
		return nil

	case DriverSMTP:
		cfg, err := smtp.ParseURL(m.smtpURL)
		if err != nil {
			return fmt.Errorf("mail-smtp-url: %w", err)
		}
		if cfg.TLS == smtp.TLSNone && sc.EnvName() == sctx.PrdEnv {
			return fmt.Errorf("mail-smtp-url: tls=none is not allowed with app-env=%s", sctx.PrdEnv)
		}
		cfg.From = m.from
		cfg.Timeout = time.Duration(m.timeout) * time.Second
		s, err := smtp.New(cfg)
		if err != nil {
			return fmt.Errorf("mail-from: %w", err)
		}
		m.sender = s
		m.logger.Infof("mail over smtp (host=%s:%d, tls=%s, from=%s)", cfg.Host, cfg.Port, cfg.TLS, m.from)
		return nil
	}
	return fmt.Errorf("invalid mail-driver %q (log | smtp)", m.driver)
}

func (m *mailComponent) Stop() error { return nil }

func (m *mailComponent) Sender() mail.Sender { return m.sender }
