// Package response is the kit's JSON envelope for REST APIs (gateway, and any
// HTTP endpoint a service exposes, e.g. webhooks):
//
//	200 {"data": {...}}
//	200 {"data": [...], "paging": {...}}
//	4xx {"error": {"code": "invalid_input", "message": "...", "request_id": "...",
//	               "details": [{"field": "title", "description": "is required"}]}}
package response

import (
	"errors"
	"net/http"

	"core/apperr"
	"core/reqctx"

	"github.com/gin-gonic/gin"
)

type dataBody struct {
	Data   any `json:"data"`
	Paging any `json:"paging,omitempty"`
}

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code      string        `json:"code"`
	Message   string        `json:"message"`
	RequestID string        `json:"request_id,omitempty"`
	Details   []fieldDetail `json:"details,omitempty"`
}

type fieldDetail struct {
	Field       string `json:"field"`
	Description string `json:"description"`
}

func OK(c *gin.Context, status int, data any) {
	c.JSON(status, dataBody{Data: data})
}

func List(c *gin.Context, data any, paging any) {
	c.JSON(http.StatusOK, dataBody{Data: data, Paging: paging})
}

func NoContent(c *gin.Context) {
	c.Status(http.StatusNoContent)
}

// Fail writes err as a public error. apperr sentinels (including those converted
// from upstream gRPC codes by core/rpc) keep their status and message; anything
// else becomes a generic 500. 5xx errors are attached to the gin context so the
// access log records the real cause.
func Fail(c *gin.Context, err error) {
	status, message := apperr.Public(err)
	if status >= http.StatusInternalServerError {
		_ = c.Error(err)
	}

	body := errorDetail{
		Code:      apperr.Code(err),
		Message:   message,
		RequestID: reqctx.RequestIDFrom(c.Request.Context()),
	}
	var ve *apperr.ValidationError
	if status < http.StatusInternalServerError && errors.As(err, &ve) {
		for _, v := range ve.Violations {
			body.Details = append(body.Details, fieldDetail{Field: v.Field, Description: v.Description})
		}
	}
	c.AbortWithStatusJSON(status, errorBody{Error: body})
}
