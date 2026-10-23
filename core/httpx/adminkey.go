package httpx

import (
	"crypto/subtle"
	"fmt"
	"strings"

	"core/apperr"
	"core/response"

	"github.com/gin-gonic/gin"
)

var errAdminKey = fmt.Errorf(`%w: expected "Authorization: Bearer <admin key>"`, apperr.ErrUnauthenticated)

// AdminKey guards an app's internal admin API: every request must carry
// "Authorization: Bearer <key>". An empty key refuses every request.
func AdminKey(key string) gin.HandlerFunc {
	k := []byte(key)
	return func(c *gin.Context) {
		scheme, raw, ok := strings.Cut(c.GetHeader("Authorization"), " ")
		if len(k) == 0 || !ok || !strings.EqualFold(scheme, "Bearer") ||
			subtle.ConstantTimeCompare([]byte(strings.TrimSpace(raw)), k) != 1 {
			response.Fail(c, errAdminKey)
			return
		}
		c.Next()
	}
}
