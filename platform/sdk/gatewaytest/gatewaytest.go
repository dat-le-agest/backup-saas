// Package gatewaytest runs fake platform services for gateway tests: an iam
// that signs real access tokens, serves its JWKS and answers membership
// versions, plus the h2c server and gRPC client wiring rpcc uses.
//
//	iam := gatewaytest.NewIam()
//	iamURL, jwksURL := gatewaytest.ServeIam(t, iam, iam, log)
//	auth := gatewaytest.Authenticator(t, jwksURL, gatewaytest.GRPCClient(iamURL, iamv1connect.NewIamServiceClient), nil, log)
//	tok := iam.Member("u1", "t1", "member", 1)
package gatewaytest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	iamv1 "api/gen/go/platform/iam/v1"
	"api/gen/go/platform/iam/v1/iamv1connect"
	"core/apperr"
	"core/logger"
	"core/rpc"
	"sdk/authn"
	"sdk/token"

	"connectrpc.com/connect"
	"github.com/redis/go-redis/v9"
)

// Issuer and Audience of the tokens the fake iam signs.
const (
	Issuer   = "iam"
	Audience = "api"
)

// Iam is a fake iam service. Embed it to implement more RPCs:
//
//	type fakeIam struct{ *gatewaytest.Iam }
//	func (f *fakeIam) Login(...) {...}
type Iam struct {
	iamv1connect.UnimplementedIamServiceHandler

	Signer *token.Signer
	// GetMembershipVersion calls
	VersionCalls atomic.Int32

	mu       sync.Mutex
	versions map[string]int64 // tenant/user → version
}

func NewIam() *Iam {
	key, err := token.GenerateKey()
	if err != nil {
		panic(err)
	}
	return &Iam{Signer: token.NewSigner(key, Issuer, Audience, 10*time.Minute), versions: map[string]int64{}}
}

// SetVersion sets the current version of a membership (as iam does on a change).
func (f *Iam) SetVersion(tenantID, userID string, v int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.versions[tenantID+"/"+userID] = v
}

// RemoveMembership forgets a membership: GetMembershipVersion answers NOT_FOUND.
func (f *Iam) RemoveMembership(tenantID, userID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.versions, tenantID+"/"+userID)
}

// Token signs an access token without registering the membership.
// tenantID "" gives a tenant-less token.
func (f *Iam) Token(userID, tenantID, role string, version int64) string {
	access, _, err := f.Signer.Sign(userID, tenantID, role, version)
	if err != nil {
		panic(err)
	}
	return access
}

// Member registers a membership at version and returns an access token for it.
func (f *Iam) Member(userID, tenantID, role string, version int64) string {
	f.SetVersion(tenantID, userID, version)
	return f.Token(userID, tenantID, role, version)
}

func (f *Iam) GetMembershipVersion(_ context.Context, req *iamv1.GetMembershipVersionRequest) (*iamv1.GetMembershipVersionResponse, error) {
	f.VersionCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.versions[req.GetTenantId()+"/"+req.GetUserId()]
	if !ok {
		return nil, apperr.ErrNotFound
	}
	return &iamv1.GetMembershipVersionResponse{Version: v}, nil
}

// ServeIam serves h (usually f, or a type embedding f) over h2c with f's JWKS
// at /.well-known/jwks.json, until the test ends.
func ServeIam(t testing.TB, f *Iam, h iamv1connect.IamServiceHandler, l logger.Logger) (url, jwksURL string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(iamv1connect.NewIamServiceHandler(h, rpc.HandlerOptions(l)))
	jwks := f.Signer.PublicKeys()
	mux.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jwks)
	})
	url = ServeH2C(t, mux)
	return url, url + "/.well-known/jwks.json"
}

// Authenticator builds what a gateway's main builds: a JWT verifier on the
// JWKS and a membership version lookup (Redis if rdb is set, then iam).
func Authenticator(t testing.TB, jwksURL string, iam iamv1connect.IamServiceClient, rdb *redis.Client, l logger.Logger) *authn.JWTAuthenticator {
	t.Helper()
	jwks := token.NewRemoteJWKS(jwksURL, nil)
	if err := jwks.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	return authn.NewJWTAuthenticator(
		token.NewVerifier(jwks, Issuer, Audience),
		authn.NewVersionLookup(rdb, iam, time.Hour, l),
	)
}

// GRPCClient mirrors what rpcc builds: gRPC over h2c + core/rpc client interceptors.
func GRPCClient[T any](url string, ctor func(connect.HTTPClient, string, ...connect.ClientOption) T) T {
	p := new(http.Protocols)
	p.SetUnencryptedHTTP2(true)
	return ctor(
		&http.Client{Transport: &http.Transport{Protocols: p}},
		url,
		connect.WithGRPC(),
		connect.WithInterceptors(
			rpc.ClientMetadataInterceptor(),
			rpc.ClientTimeoutInterceptor(2*time.Second),
			rpc.ClientErrorInterceptor(),
		),
	)
}

// ServeH2C serves h over HTTP/1 and unencrypted HTTP/2 until the test ends.
func ServeH2C(t testing.TB, h http.Handler) string {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	srv.Config.Protocols = new(http.Protocols)
	srv.Config.Protocols.SetHTTP1(true)
	srv.Config.Protocols.SetUnencryptedHTTP2(true)
	srv.Start()
	t.Cleanup(srv.Close)
	return srv.URL
}
