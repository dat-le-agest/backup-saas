// Package httpx is the HTTP middleware stack shared by gateways and the admin
// service: request id, access log, authentication and rate limiting, plus
// small request helpers.
package httpx

import (
	"fmt"

	"core/apperr"
	"core/response"

	"github.com/gin-gonic/gin"
)

// ErrInvalidBody is the error for a request body that isn't valid JSON for its shape.
var ErrInvalidBody = fmt.Errorf("%w: invalid JSON body", apperr.ErrInvalid)

// BindJSON decodes the body into v, or answers 400 ErrInvalidBody and returns false.
func BindJSON(c *gin.Context, v any) bool {
	if err := c.ShouldBindJSON(v); err != nil {
		response.Fail(c, ErrInvalidBody)
		return false
	}
	return true
}
