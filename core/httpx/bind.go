// Package httpx is the HTTP middleware stack shared by gateways and the admin
// service: request id, access log, authentication and rate limiting, plus
// small request helpers.
package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"core/apperr"
	"core/response"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
)

// ErrInvalidBody is the error for a request body that isn't valid JSON for its shape.
var ErrInvalidBody = fmt.Errorf("%w: invalid JSON body", apperr.ErrInvalid)

// ErrInvalidQuery is the error for query parameters that don't parse into their types.
var ErrInvalidQuery = fmt.Errorf("%w: invalid query parameters", apperr.ErrInvalid)

// BindJSON decodes the body into v and validates it against its `validate` tags
// (go-playground/validator), or answers 400 and returns false:
//
//	type CreateNoteRequest struct {
//		Title string `json:"title" validate:"required,max=255"`
//	}
//
// Tag violations and fields of the wrong JSON type become an apperr.ValidationError
// (per-field "details", public field names); a malformed body is ErrInvalidBody.
func BindJSON(c *gin.Context, v any) bool {
	if err := c.ShouldBindJSON(v); err != nil {
		response.Fail(c, bindError(err, ErrInvalidBody))
		return false
	}
	return validated(c, v)
}

// BindQuery is BindJSON for query parameters (`form` tags).
func BindQuery(c *gin.Context, v any) bool {
	if err := c.ShouldBindQuery(v); err != nil {
		response.Fail(c, bindError(err, ErrInvalidQuery))
		return false
	}
	return validated(c, v)
}

// Validate checks v against its `validate` tags: nil, or an apperr.ValidationError
// with one violation per invalid field.
func Validate(v any) error {
	if err := validate.Struct(v); err != nil {
		return bindError(err, err) // e.g. v is not a struct: a bug, 500
	}
	return nil
}

func validated(c *gin.Context, v any) bool {
	if err := Validate(v); err != nil {
		response.Fail(c, err)
		return false
	}
	return true
}

// validate reports fields by their json (or form) name, the one clients send,
// instead of the Go field name.
var validate = newValidator()

func newValidator() *validator.Validate {
	v := validator.New(validator.WithRequiredStructEnabled())
	v.RegisterTagNameFunc(func(f reflect.StructField) string {
		for _, tag := range []string{"json", "form"} {
			name, _, _ := strings.Cut(f.Tag.Get(tag), ",")
			if name == "-" {
				return ""
			}
			if name != "" {
				return name
			}
		}
		return f.Name
	})
	return v
}

// bindError turns a binding error into a field-level apperr.ValidationError
// when it names fields, fallback otherwise.
func bindError(err, fallback error) error {
	var verrs validator.ValidationErrors
	if errors.As(err, &verrs) {
		ve := &apperr.ValidationError{}
		for _, fe := range verrs {
			ve.Violations = append(ve.Violations, apperr.FieldViolation{Field: fieldPath(fe), Description: describe(fe)})
		}
		return ve
	}
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) && te.Field != "" {
		return apperr.Invalid(te.Field, "must be "+jsonType(te.Type))
	}
	return fallback
}

// fieldPath is the field's path without the request type: "title", "items[0].name".
func fieldPath(fe validator.FieldError) string {
	if _, path, ok := strings.Cut(fe.Namespace(), "."); ok {
		return path
	}
	return fe.Field()
}

func describe(fe validator.FieldError) string {
	unit := ""
	switch fe.Kind() {
	case reflect.String:
		unit = " characters"
	case reflect.Slice, reflect.Array, reflect.Map:
		unit = " items"
	}
	if fe.Param() == "1" {
		unit = strings.TrimSuffix(unit, "s")
	}
	switch fe.Tag() {
	case "required":
		return "is required"
	case "min", "gte":
		return fmt.Sprintf("must be at least %s%s", fe.Param(), unit)
	case "max", "lte":
		return fmt.Sprintf("must be at most %s%s", fe.Param(), unit)
	case "gt":
		return fmt.Sprintf("must be greater than %s%s", fe.Param(), unit)
	case "lt":
		return fmt.Sprintf("must be less than %s%s", fe.Param(), unit)
	case "len":
		return fmt.Sprintf("must be exactly %s%s", fe.Param(), unit)
	case "oneof":
		return "must be one of " + strings.ReplaceAll(fe.Param(), " ", ", ")
	case "email":
		return "is not a valid email address"
	case "url", "http_url":
		return "is not a valid URL"
	case "uuid", "uuid4":
		return "is not a valid UUID"
	}
	return "is invalid (" + fe.Tag() + ")"
}

func jsonType(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "a boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "an integer"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Slice, reflect.Array:
		return "an array"
	case reflect.Struct, reflect.Map:
		return "an object"
	}
	return "of type " + t.String()
}
