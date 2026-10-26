package main

import (
	"fmt"
	"net/http"

	"api/openapi"
	"core/apperr"
	"core/authn"
	"core/authz/rebac"
	"core/di"
	"core/httpx"
	"core/infra"
	"core/logger"
	"core/outbox"
	"core/response"
	"notes-gateway/config"
	"notes-gateway/internal/handler"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
)

const (
	defaultAuthRatePerMinute = 20
	defaultAuthRateBurst     = 10
)

type routeOptions struct {
	auth *authn.Module
	// per client IP, on the unauthenticated /v1/auth/* endpoints (login, register...)
	authRatePerMinute, authRateBurst int
	// devLogin exposes POST /v1/auth/dev-login. Dev environment only.
	devLogin bool
	logger   logger.Logger
}

// registerRoutes is shared by main and tests so both run the same middleware chain.
// Panic recovery is already installed by ginc.
func registerRoutes(r *gin.Engine, c *di.Container, opts routeOptions) {
	if opts.authRatePerMinute <= 0 || opts.authRateBurst <= 0 {
		opts.authRatePerMinute, opts.authRateBurst = defaultAuthRatePerMinute, defaultAuthRateBurst
	}

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

	// sign-in and tokens: no access token, rate limited per client IP
	public := r.Group("/v1", httpx.RateLimit(opts.authRatePerMinute, opts.authRateBurst))
	opts.auth.HTTP.RegisterPublicRoutes(public, opts.devLogin)

	v1 := r.Group("/v1", httpx.Auth(opts.auth.Authenticator))
	opts.auth.HTTP.RegisterRoutes(v1) // /me, /tenants, /members
	di.MustGet[*handler.NoteHandler](c).RegisterRoutes(v1)

	r.NoRoute(notFound)
}

// registerAdminRoutes: the internal admin server (never published), for the
// platform's admin service: /admin/v1/* behind the app's admin key, with the
// services' authorization engines under /admin/v1/authz and outbox under /admin/v1/outbox.
func registerAdminRoutes(r *gin.Engine, auth *authn.Module, authzAdmin *rebac.AdminProxy, outboxAdmin *outbox.AdminProxy, infraProbe *infra.Probe, adminKey string, l logger.Logger) {
	r.Use(httpx.RequestID(), httpx.AccessLog(l))
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	auth.AdminHTTP.RegisterRoutes(r)
	authzAdmin.RegisterRoutes(r)
	outboxAdmin.RegisterRoutes(r)
	infraProbe.RegisterRoutes(r)

	adminV1 := r.Group("/admin/v1", httpx.AdminKey(adminKey))
	adminV1.GET("/openapi", func(c *gin.Context) {
		data, err := openapi.FS.ReadFile("apps/notes/v1.yaml")
		if err != nil {
			response.Fail(c, fmt.Errorf("%w: openapi spec not found", apperr.ErrNotFound))
			return
		}
		c.Data(http.StatusOK, "application/yaml; charset=utf-8", data)
	})

	r.NoRoute(notFound)
}

func notFound(c *gin.Context) {
	response.Fail(c, fmt.Errorf("%w: route %s %s", apperr.ErrNotFound, c.Request.Method, c.Request.URL.Path))
}
