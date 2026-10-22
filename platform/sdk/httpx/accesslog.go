package httpx

import (
	"net/http"
	"time"

	"core/logger"

	"github.com/gin-gonic/gin"
)

// AccessLog writes one line per request. 5xx responses are logged as errors
// together with the underlying cause attached via c.Error (see response.Fail).
func AccessLog(l logger.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		// request_id, trace_id, user_id, tenant_id come from the context
		entry := logger.WithContext(c.Request.Context(), l).WithFields(logger.Fields{
			"method":     c.Request.Method,
			"path":       c.Request.URL.Path,
			"route":      c.FullPath(),
			"status":     c.Writer.Status(),
			"latency_ms": time.Since(start).Milliseconds(),
		})
		switch {
		case c.Writer.Status() >= http.StatusInternalServerError:
			entry.Errorf("request failed: %s", c.Errors.String())
		case c.Writer.Status() >= http.StatusBadRequest:
			entry.Warn("request rejected")
		default:
			entry.Info("request")
		}
	}
}
