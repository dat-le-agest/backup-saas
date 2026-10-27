// Package httpxtest helps test HTTP gateways: serve upstream fakes over h2c,
// build clients like rpcc does, and send JSON requests to a handler (Do).
package httpxtest

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"core/rpc"
	"core/token"

	"connectrpc.com/connect"
)

// GRPCClient mirrors what rpcc builds: gRPC over h2c + core/rpc client
// interceptors, signing internal tokens for audience with signer (nil: none,
// like a client without rpcc.WithIdentitySigner).
func GRPCClient[T any](url string, ctor func(connect.HTTPClient, string, ...connect.ClientOption) T, signer *token.Signer, audience string) T {
	p := new(http.Protocols)
	p.SetUnencryptedHTTP2(true)
	interceptors := []connect.Interceptor{rpc.ClientMetadataInterceptor()}
	if signer != nil {
		interceptors = append(interceptors, rpc.ClientIdentityInterceptor(signer, audience))
	}
	interceptors = append(interceptors, rpc.ClientTimeoutInterceptor(2*time.Second), rpc.ClientErrorInterceptor())
	return ctor(
		&http.Client{Transport: &http.Transport{Protocols: p}},
		url,
		connect.WithGRPC(),
		connect.WithInterceptors(interceptors...),
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
