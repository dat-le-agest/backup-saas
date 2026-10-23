package httpapi

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"core/apperr"
	"core/authn/model"
	"core/authn/service"
	"core/httpx"
	"core/response"

	"github.com/gin-gonic/gin"
)

// AdminHandler is the admin API of an app, for the platform's admin dashboard.
// Serve it on the app's internal admin port only (never the public one): every
// request must carry "Authorization: Bearer <admin key>".
type AdminHandler struct {
	svc *service.AdminService
	app string
	key []byte
}

// NewAdminHandler: key is the app's admin key (ADMIN_API_KEY), shared with the
// admin service; empty disables the admin API (every request is refused).
func NewAdminHandler(svc *service.AdminService, app, key string) *AdminHandler {
	return &AdminHandler{svc: svc, app: app, key: []byte(key)}
}

var errAdminKey = fmt.Errorf(`%w: expected "Authorization: Bearer <admin key>"`, apperr.ErrUnauthenticated)

// RegisterRoutes mounts /admin/v1/* on r.
func (h *AdminHandler) RegisterRoutes(r gin.IRouter) {
	g := r.Group("/admin/v1", h.requireKey)
	g.GET("/stats", h.stats)

	g.GET("/users", h.listUsers)
	g.GET("/users/:id", h.getUser)
	g.PUT("/users/:id/disabled", h.setDisabled)
	g.PUT("/users/:id/password", h.setPassword)

	g.GET("/tenants", h.listTenants)
	g.GET("/tenants/:id", h.getTenant)
	g.DELETE("/tenants/:id", h.deleteTenant)
	g.PATCH("/tenants/:id/members/:user_id", h.updateMember)
	g.DELETE("/tenants/:id/members/:user_id", h.removeMember)
}

func (h *AdminHandler) requireKey(c *gin.Context) {
	scheme, raw, ok := strings.Cut(c.GetHeader("Authorization"), " ")
	if len(h.key) == 0 || !ok || !strings.EqualFold(scheme, "Bearer") ||
		subtle.ConstantTimeCompare([]byte(strings.TrimSpace(raw)), h.key) != 1 {
		response.Fail(c, errAdminKey)
		return
	}
	c.Next()
}

func pageOf(c *gin.Context) model.Page {
	n, _ := strconv.Atoi(c.Query("page"))
	l, _ := strconv.Atoi(c.Query("limit"))
	if n < 1 {
		n = 1
	}
	if l < 1 || l > 100 {
		l = 20
	}
	return model.Page{Number: n, Limit: l}
}

func (h *AdminHandler) stats(c *gin.Context) {
	st, err := h.svc.Stats(c.Request.Context())
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusOK, AdminStats{App: h.app, Users: st.Users, Tenants: st.Tenants})
}

func (h *AdminHandler) listUsers(c *gin.Context) {
	p := pageOf(c)
	users, total, err := h.svc.ListUsers(c.Request.Context(), c.Query("q"), p)
	if err != nil {
		response.Fail(c, err)
		return
	}
	out := make([]AdminUser, 0, len(users))
	for i := range users {
		out = append(out, adminUser(&users[i]))
	}
	response.List(c, out, Paging{Page: p.Number, Limit: p.Limit, Total: total})
}

func (h *AdminHandler) getUser(c *gin.Context) {
	v, err := h.svc.GetUser(c.Request.Context(), c.Param("id"))
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusOK, adminUserView(v))
}

func (h *AdminHandler) setDisabled(c *gin.Context) {
	var body SetDisabledRequest
	if !httpx.BindJSON(c, &body) {
		return
	}
	u, err := h.svc.SetUserDisabled(c.Request.Context(), c.Param("id"), body.Disabled)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusOK, adminUser(u))
}

func (h *AdminHandler) setPassword(c *gin.Context) {
	var body SetPasswordRequest
	if !httpx.BindJSON(c, &body) {
		return
	}
	if err := h.svc.SetPassword(c.Request.Context(), c.Param("id"), body.Password); err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}

func (h *AdminHandler) listTenants(c *gin.Context) {
	p := pageOf(c)
	tenants, total, err := h.svc.ListTenants(c.Request.Context(), c.Query("q"), p)
	if err != nil {
		response.Fail(c, err)
		return
	}
	out := make([]AdminTenant, 0, len(tenants))
	for _, t := range tenants {
		out = append(out, AdminTenant{ID: t.ID, Name: t.Name, Members: t.Members, CreatedAt: t.CreatedAt})
	}
	response.List(c, out, Paging{Page: p.Number, Limit: p.Limit, Total: total})
}

func (h *AdminHandler) getTenant(c *gin.Context) {
	t, ms, err := h.svc.GetTenant(c.Request.Context(), c.Param("id"))
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusOK, AdminTenant{
		ID: t.ID, Name: t.Name, Members: int64(len(ms)), CreatedAt: t.CreatedAt, MemberList: members(ms),
	})
}

func (h *AdminHandler) deleteTenant(c *gin.Context) {
	if err := h.svc.DeleteTenant(c.Request.Context(), c.Param("id")); err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}

func (h *AdminHandler) updateMember(c *gin.Context) {
	var body UpdateMemberRequest
	if !httpx.BindJSON(c, &body) {
		return
	}
	m, err := h.svc.UpdateMemberRole(c.Request.Context(), c.Param("id"), c.Param("user_id"), body.Role)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusOK, member(m))
}

func (h *AdminHandler) removeMember(c *gin.Context) {
	if err := h.svc.RemoveMember(c.Request.Context(), c.Param("id"), c.Param("user_id")); err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}
