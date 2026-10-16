package rpc

import (
	"context"
	"errors"

	"core/apperr"

	"connectrpc.com/connect"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
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
			cerr = connect.NewError(c.code, errors.New(err.Error()))
			addFieldViolations(cerr, err)
			return cerr, true
		}
	}
	return connect.NewError(connect.CodeInternal, errors.New("internal error")), false
}

// addFieldViolations attaches apperr.ValidationError details as the standard
// google.rpc.BadRequest detail.
func addFieldViolations(cerr *connect.Error, err error) {
	var ve *apperr.ValidationError
	if !errors.As(err, &ve) {
		return
	}
	br := &errdetails.BadRequest{}
	for _, v := range ve.Violations {
		br.FieldViolations = append(br.FieldViolations, &errdetails.BadRequest_FieldViolation{
			Field: v.Field, Description: v.Description,
		})
	}
	if detail, derr := connect.NewErrorDetail(br); derr == nil {
		cerr.AddDetail(detail)
	}
}

// fieldViolations reads a google.rpc.BadRequest detail back into an apperr.ValidationError.
func fieldViolations(cerr *connect.Error) *apperr.ValidationError {
	for _, d := range cerr.Details() {
		msg, err := d.Value()
		if err != nil {
			continue
		}
		br, ok := msg.(*errdetails.BadRequest)
		if !ok {
			continue
		}
		ve := &apperr.ValidationError{}
		for _, v := range br.GetFieldViolations() {
			ve.Violations = append(ve.Violations, apperr.FieldViolation{Field: v.GetField(), Description: v.GetDescription()})
		}
		return ve
	}
	return nil
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
			sentinel := c.err
			if c.code == connect.CodeInvalidArgument {
				if ve := fieldViolations(cerr); ve != nil {
					sentinel = ve // still errors.Is(err, apperr.ErrInvalid)
				}
			}
			return &remoteError{sentinel: sentinel, cerr: cerr}
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
