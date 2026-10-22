package main

import (
	"fmt"
	"net/http"

	"core/apperr"
	"core/di"
	"core/logger"
	"core/response"
	"notes-gateway/config"
	"notes-gateway/internal/handler"
	"sdk/httpx"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
)

type routeOptions struct {
	auth   httpx.Authenticator
	logger logger.Logger
}

// registerRoutes is shared by main and tests so both run the same middleware chain.
// Panic recovery is already installed by ginc. Every route needs an access token:
// sign-in and tenants are on the platform gateway.
func registerRoutes(r *gin.Engine, c *di.Container, opts routeOptions) {
	r.Use(
		// first: the span must exist before logs and handlers run
		otelgin.Middleware(config.ServiceName, otelgin.WithFilter(func(r *http.Request) bool {
			return r.URL.Path != "/healthz"
		})),
		httpx.RequestID(),
		httpx.AccessLog(opts.logger),
	)

	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	v1 := r.Group("/v1", httpx.Auth(opts.auth))
	di.MustGet[*handler.NoteHandler](c).RegisterRoutes(v1)

	r.NoRoute(func(c *gin.Context) {
		response.Fail(c, fmt.Errorf("%w: route %s %s", apperr.ErrNotFound, c.Request.Method, c.Request.URL.Path))
	})
}
