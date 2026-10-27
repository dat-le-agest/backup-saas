package internalauthc

import (
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"core/logger"
	"core/rpc"
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
func (fakeSC) GetName() string             { return "note" }

// newComponent is a component as if its flags were parsed (defaults + set).
func newComponent(set func(c *component)) *component {
	c := New("internal-auth")
	c.mode, c.issuer = rpc.InternalAuthEnforce, defaultIssuer
	if set != nil {
		set(c)
	}
	return c
}

// newGateway: the gateway's internal token signer and its public key.
func newGateway(t *testing.T) (*token.Signer, ed25519.PublicKey) {
	t.Helper()
	key, err := token.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return token.NewSigner(key, defaultIssuer, time.Minute), key.Public().(ed25519.PublicKey)
}

func TestVerifiesWithTheGatewaysJWKS(t *testing.T) {
	gw, _ := newGateway(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(gw.PublicKeys())
	}))
	defer srv.Close()

	c := newComponent(func(c *component) { c.jwksURL = srv.URL })
	if err := c.Activate(fakeSC{env: sctx.PrdEnv}); err != nil {
		t.Fatal(err)
	}
	auth := c.Auth()
	if auth.Mode != rpc.InternalAuthEnforce {
		t.Fatalf("mode %s", auth.Mode)
	}

	// audience defaults to the service name
	raw, _ := gw.SignInternal("note", token.Internal{UserID: "u1"}, 0)
	if _, err := auth.Verifier.Verify(raw); err != nil {
		t.Fatal(err)
	}
	raw, _ = gw.SignInternal("billing", token.Internal{UserID: "u1"}, 0)
	if _, err := auth.Verifier.Verify(raw); err == nil {
		t.Fatal("a token for another service must be rejected")
	}
}

func TestVerifiesWithAPublicKeyDirectory(t *testing.T) {
	gw, pub := newGateway(t)
	dir := t.TempDir()
	data, _ := token.EncodePublicKeyPEM(pub)
	if err := os.WriteFile(filepath.Join(dir, "gw.pem"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	c := newComponent(func(c *component) { c.publicKeys = dir })
	if err := c.Activate(fakeSC{env: sctx.PrdEnv}); err != nil {
		t.Fatal(err)
	}
	raw, _ := gw.SignInternal("note", token.Internal{UserID: "u1"}, 0)
	if _, err := c.Auth().Verifier.Verify(raw); err != nil {
		t.Fatal(err)
	}
}

func TestActivateErrors(t *testing.T) {
	cases := map[string]struct {
		set     func(c *component)
		env     string
		wantErr string
	}{
		"no key source":   {env: sctx.DevEnv, wantErr: "internal-jwt-jwks-url or internal-jwt-public-keys-dir is required"},
		"both sources":    {set: func(c *component) { c.jwksURL, c.publicKeys = "http://gw/jwks", "/keys" }, env: sctx.DevEnv, wantErr: "not both"},
		"bad url":         {set: func(c *component) { c.jwksURL = "gw:9081" }, env: sctx.DevEnv, wantErr: "invalid internal-jwt-jwks-url"},
		"empty keys dir":  {set: func(c *component) { c.publicKeys = t.TempDir() }, env: sctx.DevEnv, wantErr: "no *.pem public key"},
		"unknown mode":    {set: func(c *component) { c.mode = "enforced" }, env: sctx.DevEnv, wantErr: "invalid internal-auth"},
		"off in prd":      {set: func(c *component) { c.mode = rpc.InternalAuthOff }, env: sctx.PrdEnv, wantErr: "not allowed"},
		"off outside prd": {set: func(c *component) { c.mode = rpc.InternalAuthOff }, env: sctx.StgEnv},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := newComponent(tc.set).Activate(fakeSC{env: tc.env})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected %q, got %v", tc.wantErr, err)
			}
		})
	}
}
