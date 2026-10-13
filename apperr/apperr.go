// Package apperr defines the error vocabulary shared by every layer and service.
//
// Inside a service, code returns (or wraps) these sentinels:
//
//	return fmt.Errorf("%w: title is required", apperr.ErrInvalid)
//
// Boundaries translate them: package rpc maps them to/from gRPC codes,
// HTTPStatus maps them to HTTP status codes for REST responses.
package apperr

import (
	"context"
	"errors"
	"net/http"
)

var (
	ErrInvalid         = errors.New("invalid input")
	ErrNotFound        = errors.New("not found")
	ErrConflict        = errors.New("already exists")
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrForbidden       = errors.New("forbidden")
	ErrTooManyRequests = errors.New("too many requests")
	ErrUnavailable     = errors.New("service unavailable")
)

var mappings = []struct {
	err    error
	status int
	code   string
}{
	{ErrInvalid, http.StatusBadRequest, "invalid_input"},
	{ErrNotFound, http.StatusNotFound, "not_found"},
	{ErrConflict, http.StatusConflict, "already_exists"},
	{ErrUnauthenticated, http.StatusUnauthorized, "unauthenticated"},
	{ErrForbidden, http.StatusForbidden, "forbidden"},
	{ErrTooManyRequests, http.StatusTooManyRequests, "too_many_requests"},
	{ErrUnavailable, http.StatusServiceUnavailable, "unavailable"},
	{context.DeadlineExceeded, http.StatusGatewayTimeout, "timeout"},
}

// HTTPStatus returns the HTTP status for err, 500 if it is not a known sentinel.
func HTTPStatus(err error) int {
	for _, m := range mappings {
		if errors.Is(err, m.err) {
			return m.status
		}
	}
	return http.StatusInternalServerError
}

// Code returns a stable, machine-readable error code for public APIs
// ("not_found", "invalid_input"...), "internal" if err is not a known sentinel.
func Code(err error) string {
	for _, m := range mappings {
		if errors.Is(err, m.err) {
			return m.code
		}
	}
	return "internal"
}

// Public returns the status and a message that is safe to show to clients.
// Messages of 5xx errors are replaced so internals don't leak.
func Public(err error) (status int, message string) {
	status = HTTPStatus(err)
	if status >= http.StatusInternalServerError {
		return status, http.StatusText(status)
	}
	return status, err.Error()
}
