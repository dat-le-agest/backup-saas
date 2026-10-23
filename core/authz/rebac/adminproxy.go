package rebac

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"

	"core/apperr"
	"core/httpx"
	"core/logger"
	"core/response"

	"github.com/gin-gonic/gin"
)

// AdminProxy exposes the engines of an app's services through the app's admin
// API (one URL per app for the admin service): GET /admin/v1/authz lists the
// services, /admin/v1/authz/<service>/<path> is forwarded to that service's
// /admin/v1/authz/<path> (AdminHandler) with the same admin key.
type AdminProxy struct {
	key       string
	services  map[string]*url.URL
	names     []string
	transport http.RoundTripper
	logger    logger.Logger
}

// NewAdminProxy: services maps a service name to the base URL serving its
// AdminHandler, e.g. {"note": "http://note:3000"}.
func NewAdminProxy(key string, services map[string]string, transport http.RoundTripper, l logger.Logger) (*AdminProxy, error) {
	p := &AdminProxy{key: key, services: map[string]*url.URL{}, transport: transport, logger: l}
	for name, raw := range services {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("authz admin proxy: invalid URL %q for service %s", raw, name)
		}
		p.services[name] = u
		p.names = append(p.names, name)
	}
	sort.Strings(p.names)
	return p, nil
}

// RegisterRoutes mounts /admin/v1/authz and /admin/v1/authz/:service/* on r.
func (p *AdminProxy) RegisterRoutes(r gin.IRouter) {
	key := httpx.AdminKey(p.key)
	r.GET("/admin/v1/authz", key, func(c *gin.Context) { response.OK(c, http.StatusOK, p.names) })
	r.Any("/admin/v1/authz/:service/*path", key, p.forward)
}

func (p *AdminProxy) forward(c *gin.Context) {
	name := c.Param("service")
	target, ok := p.services[name]
	if !ok {
		response.Fail(c, fmt.Errorf("%w: no authorization engine named %q", apperr.ErrNotFound, name))
		return
	}
	rp := &httputil.ReverseProxy{
		Transport: p.transport,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.URL.Path = strings.TrimSuffix(target.Path, "/") + "/admin/v1/authz" + c.Param("path")
			r.Out.URL.RawPath = ""
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.WithContext(r.Context(), p.logger).Errorf("authz admin API of %s: %v", name, err)
			response.Fail(c, fmt.Errorf("%w: the %s service is unreachable", apperr.ErrUnavailable, name))
		},
	}
	rp.ServeHTTP(plainWriter{c.Writer}, c.Request)
}

// plainWriter: gin's writer also claims http.CloseNotifier, which it can only
// forward when the underlying writer has it.
type plainWriter struct{ http.ResponseWriter }

func (w plainWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
