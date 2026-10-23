// Package handler is the admin service's HTTP API (/api/*) for the dashboard.
package handler

import (
	"fmt"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
	"time"

	"admin/internal/model"
	"admin/internal/service"
	"core/apperr"
	"core/httpx"
	"core/logger"
	"core/response"

	"github.com/gin-gonic/gin"
)

// SessionCookie holds the admin's session token (HttpOnly, SameSite=Strict).
const SessionCookie = "admin_session"

type Handler struct {
	admins *service.Admins
	apps   *service.Apps
	logger logger.Logger
	// SecureCookie: set the Secure flag on the session cookie (HTTPS deployments)
	secureCookie bool
	// GrafanaURL, shown in the UI for per-app dashboards ("" hides the links)
	grafanaURL string
	transport  http.RoundTripper
}

type Options struct {
	SecureCookie bool
	GrafanaURL   string
	// Transport for calls to the apps' admin APIs; nil uses http.DefaultTransport.
	Transport http.RoundTripper
}

func New(admins *service.Admins, apps *service.Apps, l logger.Logger, o Options) *Handler {
	if o.Transport == nil {
		o.Transport = http.DefaultTransport
	}
	return &Handler{admins: admins, apps: apps, logger: l, secureCookie: o.SecureCookie, grafanaURL: o.GrafanaURL, transport: o.Transport}
}

// Register mounts /api/* on r. loginPerMinute rate limits sign-in per client IP.
func (h *Handler) Register(r gin.IRouter, loginPerMinute, loginBurst int) {
	api := r.Group("/api", h.sameOriginJSON)
	api.POST("/auth/login", httpx.RateLimit(loginPerMinute, loginBurst), h.login)
	api.POST("/auth/logout", h.logout)
	api.GET("/config", h.config)

	in := api.Group("", h.requireSession)
	in.GET("/me", h.me)
	in.GET("/admins", h.listAdmins)
	in.POST("/admins", h.createAdmin)
	in.GET("/apps", h.listApps)
	in.GET("/apps/:app", h.getApp)
	in.Any("/apps/:app/admin/*path", h.proxy)
}

// sameOriginJSON: POSTs must be JSON. A cross-site page can't send a JSON POST
// (or PUT/PATCH/DELETE) without a CORS preflight, which this API never grants,
// so the session cookie can't be ridden (CSRF), on top of SameSite=Strict.
func (h *Handler) sameOriginJSON(c *gin.Context) {
	if c.Request.Method == http.MethodPost {
		mt, _, _ := mime.ParseMediaType(c.GetHeader("Content-Type"))
		if mt != "application/json" {
			response.Fail(c, fmt.Errorf("%w: expected Content-Type application/json", apperr.ErrInvalid))
			return
		}
	}
	c.Next()
}

const adminKey = "admin"

func (h *Handler) requireSession(c *gin.Context) {
	cookie, err := c.Cookie(SessionCookie)
	if err != nil || cookie == "" {
		response.Fail(c, fmt.Errorf("%w: not signed in", apperr.ErrUnauthenticated))
		return
	}
	a, err := h.admins.Session(c.Request.Context(), cookie)
	if err != nil {
		response.Fail(c, err)
		return
	}
	c.Set(adminKey, a)
	c.Next()
}

func current(c *gin.Context) *model.Admin {
	a, _ := c.Get(adminKey)
	return a.(*model.Admin)
}

