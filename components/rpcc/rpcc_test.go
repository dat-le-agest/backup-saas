package rpcc

import (
	"context"
	"errors"
	"flag"
	"net"
	"net/http"
	"strconv"
	"testing"

	"core/apperr"
	"core/components/ginc"
	"core/logger"
	"core/reqctx"
	"core/rpc"
	"core/sctx"

	"connectrpc.com/connect"
	"github.com/gin-gonic/gin"
	"google.golang.org/protobuf/types/known/wrapperspb"
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

type fakeSC struct{ sctx.ServiceContext }

func (fakeSC) EnvName() string             { return sctx.PrdEnv }
func (fakeSC) Logger(string) logger.Logger { return nopLogger{} }

func TestActivateValidatesURL(t *testing.T) {
	for _, u := range []string{"", "note:3000", "ftp://note:3000", "http://"} {
		c := NewClient("note-client", "note-service")
		c.rawURL = u
		if err := c.Activate(fakeSC{}); err == nil {
			t.Errorf("url %q: expected error", u)
		}
	}
}

// Generated-client shaped constructor so New[T] is exercised like real code.
type echoClient struct {
	*connect.Client[wrapperspb.StringValue, wrapperspb.StringValue]
}

const procedure = "/test.v1.EchoService/Echo"

func newEchoClient(h connect.HTTPClient, baseURL string, opts ...connect.ClientOption) echoClient {
	return echoClient{connect.NewClient[wrapperspb.StringValue, wrapperspb.StringValue](h, baseURL+procedure, opts...)}
}

// Full path used by gateway → service: rpcc client (gRPC over h2c) → ginc server → Mount → Connect handler.
func TestGRPCOverGinH2C(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	server := ginc.NewGin("gin")
	server.InitFlags() // registers on flag.CommandLine, only done once in this test binary
	if err := flag.Set("gin-port", strconv.Itoa(port)); err != nil {
		t.Fatal(err)
	}
	if err := server.Activate(fakeSC{}); err != nil {
		t.Fatal(err)
	}

	handler := connect.NewUnaryHandler(procedure,
		func(ctx context.Context, req *connect.Request[wrapperspb.StringValue]) (*connect.Response[wrapperspb.StringValue], error) {
			if req.Msg.GetValue() == "missing" {
				return nil, apperr.ErrNotFound
			}
			// gRPC must arrive over HTTP/2
			id, _ := reqctx.IdentityFrom(ctx)
			return connect.NewResponse(wrapperspb.String(req.Peer().Protocol + "|" + id.UserID)), nil
		},
		rpc.HandlerOptions(nopLogger{}),
	)
	ginc.Mount(server.GetRouter(), "/test.v1.EchoService/", handler)
	server.GetRouter().GET("/healthz", func(c *gin.Context) { c.String(200, c.Request.Proto) })

	if err := server.Run(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Stop() })

	cc := NewClient("echo-client", "echo-service")
	cc.rawURL = "http://127.0.0.1:" + strconv.Itoa(port)
	cc.timeout = 5
	if err := cc.Activate(fakeSC{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cc.Stop() })

	client := New(cc, newEchoClient)
	ctx := reqctx.WithIdentity(context.Background(), reqctx.Identity{UserID: "u1"})

	res, err := client.CallUnary(ctx, connect.NewRequest(wrapperspb.String("hi")))
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Msg.GetValue(); got != connect.ProtocolGRPC+"|u1" {
		t.Fatalf("got %q", got)
	}

	if _, err := client.CallUnary(ctx, connect.NewRequest(wrapperspb.String("missing"))); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	// plain HTTP/1.1 still works on the same port
	r, err := http.Get(cc.rawURL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("healthz %d", r.StatusCode)
	}
}
