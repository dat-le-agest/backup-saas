package rpc

import (
	"context"
	"errors"

	"core/apperr"

	"connectrpc.com/connect"
)

var codes = []struct {
	err  error
	code connect.Code
}{
	{apperr.ErrInvalid, connect.CodeInvalidArgument},
	{apperr.ErrNotFound, connect.CodeNotFound},
	{apperr.ErrConflict, connect.CodeAlreadyExists},
	{apperr.ErrUnauthenticated, connect.CodeUnauthenticated},
	{apperr.ErrForbidden, connect.CodePermissionDenied},
	{apperr.ErrTooManyRequests, connect.CodeResourceExhausted},
	{apperr.ErrUnavailable, connect.CodeUnavailable},
	{context.Canceled, connect.CodeCanceled},
	{context.DeadlineExceeded, connect.CodeDeadlineExceeded},
}

// ToConnectError converts an error returned by service code into a *connect.Error.
// Known sentinels keep their message; anything else becomes a generic Internal
// error, and ok=false tells the caller it should be logged.
func ToConnectError(err error) (cerr *connect.Error, ok bool) {
	if err == nil {
		return nil, true
	}
	if errors.As(err, &cerr) {
		return cerr, true
	}
	for _, c := range codes {
		if errors.Is(err, c.err) {
			return connect.NewError(c.code, errors.New(err.Error())), true
		}
	}
	return connect.NewError(connect.CodeInternal, errors.New("internal error")), false
}

// FromConnectError converts an error received from an upstream service back into
// the apperr vocabulary, so callers can use errors.Is(err, apperr.ErrNotFound).
// The original *connect.Error stays reachable through errors.As.
func FromConnectError(err error) error {
	var cerr *connect.Error
	if !errors.As(err, &cerr) {
		return err
	}
	for _, c := range codes {
		if cerr.Code() == c.code {
			return &remoteError{sentinel: c.err, cerr: cerr}
		}
	}
	return err
}

type remoteError struct {
	sentinel error
	cerr     *connect.Error
}

// Error returns the upstream message as-is ("invalid input: title is required").
func (e *remoteError) Error() string   { return e.cerr.Message() }
func (e *remoteError) Unwrap() []error { return []error{e.sentinel, e.cerr} }
