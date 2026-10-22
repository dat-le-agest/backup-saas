package handler

import (
	"net/http"

	iamv1 "api/gen/go/platform/iam/v1"
	"api/gen/go/platform/iam/v1/iamv1connect"
	"core/response"
	"gateway/internal/dto"
	"sdk/httpx"

	"github.com/gin-gonic/gin"
)

// IamHandler maps auth, tenant and member endpoints to the iam service.
// Business rules (who may grant which role, last owner...) live in iam.
type IamHandler struct {
	iam iamv1connect.IamServiceClient
}

func NewIamHandler(iam iamv1connect.IamServiceClient) *IamHandler {
	return &IamHandler{iam: iam}
}

// RegisterPublicRoutes: endpoints that work without an access token (the caller
// should rate limit rg). devLogin must only be true in the dev environment.
func (h *IamHandler) RegisterPublicRoutes(rg *gin.RouterGroup, devLogin bool) {
	auth := rg.Group("/auth")
	auth.POST("/register", h.register)
	auth.POST("/login", h.login)
	if devLogin {
		auth.POST("/dev-login", h.devLogin)
	}
	auth.POST("/refresh", h.refresh)
	auth.POST("/logout", h.logout)
}

// RegisterRoutes: endpoints behind Auth. Tenant-less tokens may create/list
// tenants and switch into one; /members needs a tenant-scoped token.
func (h *IamHandler) RegisterRoutes(rg *gin.RouterGroup) {
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

func (h *IamHandler) register(c *gin.Context) {
	var body dto.RegisterRequest
	if !httpx.BindJSON(c, &body) {
		return
	}
	res, err := h.iam.Register(c.Request.Context(), &iamv1.RegisterRequest{Email: body.Email, Password: body.Password})
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusCreated, dto.RegisterResponse{
		User:   dto.UserFromProto(res.GetUser()),
		Tokens: dto.TokenPairFromProto(res.GetTokens()),
	})
}

func (h *IamHandler) login(c *gin.Context) {
	var body dto.LoginRequest
	if !httpx.BindJSON(c, &body) {
		return
	}
	res, err := h.iam.Login(c.Request.Context(), &iamv1.LoginRequest{
		Email: body.Email, Password: body.Password, TenantId: body.TenantID,
	})
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusOK, dto.TokenPairFromProto(res.GetTokens()))
}

func (h *IamHandler) changePassword(c *gin.Context) {
	var body dto.ChangePasswordRequest
	if !httpx.BindJSON(c, &body) {
		return
	}
	_, err := h.iam.ChangePassword(c.Request.Context(), &iamv1.ChangePasswordRequest{
		CurrentPassword: body.CurrentPassword, NewPassword: body.NewPassword,
	})
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}

func (h *IamHandler) me(c *gin.Context) {
	res, err := h.iam.GetMe(c.Request.Context(), &iamv1.GetMeRequest{})
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusOK, dto.UserFromProto(res.GetUser()))
}

func (h *IamHandler) devLogin(c *gin.Context) {
	var body dto.DevLoginRequest
	if !httpx.BindJSON(c, &body) {
		return
	}
	res, err := h.iam.DevLogin(c.Request.Context(), &iamv1.DevLoginRequest{UserId: body.UserID, TenantId: body.TenantID})
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusOK, dto.TokenPairFromProto(res.GetTokens()))
}

func (h *IamHandler) refresh(c *gin.Context) {
	var body dto.RefreshRequest
	if !httpx.BindJSON(c, &body) {
		return
	}
	res, err := h.iam.RefreshTokens(c.Request.Context(), &iamv1.RefreshTokensRequest{RefreshToken: body.RefreshToken})
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusOK, dto.TokenPairFromProto(res.GetTokens()))
}

func (h *IamHandler) logout(c *gin.Context) {
	var body dto.RefreshRequest
	if !httpx.BindJSON(c, &body) {
		return
	}
	if _, err := h.iam.RevokeRefreshToken(c.Request.Context(), &iamv1.RevokeRefreshTokenRequest{RefreshToken: body.RefreshToken}); err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}

func (h *IamHandler) switchTenant(c *gin.Context) {
	var body dto.SwitchTenantRequest
	if !httpx.BindJSON(c, &body) {
		return
	}
	res, err := h.iam.SwitchTenant(c.Request.Context(), &iamv1.SwitchTenantRequest{TenantId: body.TenantID})
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusOK, dto.TokenPairFromProto(res.GetTokens()))
}

func (h *IamHandler) myTenants(c *gin.Context) {
	res, err := h.iam.ListMyTenants(c.Request.Context(), &iamv1.ListMyTenantsRequest{})
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusOK, dto.MyTenantsFromProto(res.GetTenants()))
}

func (h *IamHandler) createTenant(c *gin.Context) {
	var body dto.CreateTenantRequest
	if !httpx.BindJSON(c, &body) {
		return
	}
	res, err := h.iam.CreateTenant(c.Request.Context(), &iamv1.CreateTenantRequest{Name: body.Name})
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusCreated, dto.TenantFromProto(res.GetTenant()))
}

func (h *IamHandler) listMembers(c *gin.Context) {
	res, err := h.iam.ListMembers(c.Request.Context(), &iamv1.ListMembersRequest{})
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusOK, dto.MembersFromProto(res.GetMembers()))
}

func (h *IamHandler) addMember(c *gin.Context) {
	var body dto.AddMemberRequest
	if !httpx.BindJSON(c, &body) {
		return
	}
	res, err := h.iam.AddMember(c.Request.Context(), &iamv1.AddMemberRequest{UserId: body.UserID, Email: body.Email, Role: body.Role})
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusCreated, dto.MemberFromProto(res.GetMember()))
}

func (h *IamHandler) updateMember(c *gin.Context) {
	var body dto.UpdateMemberRequest
	if !httpx.BindJSON(c, &body) {
		return
	}
	res, err := h.iam.UpdateMemberRole(c.Request.Context(), &iamv1.UpdateMemberRoleRequest{UserId: c.Param("user_id"), Role: body.Role})
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, http.StatusOK, dto.MemberFromProto(res.GetMember()))
}

func (h *IamHandler) removeMember(c *gin.Context) {
	if _, err := h.iam.RemoveMember(c.Request.Context(), &iamv1.RemoveMemberRequest{UserId: c.Param("user_id")}); err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}
