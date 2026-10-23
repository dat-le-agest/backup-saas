package logger

import (
	"context"

	"core/reqctx"

	"go.opentelemetry.io/otel/trace"
)

// WithContext returns l enriched with what ctx knows about the request:
// request_id, trace_id/span_id (OpenTelemetry), app_id, user_id and tenant_id.
// Use it for any log line written while handling a request so logs can be
// correlated with each other and with traces:
//
//	logger.WithContext(ctx, s.logger).Warn("refresh token reuse detected")
func WithContext(ctx context.Context, l Logger) Logger {
	fields := Fields{}
	if id := reqctx.RequestIDFrom(ctx); id != "" {
		fields["request_id"] = id
	}
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		fields["trace_id"] = sc.TraceID().String()
		fields["span_id"] = sc.SpanID().String()
	}
	if id, ok := reqctx.IdentityFrom(ctx); ok {
		if id.AppID != "" {
			fields["app_id"] = id.AppID
		}
		if id.UserID != "" {
			fields["user_id"] = id.UserID
		}
		if id.TenantID != "" {
			fields["tenant_id"] = id.TenantID
		}
	}
	if len(fields) == 0 {
		return l
	}
	return l.WithFields(fields)
}
