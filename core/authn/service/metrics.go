package service

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Business metrics of iam, exported through the global OpenTelemetry meter
// (core/components/otelc). Prometheus names: iam_auth_logins_total, ...
type authMetrics struct {
	logins        metric.Int64Counter
	registrations metric.Int64Counter
	refreshReuse  metric.Int64Counter
}

func newAuthMetrics() *authMetrics {
	meter := otel.Meter("iam")
	m := &authMetrics{}
	m.logins, _ = meter.Int64Counter("iam.auth.logins",
		metric.WithDescription("Password login attempts by result (success, invalid_credentials, locked)"))
	m.registrations, _ = meter.Int64Counter("iam.auth.registrations",
		metric.WithDescription("Accounts registered"))
	m.refreshReuse, _ = meter.Int64Counter("iam.auth.refresh_token_reuse",
		metric.WithDescription("Refresh tokens presented twice (possible theft); the family was revoked"))
	return m
}

func (m *authMetrics) login(ctx context.Context, result string) {
	m.logins.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}
