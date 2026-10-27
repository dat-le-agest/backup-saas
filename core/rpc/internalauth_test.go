package rpc

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"core/apperr"
	"core/reqctx"
	"core/token"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func newServerWith(t *testing.T, auth InternalAuth, l *captureLogger) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(procedure, connect.NewUnaryHandler(procedure, echo, HandlerOptions(l, auth)))
	srv := httptest.NewUnstartedServer(mux)
	srv.Config.Protocols = new(http.Protocols)
	srv.Config.Protocols.SetUnencryptedHTTP2(true)
	srv.Start()
	t.Cleanup(srv.Close)
	return srv.URL
}

// rawCall sends headers as given (an attacker inside the network, or grpcurl).
func rawCall(t *testing.T, url string, headers map[string]string) (string, error) {
	t.Helper()
	p := new(http.Protocols)
	p.SetUnencryptedHTTP2(true)
	set := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			for k, v := range headers {
				req.Header().Set(k, v)
			}
			return next(ctx, req)
		}
	})
	c := connect.NewClient[wrapperspb.StringValue, wrapperspb.StringValue](
		&http.Client{Transport: &http.Transport{Protocols: p}}, url+procedure,
		connect.WithGRPC(), connect.WithInterceptors(set, ClientErrorInterceptor()),
	)
	return call(c, context.Background(), "")
}

func sign(t *testing.T, s *token.Signer, audience string, id token.Internal) string {
	t.Helper()
	raw, err := s.SignInternal(audience, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

var spoofed = map[string]string{
	reqctx.HeaderUserID: "admin", reqctx.HeaderTenantID: "victim", reqctx.HeaderTenantRole: "owner",
}

func TestEnforceTakesIdentityFromTheTokenOnly(t *testing.T) {
	url := newServerWith(t, testAuth(), &captureLogger{})
	u1 := sign(t, testSigner, testAudience, token.Internal{App: "notes", UserID: "u1", TenantID: "t1", Role: "member"})

	// identity headers next to a valid token are ignored
	headers := map[string]string{HeaderInternalToken: u1}
	for k, v := range spoofed {
		headers[k] = v
	}
	got, err := rawCall(t, url, headers)
	if err != nil || got != "u1|t1|member|" {
		t.Fatalf("got %q, %v", got, err)
	}

	// the gateway's client: identity and request id from ctx
	c := newClient(url, time.Second)
	ctx := reqctx.WithRequestID(reqctx.WithIdentity(context.Background(), reqctx.Identity{UserID: "u2", TenantID: "t2", Role: "viewer"}), "req-1")
	if got, err := call(c, ctx, ""); err != nil || got != "u2|t2|viewer|req-1" {
		t.Fatalf("client: got %q, %v", got, err)
	}
	// a call without a caller still carries a token: accepted, no identity
	if got, err := call(c, context.Background(), ""); err != nil || got != "|||" {
		t.Fatalf("anonymous: got %q, %v", got, err)
	}
}

func TestEnforceRejects(t *testing.T) {
	l := &captureLogger{}
	url := newServerWith(t, testAuth(), l)

	otherKey, _ := token.GenerateKey()
	other := token.NewSigner(otherKey, "test-gateway", time.Minute)
	access, _, _ := testSigner.Sign("u1", testAudience, "t1", "owner", 1)
	id := token.Internal{UserID: "u1", TenantID: "t1", Role: "owner"}

	cases := map[string]map[string]string{
		"headers only":    spoofed,
		"nothing":         {},
		"foreign key":     {HeaderInternalToken: sign(t, other, testAudience, id)},
		"another service": {HeaderInternalToken: sign(t, testSigner, "billing", id)},
		"access token":    {HeaderInternalToken: access},
		"garbage":         {HeaderInternalToken: "a.b.c"},
	}
	for name, headers := range cases {
		if _, err := rawCall(t, url, headers); !errors.Is(err, apperr.ErrUnauthenticated) {
			t.Errorf("%s: expected Unauthenticated, got %v", name, err)
		}
	}

	// a misconfigured service (no verifier) rejects every token
	noVerifier := newServerWith(t, InternalAuth{Mode: InternalAuthEnforce}, l)
	if _, err := rawCall(t, noVerifier, map[string]string{HeaderInternalToken: sign(t, testSigner, testAudience, id)}); !errors.Is(err, apperr.ErrUnauthenticated) {
		t.Errorf("no verifier: %v", err)
	}
	// so does an unknown mode (fail closed)
	typo := newServerWith(t, InternalAuth{Mode: "enforced", Verifier: testAuth().Verifier}, l)
	if _, err := rawCall(t, typo, spoofed); !errors.Is(err, apperr.ErrUnauthenticated) {
		t.Errorf("unknown mode with headers: %v", err)
	}
}

func TestPermissiveFallsBackToHeaders(t *testing.T) {
	auth := testAuth()
	auth.Mode = InternalAuthPermissive
	url := newServerWith(t, auth, &captureLogger{})

	if got, err := rawCall(t, url, spoofed); err != nil || got != "admin|victim|owner|" {
		t.Fatalf("without a token: got %q, %v", got, err)
	}
	u1 := sign(t, testSigner, testAudience, token.Internal{UserID: "u1", TenantID: "t1", Role: "member"})
	headers := map[string]string{HeaderInternalToken: u1}
	for k, v := range spoofed {
		headers[k] = v
	}
	if got, err := rawCall(t, url, headers); err != nil || !strings.HasPrefix(got, "u1|t1|member") {
		t.Fatalf("a token wins over headers: got %q, %v", got, err)
	}
	// an invalid token is never a fallback
	if _, err := rawCall(t, url, map[string]string{HeaderInternalToken: "a.b.c"}); !errors.Is(err, apperr.ErrUnauthenticated) {
		t.Fatalf("invalid token: %v", err)
	}
}

func TestOffTrustsHeaders(t *testing.T) {
	url := newServerWith(t, InternalAuth{Mode: InternalAuthOff}, &captureLogger{})
	if got, err := rawCall(t, url, spoofed); err != nil || got != "admin|victim|owner|" {
		t.Fatalf("got %q, %v", got, err)
	}
}
