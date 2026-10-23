// Package httpapi exposes core/authn over HTTP (gin): the public endpoints an
// app's gateway serves (sign-in, tokens, tenants, members) and the admin API
// the platform's admin dashboard calls on the app's internal admin port.
package httpapi

import (
	"net/http"
	"strings"

	"core/authn/service"
	"core/httpx"
	"core/response"

	"github.com/gin-gonic/gin"
)

// Handler serves the public auth, tenant and member endpoints of an app.
// Business rules (who may grant which role, last owner...) live in the services.
type Handler struct {
	auth    *service.AuthService
	tokens  *service.TokenService
	tenants *service.TenantService
	members *service.MemberService
}

func NewHandler(auth *service.AuthService, tokens *service.TokenService, tenants *service.TenantService, members *service.MemberService) *Handler {
	return &Handler{auth: auth, tokens: tokens, tenants: tenants, members: members}
}

// RegisterPublicRoutes: endpoints that work without an access token (the caller
// should rate limit rg). devLogin must only be true in the dev environment.
func (h *Handler) RegisterPublicRoutes(rg *gin.RouterGroup, devLogin bool) {
	auth := rg.Group("/auth")
	auth.POST("/register", h.register)
	auth.POST("/login", h.login)
	if devLogin {
		auth.POST("/dev-login", h.devLogin)
	}
	auth.POST("/refresh", h.refresh)
	auth.POST("/logout", h.logout)
}

// RegisterRoutes: endpoints behind httpx.Auth. Tenant-less tokens may create/list
// tenants and switch into one; /members needs a tenant-scoped token.
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/me", h.me)
	rg.POST("/auth/change-password", h.changePassword)
	rg.POST("/auth/switch-tenant", h.switchTenant)
	rg.GET("/me/tenants", h.myTenants)
	rg.POST("/tenants", h.createTenant)

	members := rg.Group("/members")
	members.GET("", h.listMembers)
	members.POST("", h.addMember)
	members.PATCH("/:user_id", h.updateMember)
	members.DELETE("/:user_id", h.removeMember)
}

func (h *Handler) register(c *gin.Context) {
	var body RegisterRequest
	if !httpx.BindJSON(c, &body) {
		return
	}
	u, pair, err := h.auth.Register(c.Request.Context(), body.Email, body.Password)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusCreated, RegisterResponse{User: user(u), Tokens: tokenPair(pair)})
}

func (h *Handler) login(c *gin.Context) {
	var body LoginRequest
	if !httpx.BindJSON(c, &body) {
		return
	}
	pair, err := h.auth.Login(c.Request.Context(), body.Email, body.Password, body.TenantID)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusOK, tokenPair(pair))
}

func (h *Handler) changePassword(c *gin.Context) {
	var body ChangePasswordRequest
	if !httpx.BindJSON(c, &body) {
		return
	}
	if err := h.auth.ChangePassword(c.Request.Context(), body.CurrentPassword, body.NewPassword); err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}

func (h *Handler) me(c *gin.Context) {
	u, err := h.auth.Me(c.Request.Context())
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusOK, user(u))
}

func (h *Handler) devLogin(c *gin.Context) {
	var body DevLoginRequest
	if !httpx.BindJSON(c, &body) {
		return
	}
	pair, err := h.tokens.DevLogin(c.Request.Context(), body.UserID, body.TenantID)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusOK, tokenPair(pair))
}

func (h *Handler) refresh(c *gin.Context) {
	var body RefreshRequest
	if !httpx.BindJSON(c, &body) {
		return
	}
	pair, err := h.tokens.Refresh(c.Request.Context(), body.RefreshToken)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusOK, tokenPair(pair))
}

func (h *Handler) logout(c *gin.Context) {
	var body RefreshRequest
	if !httpx.BindJSON(c, &body) {
		return
	}
	if err := h.tokens.Revoke(c.Request.Context(), body.RefreshToken); err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}

func (h *Handler) switchTenant(c *gin.Context) {
	var body SwitchTenantRequest
	if !httpx.BindJSON(c, &body) {
		return
	}
	pair, err := h.tokens.SwitchTenant(c.Request.Context(), body.TenantID)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusOK, tokenPair(pair))
}

func (h *Handler) myTenants(c *gin.Context) {
	mine, err := h.tenants.ListMine(c.Request.Context())
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusOK, myTenants(mine))
}

func (h *Handler) createTenant(c *gin.Context) {
	var body CreateTenantRequest
	if !httpx.BindJSON(c, &body) {
		return
	}
	t, err := h.tenants.Create(c.Request.Context(), body.Name)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusCreated, tenant(t))
}

func (h *Handler) listMembers(c *gin.Context) {
	list, err := h.members.List(c.Request.Context())
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusOK, members(list))
}

func (h *Handler) addMember(c *gin.Context) {
	var body AddMemberRequest
	if !httpx.BindJSON(c, &body) {
		return
	}
	m, err := h.members.Add(c.Request.Context(), service.MemberRef{UserID: body.UserID, Email: strings.TrimSpace(body.Email)}, body.Role)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusCreated, member(m))
}

func (h *Handler) updateMember(c *gin.Context) {
	var body UpdateMemberRequest
	if !httpx.BindJSON(c, &body) {
		return
	}
	m, err := h.members.UpdateRole(c.Request.Context(), c.Param("user_id"), body.Role)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusOK, member(m))
}

func (h *Handler) removeMember(c *gin.Context) {
	if err := h.members.Remove(c.Request.Context(), c.Param("user_id")); err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}
