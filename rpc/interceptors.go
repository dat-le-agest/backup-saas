package rpc

import (
	"context"
	"net/http"
	"time"

	"core/logger"
	"core/reqctx"

	"connectrpc.com/connect"
)

// HandlerOptions returns the options every service should pass to its generated
// New<Service>Handler: identity extraction and error translation.
func HandlerOptions(l logger.Logger) connect.HandlerOption {
	return connect.WithInterceptors(
		ServerMetadataInterceptor(),
		ServerErrorInterceptor(l),
	)
}

// ServerMetadataInterceptor reads identity and request id headers set by the
// gateway (or another service) into the context.
//
// The headers are trusted as-is: services must only be reachable from the
// internal network (network policy / mTLS), never directly from the internet.
func ServerMetadataInterceptor() connect.Interceptor {
	return &metadataInterceptor{
		server: func(ctx context.Context, h http.Header) context.Context {
			if id := (reqctx.Identity{
				UserID:   h.Get(reqctx.HeaderUserID),
				TenantID: h.Get(reqctx.HeaderTenantID),
				Role:     h.Get(reqctx.HeaderTenantRole),
			}); !id.IsZero() {
				ctx = reqctx.WithIdentity(ctx, id)
			}
			if rid := h.Get(reqctx.HeaderRequestID); rid != "" {
				ctx = reqctx.WithRequestID(ctx, rid)
			}
			return ctx
		},
	}
}

// ClientMetadataInterceptor forwards identity and request id from the context
// to upstream services.
func ClientMetadataInterceptor() connect.Interceptor {
	return &metadataInterceptor{
		client: func(ctx context.Context, h http.Header) {
			if id, ok := reqctx.IdentityFrom(ctx); ok {
				setIfNotEmpty(h, reqctx.HeaderUserID, id.UserID)
				setIfNotEmpty(h, reqctx.HeaderTenantID, id.TenantID)
				setIfNotEmpty(h, reqctx.HeaderTenantRole, id.Role)
			}
			setIfNotEmpty(h, reqctx.HeaderRequestID, reqctx.RequestIDFrom(ctx))
		},
	}
}

type metadataInterceptor struct {
	server func(context.Context, http.Header) context.Context
	client func(context.Context, http.Header)
}

func (i *metadataInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if req.Spec().IsClient {
			if i.client != nil {
				i.client(ctx, req.Header())
			}
		} else if i.server != nil {
			ctx = i.server(ctx, req.Header())
		}
		return next(ctx, req)
	}
}

func (i *metadataInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		if i.client != nil {
			i.client(ctx, conn.RequestHeader())
		}
		return conn
	}
}

func (i *metadataInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if i.server != nil {
			ctx = i.server(ctx, conn.RequestHeader())
		}
		return next(ctx, conn)
	}
}

// ServerErrorInterceptor lets handlers return apperr sentinels directly:
// they are converted to the matching gRPC code, unknown errors are logged
// and hidden behind a generic Internal error.
func ServerErrorInterceptor(l logger.Logger) connect.Interceptor {
	convert := func(ctx context.Context, procedure string, err error) error {
		if err == nil {
			return nil
		}
		cerr, ok := ToConnectError(err)
		if !ok {
			l.WithFields(logger.Fields{
				"procedure":  procedure,
				"request_id": reqctx.RequestIDFrom(ctx),
			}).Errorf("internal error: %v", err)
		}
		return cerr
	}

	return &errorInterceptor{
		unary: func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
				res, err := next(ctx, req)
				if err != nil {
					return nil, convert(ctx, req.Spec().Procedure, err)
				}
				return res, nil
			}
		},
		streamingHandler: func(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
			return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
				return convert(ctx, conn.Spec().Procedure, next(ctx, conn))
			}
		},
	}
}

// ClientErrorInterceptor converts upstream errors back into apperr sentinels.
func ClientErrorInterceptor() connect.Interceptor {
	return &errorInterceptor{
		unary: func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
				res, err := next(ctx, req)
				if err != nil {
					return nil, FromConnectError(err)
				}
				return res, nil
			}
		},
	}
}

// ClientTimeoutInterceptor applies a default deadline to unary calls whose
// context has none. Streaming calls are left alone since they may be long-lived.
func ClientTimeoutInterceptor(timeout time.Duration) connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if _, has := ctx.Deadline(); !has && timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			return next(ctx, req)
		}
	})
}

type errorInterceptor struct {
	unary            func(connect.UnaryFunc) connect.UnaryFunc
	streamingHandler func(connect.StreamingHandlerFunc) connect.StreamingHandlerFunc
}

func (i *errorInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	if i.unary == nil {
		return next
	}
	return i.unary(next)
}

func (i *errorInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *errorInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	if i.streamingHandler == nil {
		return next
	}
	return i.streamingHandler(next)
}

func setIfNotEmpty(h http.Header, key, value string) {
	if value != "" {
		h.Set(key, value)
	}
}
