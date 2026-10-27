package smtp

import (
	"bufio"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	netmail "net/mail"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"core/mail"
)

// fakeServer is a minimal SMTP server (like Mailpit with tls=none): it records
// the envelope and data of each message. rcptReply overrides the RCPT reply.
type fakeServer struct {
	addr      string
	rcptReply string
	starttls  bool

	mu   sync.Mutex
	from []string
	to   []string
	data []string
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	s := &fakeServer{addr: ln.Addr().String()}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	return s
}

func (s *fakeServer) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	reply := func(line string) { _, _ = io.WriteString(conn, line+"\r\n") }
	reply("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(cmd)
		switch {
		case strings.HasPrefix(upper, "EHLO"):
			if s.starttls {
				reply("250-fake")
				reply("250 STARTTLS")
			} else {
				reply("250-fake")
				reply("250 8BITMIME")
			}
		case strings.HasPrefix(upper, "MAIL FROM:"):
			s.mu.Lock()
			s.from = append(s.from, angleAddr(cmd))
			s.mu.Unlock()
			reply("250 ok")
		case strings.HasPrefix(upper, "RCPT TO:"):
			if s.rcptReply != "" {
				reply(s.rcptReply)
				continue
			}
			s.mu.Lock()
			s.to = append(s.to, angleAddr(cmd))
			s.mu.Unlock()
			reply("250 ok")
		case upper == "DATA":
			reply("354 go ahead")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(strings.TrimPrefix(l, "."))
			}
			s.mu.Lock()
			s.data = append(s.data, b.String())
			s.mu.Unlock()
			reply("250 queued")
		case upper == "QUIT":
			reply("221 bye")
			return
		default:
			reply("250 ok")
		}
	}
}

// angleAddr: the <address> of MAIL FROM / RCPT TO, without parameters (BODY=8BITMIME).
func angleAddr(cmd string) string {
	_, rest, _ := strings.Cut(cmd, "<")
	addr, _, _ := strings.Cut(rest, ">")
	return addr
}

