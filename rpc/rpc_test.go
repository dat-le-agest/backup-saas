package rpc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"core/apperr"
	"core/logger"
	"core/reqctx"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type captureLogger struct {
	mu   sync.Mutex
	msgs []string
}

func (l *captureLogger) Debug(...interface{})                   {}
func (l *captureLogger) Info(...interface{})                    {}
func (l *captureLogger) Warn(...interface{})                    {}
func (l *captureLogger) Error(...interface{})                   {}
func (l *captureLogger) Debugf(string, ...interface{})          {}
func (l *captureLogger) Infof(string, ...interface{})           {}
func (l *captureLogger) Warnf(string, ...interface{})           {}
func (l *captureLogger) With(string, interface{}) logger.Logger { return l }
func (l *captureLogger) WithFields(logger.Fields) logger.Logger { return l }
func (l *captureLogger) Errorf(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.msgs = append(l.msgs, fmt.Sprintf(format, args...))
}

const procedure = "/test.v1.EchoService/Echo"

// echo returns "user|tenant|request-id" from the context, or the error named in the request.
func echo(ctx context.Context, req *connect.Request[wrapperspb.StringValue]) (*connect.Response[wrapperspb.StringValue], error) {
	switch req.Msg.GetValue() {
	case "invalid":
		return nil, fmt.Errorf("%w: title is required", apperr.ErrInvalid)
	case "missing":
		return nil, apperr.ErrNotFound
	case "boom":
		return nil, errors.New("sql: connection refused")
	case "slow":
		<-ctx.Done()
		return nil, ctx.Err()
	}
	id, _ := reqctx.IdentityFrom(ctx)
	return connect.NewResponse(wrapperspb.String(
		id.UserID + "|" + id.TenantID + "|" + id.Role + "|" + reqctx.RequestIDFrom(ctx),
	)), nil
}

func newServer(t *testing.T, l logger.Logger) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(procedure, connect.NewUnaryHandler(procedure, echo, HandlerOptions(l)))

	srv := httptest.NewUnstartedServer(mux)
	srv.Config.Protocols = new(http.Protocols)
	srv.Config.Protocols.SetUnencryptedHTTP2(true)
	srv.Start()
	t.Cleanup(srv.Close)
	return srv.URL
}

func newClient(url string, timeout time.Duration) *connect.Client[wrapperspb.StringValue, wrapperspb.StringValue] {
	p := new(http.Protocols)
	p.SetUnencryptedHTTP2(true)
	return connect.NewClient[wrapperspb.StringValue, wrapperspb.StringValue](
		&http.Client{Transport: &http.Transport{Protocols: p}},
		url+procedure,
		connect.WithGRPC(),
		connect.WithInterceptors(
			ClientMetadataInterceptor(),
			ClientTimeoutInterceptor(timeout),
			ClientErrorInterceptor(),
		),
	)
}

func call(c *connect.Client[wrapperspb.StringValue, wrapperspb.StringValue], ctx context.Context, v string) (string, error) {
	res, err := c.CallUnary(ctx, connect.NewRequest(wrapperspb.String(v)))
	if err != nil {
		return "", err
	}
	return res.Msg.GetValue(), nil
}

func TestMetadataPropagation(t *testing.T) {
	c := newClient(newServer(t, &captureLogger{}), time.Second)

	ctx := reqctx.WithIdentity(context.Background(), reqctx.Identity{UserID: "u1", TenantID: "t1", Role: "admin"})
	ctx = reqctx.WithRequestID(ctx, "req-42")
	got, err := call(c, ctx, "")
	if err != nil || got != "u1|t1|admin|req-42" {
		t.Fatalf("got %q, %v", got, err)
	}

	got, err = call(c, context.Background(), "")
	if err != nil || got != "|||" {
		t.Fatalf("anonymous: got %q, %v", got, err)
	}
}

func TestErrorRoundTrip(t *testing.T) {
	l := &captureLogger{}
	c := newClient(newServer(t, l), 200*time.Millisecond)
	ctx := reqctx.WithRequestID(context.Background(), "req-7")

	cases := []struct {
		in       string
		sentinel error
		code     connect.Code
		msg      string
	}{
		{"invalid", apperr.ErrInvalid, connect.CodeInvalidArgument, "invalid input: title is required"},
		{"missing", apperr.ErrNotFound, connect.CodeNotFound, "not found"},
		{"slow", context.DeadlineExceeded, connect.CodeDeadlineExceeded, ""},
	}
	for _, tc := range cases {
		_, err := call(c, ctx, tc.in)
		if !errors.Is(err, tc.sentinel) {
			t.Errorf("%s: errors.Is(%v, %v) = false", tc.in, err, tc.sentinel)
		}
		if connect.CodeOf(err) != tc.code {
			t.Errorf("%s: code %v want %v", tc.in, connect.CodeOf(err), tc.code)
		}
		if tc.msg != "" && err.Error() != tc.msg {
			t.Errorf("%s: message %q want %q", tc.in, err.Error(), tc.msg)
		}
	}

	// unknown errors are hidden from the caller and logged on the server
	_, err := call(c, ctx, "boom")
	if connect.CodeOf(err) != connect.CodeInternal || strings.Contains(err.Error(), "sql") {
		t.Fatalf("internal error leaked: %v", err)
	}
	if status, _ := apperr.Public(err); status != http.StatusInternalServerError {
		t.Fatalf("status %d", status)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.msgs) != 1 || !strings.Contains(l.msgs[0], "sql: connection refused") {
		t.Fatalf("server log: %v", l.msgs)
	}
}

func TestCallerDeadlineWins(t *testing.T) {
	c := newClient(newServer(t, &captureLogger{}), time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := call(c, ctx, "slow"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("caller deadline ignored")
	}
}