// ---- session ----

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type adminView struct {
	ID          string     `json:"id"`
	Email       string     `json:"email"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

func view(a *model.Admin) adminView {
	return adminView{ID: a.ID, Email: a.Email, LastLoginAt: a.LastLoginAt, CreatedAt: a.CreatedAt}
}

func (h *Handler) login(c *gin.Context) {
	var body loginRequest
	if !httpx.BindJSON(c, &body) {
		return
	}
	a, session, exp, err := h.admins.Login(c.Request.Context(), body.Email, body.Password)
	if err != nil {
		response.Fail(c, err)
		return
	}
	h.setCookie(c, session, exp)
	logger.WithContext(c.Request.Context(), h.logger).WithFields(logger.Fields{"admin": a.Email}).Info("admin signed in")
	response.OK(c, http.StatusOK, view(a))
}

func (h *Handler) logout(c *gin.Context) {
	h.setCookie(c, "", time.Unix(0, 0))
	response.NoContent(c)
}

func (h *Handler) setCookie(c *gin.Context, value string, exp time.Time) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: SessionCookie, Value: value, Path: "/", Expires: exp,
		HttpOnly: true, Secure: h.secureCookie, SameSite: http.SameSiteStrictMode,
	})
}

func (h *Handler) me(c *gin.Context) { response.OK(c, http.StatusOK, view(current(c))) }

func (h *Handler) config(c *gin.Context) {
	response.OK(c, http.StatusOK, gin.H{"grafana_url": h.grafanaURL})
}

// ---- admins ----

func (h *Handler) listAdmins(c *gin.Context) {
	list, err := h.admins.List(c.Request.Context())
	if err != nil {
		response.Fail(c, err)
		return
	}
	out := make([]adminView, 0, len(list))
	for i := range list {
		out = append(out, view(&list[i]))
	}
	response.OK(c, http.StatusOK, out)
}

func (h *Handler) createAdmin(c *gin.Context) {
	var body loginRequest
	if !httpx.BindJSON(c, &body) {
		return
	}
	a, err := h.admins.Create(c.Request.Context(), body.Email, body.Password)
	if err != nil {
		response.Fail(c, err)
		return
	}
	h.audit(c, "", "create admin "+a.Email)
	response.OK(c, http.StatusCreated, view(a))
}

// ---- apps ----

func (h *Handler) listApps(c *gin.Context) {
	response.OK(c, http.StatusOK, h.apps.Statuses(c.Request.Context()))
}

func (h *Handler) getApp(c *gin.Context) {
	st, err := h.apps.Status(c.Request.Context(), c.Param("app"))
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusOK, st)
}

// paths of an app's admin API the dashboard may call (core/authn/httpapi.AdminHandler)
var proxyPaths = regexp.MustCompile(`^/(stats|users|users/[^/]+(/(disabled|password))?|tenants|tenants/[^/]+(/members/[^/]+)?)$`)

// proxy forwards /api/apps/:app/admin/<path> to the app's /admin/v1/<path>
// with the app's admin key, which never reaches the browser.
func (h *Handler) proxy(c *gin.Context) {
	app, err := h.apps.Get(c.Param("app"))
	if err != nil {
		response.Fail(c, err)
		return
	}
	path := c.Param("path")
	if !proxyPaths.MatchString(path) {
		response.Fail(c, fmt.Errorf("%w: route %s %s", apperr.ErrNotFound, c.Request.Method, c.Request.URL.Path))
		return
	}
	target, _ := url.Parse(app.AdminURL)
	rp := &httputil.ReverseProxy{
		Transport: h.transport,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.URL.Path = strings.TrimSuffix(target.Path, "/") + "/admin/v1" + path
			r.Out.URL.RawPath = ""
			r.Out.Header.Del("Cookie")
			r.Out.Header.Set("Authorization", "Bearer "+app.APIKey)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.WithContext(r.Context(), h.logger).Errorf("admin API of %s: %v", app.ID, err)
			response.Fail(c, fmt.Errorf("%w: the %s app's admin API is unreachable", apperr.ErrUnavailable, app.ID))
		},
	}
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
		h.audit(c, app.ID, c.Request.Method+" "+path)
	}
	// a plain ResponseWriter: gin's also claims http.CloseNotifier, which it
	// can only forward when the underlying writer has it
	rp.ServeHTTP(plainWriter{c.Writer}, c.Request)
}

type plainWriter struct{ http.ResponseWriter }

func (w plainWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// audit logs a change made by the signed-in admin.
func (h *Handler) audit(c *gin.Context, app, what string) {
	logger.WithContext(c.Request.Context(), h.logger).
		WithFields(logger.Fields{"admin": current(c).Email, "target_app": app}).
		Infof("admin change: %s", what)
}
