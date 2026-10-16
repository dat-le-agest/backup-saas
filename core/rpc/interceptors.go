package rpc

import (
	"context"
	"errors"
	"net/http"
	"runtime/debug"
	"time"

	"core/logger"
	"core/reqctx"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
)

// HandlerOptions returns the options every service should pass to its generated
// New<Service>Handler: identity extraction, access log, error translation and
// panic recovery. Interceptors run outermost first: the access log sees the
// final (translated) code.
func HandlerOptions(l logger.Logger) connect.HandlerOption {
	interceptors := []connect.Interceptor{
		ServerMetadataInterceptor(),
		ServerLoggingInterceptor(l),
		ServerErrorInterceptor(l),
	}
	// tracing + RPC metrics, outermost so logs see the span. Services are internal:
	// trust the caller's span (gateway) as parent so traces stay connected.
	if otelInterceptor, err := otelconnect.NewInterceptor(otelconnect.WithTrustRemote()); err != nil {
		l.Errorf("otelconnect: %v", err)
	} else {
		interceptors = append([]connect.Interceptor{otelInterceptor}, interceptors...)
	}

	return connect.WithHandlerOptions(
		connect.WithRecover(recoverPanic(l)),
		connect.WithInterceptors(interceptors...),
	)
}

// ClientTelemetryInterceptor creates client spans, propagates the trace context
// (traceparent) and records client RPC metrics.
func ClientTelemetryInterceptor() (connect.Interceptor, error) {
	return otelconnect.NewInterceptor()
}

// recoverPanic turns a panic in a handler into an Internal error for the
// caller (instead of a broken stream) and logs the stack.
func recoverPanic(l logger.Logger) func(context.Context, connect.Spec, http.Header, any) error {
	return func(ctx context.Context, spec connect.Spec, _ http.Header, p any) error {
		logger.WithContext(ctx, l).WithFields(logger.Fields{
			"procedure": spec.Procedure,
			"stack":     string(debug.Stack()),
		}).Errorf("panic recovered: %v", p)
		return connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
}

// ServerLoggingInterceptor writes one line per RPC: procedure, code, duration,
// plus request/trace/user/tenant from the context. Client errors are warnings,
// server errors are errors.
func ServerLoggingInterceptor(l logger.Logger) connect.Interceptor {
	write := func(ctx context.Context, procedure, protocol string, start time.Time, err error) {
		code := "ok"
		if err != nil {
			code = connect.CodeOf(err).String()
		}
		entry := logger.WithContext(ctx, l).WithFields(logger.Fields{
			"procedure":   procedure,
			"code":        code,
			"protocol":    protocol,
			"duration_ms": time.Since(start).Milliseconds(),
		})
		switch {
		case err == nil:
			entry.Info("rpc")
		case isServerError(connect.CodeOf(err)):
			entry.Errorf("rpc failed: %v", err)
		default:
			entry.Warnf("rpc rejected: %v", err)
		}
	}

	return &errorInterceptor{
		unary: func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
				start := time.Now()
				res, err := next(ctx, req)
				write(ctx, req.Spec().Procedure, req.Peer().Protocol, start, err)
				return res, err
			}
		},
		streamingHandler: func(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
			return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
				start := time.Now()
				err := next(ctx, conn)
				write(ctx, conn.Spec().Procedure, conn.Peer().Protocol, start, err)
				return err
			}
		},
	}
}

func isServerError(code connect.Code) bool {
	switch code {
	case connect.CodeInternal, connect.CodeUnknown, connect.CodeDataLoss,
		connect.CodeUnavailable, connect.CodeUnimplemented, connect.CodeDeadlineExceeded:
		return true
	}
	return false
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
			logger.WithContext(ctx, l).WithFields(logger.Fields{"procedure": procedure}).
				Errorf("internal error: %v", err)
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
