package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"

	"core/reqctx"

	"github.com/gin-gonic/gin"
)

// accept ids from a trusted edge (load balancer, tracing) only if they look sane
var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{8,64}$`)

// RequestID reuses a well-formed incoming X-Request-Id or generates one, stores it in
// the request context (forwarded upstream by core/rpc) and echoes it in the response.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(reqctx.HeaderRequestID)
		if !validRequestID.MatchString(id) {
			id = newRequestID()
		}

		c.Request = c.Request.WithContext(reqctx.WithRequestID(c.Request.Context(), id))
		c.Header(reqctx.HeaderRequestID, id)
		c.Next()
	}
}

func newRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
