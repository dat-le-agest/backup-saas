package rpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"core/logger"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// fieldLogger records the fields attached to Info lines.
type fieldLogger struct {
	nopFields
	mu     *sync.Mutex
	fields logger.Fields
	lines  *[]logger.Fields
}

type nopFields struct{}

func (nopFields) Debug(...interface{})          {}
func (nopFields) Info(...interface{})           {}
func (nopFields) Warn(...interface{})           {}
func (nopFields) Error(...interface{})          {}
func (nopFields) Debugf(string, ...interface{}) {}
func (nopFields) Infof(string, ...interface{})  {}
func (nopFields) Warnf(string, ...interface{})  {}
func (nopFields) Errorf(string, ...interface{}) {}

func (l *fieldLogger) With(k string, v interface{}) logger.Logger {
	return l.WithFields(logger.Fields{k: v})
}
func (l *fieldLogger) WithFields(f logger.Fields) logger.Logger {
	merged := logger.Fields{}
	for k, v := range l.fields {
		merged[k] = v
	}
	for k, v := range f {
		merged[k] = v
	}
	return &fieldLogger{mu: l.mu, fields: merged, lines: l.lines}
}
func (l *fieldLogger) Info(...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	*l.lines = append(*l.lines, l.fields)
}

func TestTracePropagatesAndReachesLogs(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTracerProvider(prevTP); otel.SetTextMapPropagator(prevProp) })

	var lines []logger.Fields
	l := &fieldLogger{mu: &sync.Mutex{}, lines: &lines}

	mux := http.NewServeMux()
	mux.Handle(procedure, connect.NewUnaryHandler(procedure, echo, HandlerOptions(l, testAuth())))
	srv := httptest.NewUnstartedServer(mux)
	srv.Config.Protocols = new(http.Protocols)
	srv.Config.Protocols.SetUnencryptedHTTP2(true)
	srv.Start()
	defer srv.Close()

	telemetry, err := ClientTelemetryInterceptor()
	if err != nil {
		t.Fatal(err)
	}
	p := new(http.Protocols)
	p.SetUnencryptedHTTP2(true)
	client := connect.NewClient[wrapperspb.StringValue, wrapperspb.StringValue](
		&http.Client{Transport: &http.Transport{Protocols: p}}, srv.URL+procedure,
		connect.WithGRPC(), connect.WithInterceptors(telemetry, ClientMetadataInterceptor(), ClientIdentityInterceptor(testSigner, testAudience)),
	)

	// a parent span, like the gateway's HTTP server span
	ctx, parent := tp.Tracer("test").Start(context.Background(), "GET /v1/notes")
	if _, err := client.CallUnary(ctx, connect.NewRequest(wrapperspb.String(""))); err != nil {
		t.Fatal(err)
	}
	parent.End()

	traceID := parent.SpanContext().TraceID()
	var clientSpan, serverSpan sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		switch s.SpanKind().String() {
		case "client":
			clientSpan = s
		case "server":
			serverSpan = s
		}
	}
	if clientSpan == nil || serverSpan == nil {
		t.Fatalf("spans: %v", rec.Ended())
	}
	if serverSpan.SpanContext().TraceID() != traceID || clientSpan.SpanContext().TraceID() != traceID {
		t.Fatal("trace not continued across the RPC")
	}
	if serverSpan.Parent().SpanID() != clientSpan.SpanContext().SpanID() {
		t.Fatal("server span is not a child of the client span")
	}
	if !strings.Contains(serverSpan.Name(), "EchoService/Echo") {
		t.Fatalf("server span name %q", serverSpan.Name())
	}

	// the access log line carries the same trace
	if len(lines) != 1 || lines[0]["trace_id"] != traceID.String() || lines[0]["procedure"] != procedure {
		t.Fatalf("log fields: %v", lines)
	}
}
