package apperr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestPublic(t *testing.T) {
	cases := []struct {
		err        error
		wantStatus int
		wantMsg    string
	}{
		{fmt.Errorf("%w: title is required", ErrInvalid), http.StatusBadRequest, "invalid input: title is required"},
		{ErrNotFound, http.StatusNotFound, "not found"},
		{fmt.Errorf("wrap: %w", ErrForbidden), http.StatusForbidden, "wrap: forbidden"},
		{context.DeadlineExceeded, http.StatusGatewayTimeout, "Gateway Timeout"},
		{errors.New("dial tcp 10.0.0.3: connection refused"), http.StatusInternalServerError, "Internal Server Error"},
	}
	for _, tc := range cases {
		status, msg := Public(tc.err)
		if status != tc.wantStatus || msg != tc.wantMsg {
			t.Errorf("%v: got (%d, %q) want (%d, %q)", tc.err, status, msg, tc.wantStatus, tc.wantMsg)
		}
	}
}

func TestCode(t *testing.T) {
	if c := Code(fmt.Errorf("x: %w", ErrNotFound)); c != "not_found" {
		t.Errorf("got %q", c)
	}
	if c := Code(errors.New("boom")); c != "internal" {
		t.Errorf("got %q", c)
	}
}
