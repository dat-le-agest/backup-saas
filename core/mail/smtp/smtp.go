// Package smtp is a mail.Sender over SMTP (net/smtp), kept small: one
// connection per message. It is meant for local tests against Mailpit
// (deploy/platform.compose.yaml); production mail goes through a provider's
// API (another mail.Sender).
package smtp

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	netmail "net/mail"
	"net/smtp"
	"net/textproto"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"core/mail"
)

// TLS modes.
const (
	TLSStartTLS = "starttls" // plain connection upgraded with STARTTLS (port 587)
	TLSImplicit = "implicit" // TLS from the first byte (port 465)
	TLSNone     = "none"     // no TLS: local servers (Mailpit) only
)

type Config struct {
	Host     string
	Port     int
	Username string // empty: no AUTH
	Password string
	TLS      string        // TLSStartTLS (default), TLSImplicit or TLSNone
	From     string        // default sender, e.g. "Notes <no-reply@example.com>"
	Timeout  time.Duration // per message, default 10s
}

// ParseURL reads smtp://[user:pass@]host[:port][?tls=starttls|implicit|none]
// (default port 587, or 465 with implicit TLS).
func ParseURL(raw string) (Config, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "smtp" || u.Hostname() == "" {
		return Config{}, fmt.Errorf("smtp: invalid url (expected smtp://[user:pass@]host[:port][?tls=...])")
	}
	c := Config{Host: u.Hostname(), TLS: u.Query().Get("tls")}
	if c.TLS == "" {
		c.TLS = TLSStartTLS
	}
	if u.User != nil {
		c.Username = u.User.Username()
		c.Password, _ = u.User.Password()
	}
	switch p := u.Port(); {
	case p != "":
		if c.Port, err = strconv.Atoi(p); err != nil {
			return Config{}, fmt.Errorf("smtp: invalid port %q", p)
		}
	case c.TLS == TLSImplicit:
		c.Port = 465
	default:
		c.Port = 587
	}
	return c, nil
}

type Sender struct {
	cfg  Config
	from string // address of cfg.From, for MAIL FROM
	now  func() time.Time
}

func New(cfg Config) (*Sender, error) {
	if cfg.Host == "" || cfg.Port <= 0 {
		return nil, errors.New("smtp: host and port are required")
	}
	if !slices.Contains([]string{TLSStartTLS, TLSImplicit, TLSNone}, cfg.TLS) {
		return nil, fmt.Errorf("smtp: invalid tls mode %q (starttls | implicit | none)", cfg.TLS)
	}
	from, err := netmail.ParseAddress(cfg.From)
	if err != nil {
		return nil, fmt.Errorf("smtp: invalid from address %q", cfg.From)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	return &Sender{cfg: cfg, from: from.Address, now: time.Now}, nil
}

func (s *Sender) Send(ctx context.Context, m mail.Message) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if m.From == "" {
		m.From = s.cfg.From
	}
	envelopeFrom := s.from
	if a, err := netmail.ParseAddress(m.From); err == nil {
		envelopeFrom = a.Address
	}
	to, _ := netmail.ParseAddress(m.To) // validated

	data, err := encode(m, s.now())
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()
	if err := s.deliver(ctx, envelopeFrom, to.Address, data); err != nil {
		return classify(err)
	}
	return nil
}

func (s *Sender) deliver(ctx context.Context, from, to string, data []byte) error {
	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	tlsConfig := &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}

	var conn net.Conn
	var err error
	if s.cfg.TLS == TLSImplicit {
		conn, err = (&tls.Dialer{Config: tlsConfig}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		return err
	}
	defer c.Close()

	if s.cfg.TLS == TLSStartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return fmt.Errorf("%w: server does not offer STARTTLS (use tls=none for a local server)", mail.ErrPermanent)
		}
		if err := c.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	if s.cfg.Username != "" {
		// net/smtp refuses PLAIN auth without TLS, except to localhost
		if err := c.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)); err != nil {
			return err
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// classify: 5xx replies are permanent (rejected recipient, bad auth...).
func classify(err error) error {
	var tpErr *textproto.Error
	if errors.As(err, &tpErr) && tpErr.Code >= 500 {
		return fmt.Errorf("%w: smtp %d %s", mail.ErrPermanent, tpErr.Code, tpErr.Msg)
	}
	return fmt.Errorf("smtp: %w", err)
}

// encode builds the RFC 5322 message: text and/or HTML (multipart/alternative),
// quoted-printable, UTF-8 subject.
func encode(m mail.Message, now time.Time) ([]byte, error) {
	var b bytes.Buffer
	header := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }

	from, err := netmail.ParseAddress(m.From)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid from address", mail.ErrPermanent)
	}
	to, _ := netmail.ParseAddress(m.To)
	header("From", from.String())
	header("To", to.String())
	header("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	header("Date", now.Format(time.RFC1123Z))
	header("Message-ID", "<"+randomHex(16)+"@"+domainOf(from.Address)+">")
	header("MIME-Version", "1.0")
	keys := make([]string, 0, len(m.Headers))
	for k := range m.Headers {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		header(textproto.CanonicalMIMEHeaderKey(k), m.Headers[k])
	}

	part := func(contentType, body string) {
		header("Content-Type", contentType+"; charset=utf-8")
		header("Content-Transfer-Encoding", "quoted-printable")
		b.WriteString("\r\n")
		qp := quotedprintable.NewWriter(&b)
		_, _ = qp.Write([]byte(body))
		_ = qp.Close()
		b.WriteString("\r\n")
	}

	switch {
	case m.Text != "" && m.HTML != "":
		boundary := randomHex(12)
		header("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
		b.WriteString("\r\n")
		for _, p := range []struct{ typ, body string }{{"text/plain", m.Text}, {"text/html", m.HTML}} {
			b.WriteString("--" + boundary + "\r\n")
			part(p.typ, p.body)
		}
		b.WriteString("--" + boundary + "--\r\n")
	case m.HTML != "":
		part("text/html", m.HTML)
	default:
		part("text/plain", m.Text)
	}
	return b.Bytes(), nil
}

func domainOf(addr string) string {
	if _, d, ok := strings.Cut(addr, "@"); ok && d != "" {
		return d
	}
	return "localhost"
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
