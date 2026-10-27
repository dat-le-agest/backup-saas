package jwtc

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"core/logger"
	"core/sctx"
	"core/token"
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

// newSigner is a component as if its flags were parsed (defaults + set).
func newSigner(set func(s *signerComponent)) *signerComponent {
	s := NewSigner("jwt")
	s.issuer, s.accessTTL, s.keysReload = defaultIssuer, defaultAccessTTL, defaultKeysReload
	if set != nil {
		set(s)
	}
	return s
}

func writeKey(t *testing.T, dir, name string) ed25519.PrivateKey {
	t.Helper()
	key, err := token.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := token.EncodePrivateKeyPEM(key)
	if err := os.WriteFile(filepath.Join(dir, name+".pem"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return key
}

func kid(k ed25519.PrivateKey) string { return token.KeyID(k.Public().(ed25519.PublicKey)) }

func TestKeySources(t *testing.T) {
	dir := t.TempDir()
	key := writeKey(t, dir, "2026-10-27")
	file := filepath.Join(dir, "2026-10-27.pem")

	cases := map[string]struct {
		set     func(s *signerComponent)
		env     string
		wantErr string
	}{
		"keys dir":        {set: func(s *signerComponent) { s.keysDir = dir }, env: sctx.PrdEnv},
		"key file":        {set: func(s *signerComponent) { s.keyFile = file }, env: sctx.PrdEnv},
		"ephemeral (dev)": {env: sctx.DevEnv},
		"none outside dev": {
			env: sctx.PrdEnv, wantErr: "jwt-keys-dir or jwt-private-key-file is required",
		},
		"both": {
			set: func(s *signerComponent) { s.keysDir, s.keyFile = dir, file }, env: sctx.DevEnv,
			wantErr: "either jwt-private-key-file or jwt-keys-dir",
		},
		"negative reload": {
			set: func(s *signerComponent) { s.keysDir, s.keysReload = dir, -1 }, env: sctx.DevEnv,
			wantErr: "jwt-keys-reload",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := newSigner(tc.set)
			err := s.Activate(fakeSC{env: tc.env})
			defer s.Stop()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if s.keyFile != "" || s.keysDir != "" {
				if got := s.Signer().KeyRing().ActiveKid(); got != kid(key) {
					t.Fatalf("active %s", got)
				}
			}
			if _, _, err := s.Signer().Sign("u1", "notes", "", "", 0); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFlagPrefix(t *testing.T) {
	s := NewSigner("internal-jwt", WithFlagPrefix("internal-jwt"))
	s.accessTTL = defaultAccessTTL
	err := s.Activate(fakeSC{env: sctx.PrdEnv})
	if err == nil || !strings.Contains(err.Error(), "internal-jwt-keys-dir or internal-jwt-private-key-file") {
		t.Fatalf("errors name the prefixed flags: %v", err)
	}
	if got := NewVerifier("v", WithFlagPrefix("internal-jwt")).flagName("jwks-url"); got != "internal-jwt-jwks-url" {
		t.Fatalf("verifier flag %s", got)
	}
}

// The runbook's rotation, picked up by the reload loop without a restart.
func TestReloadRotatesKeys(t *testing.T) {
	dir := t.TempDir()
	oldKey := writeKey(t, dir, "2026-10-01")

	s := newSigner(func(s *signerComponent) { s.keysDir = dir })
	if err := s.Activate(fakeSC{env: sctx.PrdEnv}); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	v := s.Signer().Verifier("notes")
	oldTok, _, _ := s.Signer().Sign("u1", "notes", "", "", 0)

	// 1. add the new key, pin the old one: still signing with the old key
	newKey := writeKey(t, dir, "2026-11-01")
	writeFile(t, filepath.Join(dir, token.ActiveFile), "2026-10-01")
	s.reloadKeys()
	ring := s.Signer().KeyRing()
	if ring.ActiveKid() != kid(oldKey) || len(ring.Kids()) != 2 {
		t.Fatalf("after adding: active %s keys %v", ring.ActiveKid(), ring.Kids())
	}
	if len(s.JWKS().Keys) != 2 {
		t.Fatal("the new key must be published before it signs")
	}

	// 2. activate the new key
	writeFile(t, filepath.Join(dir, token.ActiveFile), "2026-11-01")
	s.reloadKeys()
	if got := s.Signer().KeyRing().ActiveKid(); got != kid(newKey) {
		t.Fatalf("after activating: %s", got)
	}
	if _, err := v.Verify(oldTok); err != nil {
		t.Fatalf("old tokens stay valid until the old key is removed: %v", err)
	}

	// a broken directory keeps the current keys
	writeFile(t, filepath.Join(dir, "broken.pem"), "nope")
	s.reloadKeys()
	if got := s.Signer().KeyRing().ActiveKid(); got != kid(newKey) {
		t.Fatalf("a failed reload must keep the keys: %s", got)
	}
	_ = os.Remove(filepath.Join(dir, "broken.pem"))

	// 3. retire the old key
	_ = os.Remove(filepath.Join(dir, "2026-10-01.pem"))
	s.reloadKeys()
	if _, err := v.Verify(oldTok); err == nil {
		t.Fatal("tokens of a removed key must be rejected")
	}
}

func TestReloadLoopStops(t *testing.T) {
	dir := t.TempDir()
	writeKey(t, dir, "a")

	s := newSigner(func(s *signerComponent) { s.keysDir = dir })
	if err := s.Activate(fakeSC{env: sctx.PrdEnv}); err != nil {
		t.Fatal(err)
	}
	// a short interval to exercise the loop (and races under -race)
	_ = s.Stop()
	s.watchKeys(5 * time.Millisecond)
	b := writeKey(t, dir, "b")
	deadline := time.Now().Add(2 * time.Second)
	for s.Signer().KeyRing().ActiveKid() != kid(b) {
		if time.Now().After(deadline) {
			t.Fatal("the loop did not reload the keys")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(); err != nil {
		t.Fatal("Stop must be idempotent")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
