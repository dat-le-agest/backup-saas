package otelc

import (
	"context"
	"testing"

	"core/logger"
	"core/sctx"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
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

func (fakeSC) Logger(string) logger.Logger { return nopLogger{} }
func (fakeSC) GetName() string             { return "svc" }
func (fakeSC) EnvName() string             { return sctx.DevEnv }

func TestDisabledWithoutEndpoint(t *testing.T) {
	o := NewOtel("otel")
	o.sampleRatio, o.metricInterval = 1, 1000
	if err := o.Activate(fakeSC{}); err != nil {
		t.Fatal(err)
	}
	if o.tp != nil {
		t.Fatal("providers created without endpoint")
	}
	if err := o.Stop(); err != nil {
		t.Fatal(err)
	}
}

// Activation with export enabled builds the resource and providers; exporters
// connect lazily, so no collector is needed.
func TestEnabled(t *testing.T) {
	prev := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	o := NewOtel("otel")
	o.endpoint, o.sampleRatio, o.metricInterval = "http://127.0.0.1:1", 0.5, 1000
	if err := o.Activate(fakeSC{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider); !ok {
		t.Fatal("global tracer provider not set")
	}
	_, span := otel.Tracer("test").Start(context.Background(), "x")
	span.End()

	// Stop flushes; the exporter can't reach 127.0.0.1:1, which must not hang or panic
	_ = o.Stop()
}

func TestRejectsBadSampleRatio(t *testing.T) {
	o := NewOtel("otel")
	o.endpoint, o.sampleRatio = "http://127.0.0.1:1", 2
	if err := o.Activate(fakeSC{}); err == nil {
		t.Fatal("expected error")
	}
}
