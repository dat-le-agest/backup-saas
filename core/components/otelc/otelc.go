// Package otelc is the sctx component that sets up OpenTelemetry tracing and
// metrics, exported over OTLP/gRPC (to Grafana Alloy in the kit's compose stack).
//
//	sctx.WithComponent(otelc.NewOtel("otel")) // register first: it is stopped last and flushes
//
// Flags map to the standard OpenTelemetry environment variables:
//
//	OTEL_EXPORTER_OTLP_ENDPOINT  e.g. http://alloy:4317; empty disables export
//	OTEL_SERVICE_NAME            defaults to the service context name
//	OTEL_TRACES_SAMPLER_ARG      ratio of new traces to sample (0..1), default 1
//	OTEL_METRIC_EXPORT_INTERVAL  milliseconds, default 15000
//
// Instrumentation (gin, Connect, gorm, redis) uses the global providers set here.
// W3C trace context propagation is enabled even when export is disabled, so a
// service still forwards the trace of its callers.
package otelc

import (
	"context"
	"core/logger"
	"core/sctx"
	"errors"
	"flag"
	"fmt"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
)

type otelComponent struct {
	id             string
	endpoint       string
	serviceName    string
	sampleRatio    float64
	metricInterval int

	logger logger.Logger
	tp     *sdktrace.TracerProvider
	mp     *sdkmetric.MeterProvider
}

func NewOtel(id string) *otelComponent {
	return &otelComponent{id: id}
}

func (o *otelComponent) ID() string { return o.id }

func (o *otelComponent) InitFlags() {
	flag.StringVar(&o.endpoint, "otel-exporter-otlp-endpoint", "",
		"OTLP/gRPC endpoint for traces and metrics, e.g. http://alloy:4317 - Default empty (disabled)")
	flag.StringVar(&o.serviceName, "otel-service-name", "", "service.name resource attribute - Default service context name")
	flag.Float64Var(&o.sampleRatio, "otel-traces-sampler-arg", 1, "ratio of new traces sampled (0..1) - Default 1")
	flag.IntVar(&o.metricInterval, "otel-metric-export-interval", 15000, "metric export interval in ms - Default 15000")
}

func (o *otelComponent) Activate(sc sctx.ServiceContext) error {
	o.logger = sc.Logger(o.id)

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		o.logger.Warnf("otel: %v", err)
	}))

	if o.endpoint == "" {
		o.logger.Info("otel export disabled (no endpoint)")
		return nil
	}
	if o.sampleRatio < 0 || o.sampleRatio > 1 {
		return errors.New("otel-traces-sampler-arg must be between 0 and 1")
	}
	if o.serviceName == "" {
		o.serviceName = sc.GetName()
	}

	// schemaless: merging with resource.Default() must not depend on which
	// semconv version the SDK happens to use
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		semconv.ServiceName(o.serviceName),
		attribute.String("deployment.environment.name", sc.EnvName()),
	))
	if err != nil {
		return fmt.Errorf("otel resource: %w", err)
	}

	ctx := context.Background()
	// exporters connect lazily: a missing collector doesn't block startup
	traceExp, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpointURL(o.endpoint))
	if err != nil {
		return fmt.Errorf("otel trace exporter: %w", err)
	}
	metricExp, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithEndpointURL(o.endpoint))
	if err != nil {
		return fmt.Errorf("otel metric exporter: %w", err)
	}

	o.tp = sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
		// keep the caller's decision for propagated traces, sample new ones by ratio
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(o.sampleRatio))),
	)
	o.mp = sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp,
			sdkmetric.WithInterval(time.Duration(o.metricInterval)*time.Millisecond))),
		sdkmetric.WithResource(res),
	)
	otel.SetTracerProvider(o.tp)
	otel.SetMeterProvider(o.mp)

	if err := runtime.Start(runtime.WithMeterProvider(o.mp)); err != nil {
		return fmt.Errorf("otel runtime metrics: %w", err)
	}

	o.logger.Infof("otel export enabled (endpoint=%s, service=%s, sample=%.2f)", o.endpoint, o.serviceName, o.sampleRatio)
	return nil
}

// Stop flushes pending spans and metrics.
func (o *otelComponent) Stop() error {
	if o.tp == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return errors.Join(o.tp.Shutdown(ctx), o.mp.Shutdown(ctx))
}
