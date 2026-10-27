package httpx

import (
	"net/http"

	"core/reqctx"
	"core/response"

	"github.com/gin-gonic/gin"
)

// Authenticator turns an incoming request into an identity.
// Return an error wrapping apperr.ErrUnauthenticated to reject the request.
type Authenticator interface {
	Authenticate(r *http.Request) (reqctx.Identity, error)
}

// Auth rejects requests without a valid identity and stores the identity in the
// request context. core/rpc forwards it upstream in a signed internal token
// (X-Internal-Token); headers sent by the client itself are never forwarded,
// and services ignore identity headers, so they can't be spoofed.
func Auth(a Authenticator) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := a.Authenticate(c.Request)
		if err != nil {
			response.Fail(c, err)
			return
		}
		c.Request = c.Request.WithContext(reqctx.WithIdentity(c.Request.Context(), id))
		c.Next()
	}
}
