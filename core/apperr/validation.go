package apperr

import "strings"

// FieldViolation describes why one input field is invalid. Field uses the
// public (snake_case) name, e.g. "title", "new_password".
type FieldViolation struct {
	Field       string
	Description string
}

// ValidationError is an ErrInvalid with per-field details, so clients can show
// the message next to the right form field. It travels across gRPC as a
// google.rpc.BadRequest detail and ends up in the REST error body as "details".
type ValidationError struct {
	Violations []FieldViolation
}

// Invalid returns an ErrInvalid for one field:
//
//	apperr.Invalid("title", "is required") // "invalid input: title is required"
func Invalid(field, description string) error {
	return &ValidationError{Violations: []FieldViolation{{Field: field, Description: description}}}
}

func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Violations))
	for _, v := range e.Violations {
		parts = append(parts, v.Field+" "+v.Description)
	}
	return ErrInvalid.Error() + ": " + strings.Join(parts, "; ")
}

func (e *ValidationError) Unwrap() error { return ErrInvalid }
