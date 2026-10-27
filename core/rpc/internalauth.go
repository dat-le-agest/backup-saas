package rpc

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"core/logger"
	"core/reqctx"
	"core/token"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// HeaderInternalToken carries the internal token (core/token) of a call: the
// proof that it comes from a trusted caller, and the caller's identity.
const HeaderInternalToken = "X-Internal-Token"

// Modes of InternalAuth (INTERNAL_AUTH, core/components/internalauthc).
const (
	// InternalAuthEnforce: every call needs a valid internal token; identity
	// headers (X-User-Id...) are ignored.
	InternalAuthEnforce = "enforce"
	// InternalAuthPermissive: a valid token is used, an invalid one is
	// rejected, and a call without one falls back to the identity headers
	// (logged and counted: rpc.internal_auth.fallback). For migrations only.
	InternalAuthPermissive = "permissive"
	// InternalAuthOff: identity from the headers, as sent. Dev and tests only:
	// anyone who can reach the service can claim any identity.
	InternalAuthOff = "off"
)

// InternalAuth is how a service authenticates its callers (HandlerOptions).
type InternalAuth struct {
	Mode string
	// Verifier is required unless Mode is InternalAuthOff; without it every
	// token is rejected.
	Verifier *token.InternalVerifier
}

var (
	errMissingInternalToken = errors.New("internal token required (" + HeaderInternalToken + ")")
	errInvalidInternalToken = errors.New("invalid internal token")
)

// ClientIdentityInterceptor signs an internal token for every call to the
// audience service, carrying the identity of ctx (or none, for a call without
// a caller). Identity headers are never sent: services trust the token only.
func ClientIdentityInterceptor(s *token.Signer, audience string) connect.Interceptor {
	sign := func(ctx context.Context) (string, error) {
		id, _ := reqctx.IdentityFrom(ctx)
		return s.SignInternal(audience, token.Internal{
			App: id.AppID, UserID: id.UserID, TenantID: id.TenantID, Role: id.Role,
		}, token.DefaultInternalTTL)
	}

	return &identityInterceptor{
		unary: func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
				if !req.Spec().IsClient {
					return next(ctx, req)
				}
				raw, err := sign(ctx)
				if err != nil {
					return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("sign internal token: %w", err))
				}
				req.Header().Set(HeaderInternalToken, raw)
				return next(ctx, req)
			}
		},
		streamingClient: func(next connect.StreamingClientFunc) connect.StreamingClientFunc {
			return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
				conn := next(ctx, spec)
				// without a token the service rejects the stream (Unauthenticated)
				if raw, err := sign(ctx); err == nil {
					conn.RequestHeader().Set(HeaderInternalToken, raw)
				}
				return conn
			}
		},
	}
}

// serverIdentityInterceptor authenticates the caller of every RPC and puts its
// identity in the context. Rejections are logged here: they happen outside the
// access log interceptor, which needs the identity for its own fields.
func serverIdentityInterceptor(auth InternalAuth, l logger.Logger) connect.Interceptor {
	fallbacks, _ := otel.Meter("rpc").Int64Counter("rpc.internal_auth.fallback",
		metric.WithDescription("Calls authenticated by identity headers instead of an internal token (permissive mode)"))

	resolve := func(ctx context.Context, procedure string, h http.Header) (context.Context, error) {
		if auth.Mode == InternalAuthOff {
			return withHeaderIdentity(ctx, h), nil
		}
		entry := logger.WithContext(ctx, l).WithFields(logger.Fields{"procedure": procedure})

		raw := h.Get(HeaderInternalToken)
		if raw == "" {
			if auth.Mode == InternalAuthPermissive {
				fallbacks.Add(ctx, 1)
				entry.Warn("rpc without internal token: identity taken from headers (permissive mode)")
				return withHeaderIdentity(ctx, h), nil
			}
			entry.Warn("rpc rejected: no internal token")
			return ctx, connect.NewError(connect.CodeUnauthenticated, errMissingInternalToken)
		}

		if auth.Verifier == nil {
			entry.Error("rpc rejected: internal auth has no verifier")
			return ctx, connect.NewError(connect.CodeUnauthenticated, errInvalidInternalToken)
		}
		claims, err := auth.Verifier.Verify(raw)
		if err != nil {
			entry.Warnf("rpc rejected: %v", err)
			return ctx, connect.NewError(connect.CodeUnauthenticated, errInvalidInternalToken)
		}
		if id := claims.Identity(); id.UserID != "" || id.TenantID != "" {
			ctx = reqctx.WithIdentity(ctx, reqctx.Identity{AppID: id.App, UserID: id.UserID, TenantID: id.TenantID, Role: id.Role})
		}
		return ctx, nil
	}

	return &identityInterceptor{
		unary: func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
				if req.Spec().IsClient {
					return next(ctx, req)
				}
				ctx, err := resolve(ctx, req.Spec().Procedure, req.Header())
				if err != nil {
					return nil, err
				}
				return next(ctx, req)
			}
		},
		streamingHandler: func(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
			return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
				ctx, err := resolve(ctx, conn.Spec().Procedure, conn.RequestHeader())
				if err != nil {
					return err
				}
				return next(ctx, conn)
			}
		},
	}
}

// withHeaderIdentity: the identity headers as sent (modes off and permissive).
func withHeaderIdentity(ctx context.Context, h http.Header) context.Context {
	if id := (reqctx.Identity{
		AppID:    h.Get(reqctx.HeaderAppID),
		UserID:   h.Get(reqctx.HeaderUserID),
		TenantID: h.Get(reqctx.HeaderTenantID),
		Role:     h.Get(reqctx.HeaderTenantRole),
	}); !id.IsZero() {
		ctx = reqctx.WithIdentity(ctx, id)
	}
	return ctx
}

type identityInterceptor struct {
	unary            func(connect.UnaryFunc) connect.UnaryFunc
	streamingClient  func(connect.StreamingClientFunc) connect.StreamingClientFunc
	streamingHandler func(connect.StreamingHandlerFunc) connect.StreamingHandlerFunc
}

func (i *identityInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	if i.unary == nil {
		return next
	}
	return i.unary(next)
}

func (i *identityInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	if i.streamingClient == nil {
		return next
	}
	return i.streamingClient(next)
}

func (i *identityInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	if i.streamingHandler == nil {
		return next
	}
	return i.streamingHandler(next)
}