func newSender(t *testing.T, s *fakeServer, tlsMode string) *Sender {
	t.Helper()
	host, port, _ := net.SplitHostPort(s.addr)
	p, _ := strconv.Atoi(port)
	sender, err := New(Config{Host: host, Port: p, TLS: tlsMode, From: "Notes <no-reply@notes.test>", Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return sender
}

func TestSendMultipart(t *testing.T) {
	srv := newFakeServer(t)
	s := newSender(t, srv, TLSNone)

	err := s.Send(context.Background(), mail.Message{
		To: "Ana <ana@example.com>", Subject: "Đặt lại mật khẩu",
		Text: "Open https://notes.test/reset?token=abc", HTML: "<p>Open <a href=\"https://notes.test/reset?token=abc\">this link</a></p>",
		Headers: map[string]string{"x-request-id": "req-1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.data) != 1 || srv.from[0] != "no-reply@notes.test" || srv.to[0] != "ana@example.com" {
		t.Fatalf("envelope from=%v to=%v (%d messages)", srv.from, srv.to, len(srv.data))
	}
	msg, err := netmail.ReadMessage(strings.NewReader(srv.data[0]))
	if err != nil {
		t.Fatal(err)
	}
	subject, _ := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if subject != "Đặt lại mật khẩu" || msg.Header.Get("X-Request-Id") != "req-1" || msg.Header.Get("Message-Id") == "" {
		t.Fatalf("headers %v (subject %q)", msg.Header, subject)
	}

	mt, params, _ := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if mt != "multipart/alternative" {
		t.Fatalf("content type %s", mt)
	}
	mr := multipart.NewReader(msg.Body, params["boundary"])
	var parts []string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		// multipart.Reader decodes quoted-printable itself
		body, _ := io.ReadAll(p)
		parts = append(parts, p.Header.Get("Content-Type")+"|"+string(body))
	}
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "text/plain") || !strings.Contains(parts[1], `href="https://notes.test/reset?token=abc"`) {
		t.Fatalf("parts %q", parts)
	}
}

func TestSendTextOnly(t *testing.T) {
	srv := newFakeServer(t)
	long := strings.Repeat("x", 200) // longer than a quoted-printable line
	if err := newSender(t, srv, TLSNone).Send(context.Background(), mail.Message{To: "ana@example.com", Subject: "Hi", Text: long}); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	msg, _ := netmail.ReadMessage(strings.NewReader(srv.data[0]))
	body, _ := io.ReadAll(quotedprintable.NewReader(msg.Body))
	if !strings.HasPrefix(msg.Header.Get("Content-Type"), "text/plain") || strings.TrimSpace(string(body)) != long {
		t.Fatalf("%v %q", msg.Header, body)
	}
}

func TestFailures(t *testing.T) {
	ok := mail.Message{To: "ana@example.com", Subject: "Hi", Text: "x"}

	rejecting := newFakeServer(t)
	rejecting.rcptReply = "550 no such user"
	if err := newSender(t, rejecting, TLSNone).Send(context.Background(), ok); !errors.Is(err, mail.ErrPermanent) {
		t.Errorf("rejected recipient: expected permanent, got %v", err)
	}

	busy := newFakeServer(t)
	busy.rcptReply = "451 try again later"
	if err := newSender(t, busy, TLSNone).Send(context.Background(), ok); err == nil || errors.Is(err, mail.ErrPermanent) {
		t.Errorf("4xx: expected a temporary error, got %v", err)
	}

	// starttls required, server without it
	if err := newSender(t, newFakeServer(t), TLSStartTLS).Send(context.Background(), ok); !errors.Is(err, mail.ErrPermanent) {
		t.Errorf("no STARTTLS: expected permanent, got %v", err)
	}

	// nothing listening: temporary
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	down := &fakeServer{addr: addr}
	if err := newSender(t, down, TLSNone).Send(context.Background(), ok); err == nil || errors.Is(err, mail.ErrPermanent) {
		t.Errorf("unreachable: expected a temporary error, got %v", err)
	}

	// invalid message: refused before connecting
	if err := newSender(t, down, TLSNone).Send(context.Background(), mail.Message{To: "a@b.c", Subject: "x\r\nBcc: evil@x.y", Text: "x"}); !errors.Is(err, mail.ErrPermanent) {
		t.Errorf("header injection: %v", err)
	}
}

func TestParseURL(t *testing.T) {
	cases := map[string]Config{
		"smtp://mailpit:1025?tls=none":          {Host: "mailpit", Port: 1025, TLS: TLSNone},
		"smtp://u:p%40ss@smtp.example.com":      {Host: "smtp.example.com", Port: 587, TLS: TLSStartTLS, Username: "u", Password: "p@ss"},
		"smtp://smtp.example.com?tls=implicit":  {Host: "smtp.example.com", Port: 465, TLS: TLSImplicit},
		"smtp://smtp.example.com:2525?tls=none": {Host: "smtp.example.com", Port: 2525, TLS: TLSNone},
	}
	for raw, want := range cases {
		got, err := ParseURL(raw)
		if err != nil || got != want {
			t.Errorf("%s: got %+v, %v", raw, got, err)
		}
	}
	for _, raw := range []string{"", "http://x", "smtp://", "smtp://x:port"} {
		if _, err := ParseURL(raw); err == nil {
			t.Errorf("%q: expected an error", raw)
		}
	}
	if _, err := New(Config{Host: "x", Port: 25, TLS: "ssl", From: "a@b.c"}); err == nil {
		t.Error("unknown tls mode: expected an error")
	}
	if _, err := New(Config{Host: "x", Port: 25, TLS: TLSNone, From: "nope"}); err == nil {
		t.Error("invalid from: expected an error")
	}
}
