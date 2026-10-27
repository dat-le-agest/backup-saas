package mailc

import (
	"strings"
	"testing"

	"core/logger"
	"core/mail"
	"core/mail/smtp"
	"core/sctx"
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

type fakeSC struct {
	sctx.ServiceContext
	env string
}

func (fakeSC) Logger(string) logger.Logger { return nopLogger{} }
func (s fakeSC) EnvName() string           { return s.env }

func TestDrivers(t *testing.T) {
	cases := map[string]struct {
		driver, url, from, env string
		wantErr                string
		want                   any
	}{
		"log in dev":         {driver: DriverLog, env: sctx.DevEnv, want: &mail.LogSender{}},
		"log outside dev":    {driver: DriverLog, env: sctx.StgEnv, wantErr: "only allowed"},
		"smtp (mailpit)":     {driver: DriverSMTP, url: "smtp://localhost:1025?tls=none", from: "Notes <no-reply@notes.test>", env: sctx.DevEnv, want: &smtp.Sender{}},
		"smtp without tls":   {driver: DriverSMTP, url: "smtp://smtp.example.com?tls=none", from: "a@b.c", env: sctx.PrdEnv, wantErr: "tls=none"},
		"smtp without url":   {driver: DriverSMTP, from: "a@b.c", env: sctx.DevEnv, wantErr: "mail-smtp-url"},
		"smtp without from":  {driver: DriverSMTP, url: "smtp://localhost:1025?tls=none", env: sctx.DevEnv, wantErr: "mail-from"},
		"unknown driver":     {driver: "ses", env: sctx.DevEnv, wantErr: "invalid mail-driver"},
		"smtp with starttls": {driver: DriverSMTP, url: "smtp://u:p@smtp.example.com", from: "a@b.c", env: sctx.PrdEnv, want: &smtp.Sender{}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := NewMail("mail")
			m.driver, m.smtpURL, m.from, m.timeout = tc.driver, tc.url, tc.from, defaultTimeout
			err := m.Activate(fakeSC{env: tc.env})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			switch tc.want.(type) {
			case *mail.LogSender:
				if _, ok := m.Sender().(*mail.LogSender); !ok {
					t.Fatalf("sender %T", m.Sender())
				}
			case *smtp.Sender:
				if _, ok := m.Sender().(*smtp.Sender); !ok {
					t.Fatalf("sender %T", m.Sender())
				}
			}
		})
	}
}
