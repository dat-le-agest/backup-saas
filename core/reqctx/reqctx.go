// Package reqctx carries per-request metadata (who is calling, which tenant,
// which request) through context.Context and across service boundaries.
package reqctx

import "context"

// Header names used to propagate metadata between gateway and services.
const (
	HeaderAppID      = "X-App-Id"
	HeaderUserID     = "X-User-Id"
	HeaderTenantID   = "X-Tenant-Id"
	HeaderTenantRole = "X-Tenant-Role"
	HeaderRequestID  = "X-Request-Id"
)

// Identity is who is calling, resolved once by the gateway's authenticator.
// AppID is the app the access token was issued for (its audience); tenants
// belong to one app. Role is the caller's role inside TenantID (roles are
// defined by the platform, see platform/sdk/authz).
type Identity struct {
	AppID    string
	UserID   string
	TenantID string
	Role     string
}

func (i Identity) IsZero() bool {
	return i.UserID == "" && i.TenantID == ""
}

type identityKey struct{}
type requestIDKey struct{}

func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

func IdentityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok && !id.IsZero()
}

func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, requestID)
}

func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}
