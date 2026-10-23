// Package httpxtest helps test HTTP gateways: serve upstream fakes over h2c,
// build clients like rpcc does, and send JSON requests to a handler (Do).
package httpxtest

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"core/rpc"

	"connectrpc.com/connect"
)

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
