// Package grpc exposes iam over gRPC (Connect). It only translates proto ↔ model;
// rpc.HandlerOptions maps apperr sentinels to gRPC codes.
package grpc

import (
	"context"

	iamv1 "api/gen/go/platform/iam/v1"
	"api/gen/go/platform/iam/v1/iamv1connect"
	"core/components/ginc"
	"core/logger"
	"core/rpc"
	"iam/internal/model"
	"iam/internal/service"

	"github.com/gin-gonic/gin"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var _ iamv1connect.IamServiceHandler = (*IamHandler)(nil)

type IamHandler struct {
	auth    *service.AuthService
	tokens  *service.TokenService
	tenants *service.TenantService
	members *service.MemberService
	logger  logger.Logger
}

func NewIamHandler(
	auth *service.AuthService,
	tokens *service.TokenService,
	tenants *service.TenantService,
	members *service.MemberService,
	l logger.Logger,
) *IamHandler {
	return &IamHandler{auth: auth, tokens: tokens, tenants: tenants, members: members, logger: l.With("handler", "iam")}
}

// Mount registers the handler on the router under /platform.iam.v1.IamService/.
func (h *IamHandler) Mount(r gin.IRouter) {
	path, handler := iamv1connect.NewIamServiceHandler(h, rpc.HandlerOptions(h.logger))
	ginc.Mount(r, path, handler)
}

// ---- accounts ----

func (h *IamHandler) Register(ctx context.Context, req *iamv1.RegisterRequest) (*iamv1.RegisterResponse, error) {
	user, pair, err := h.auth.Register(ctx, req.GetEmail(), req.GetPassword(), req.GetApp())
	if err != nil {
		return nil, err
	}
	return &iamv1.RegisterResponse{User: toUser(user), Tokens: toTokenPair(pair)}, nil
}

func (h *IamHandler) Login(ctx context.Context, req *iamv1.LoginRequest) (*iamv1.LoginResponse, error) {
	pair, err := h.auth.Login(ctx, req.GetEmail(), req.GetPassword(), req.GetApp(), req.GetTenantId())
	if err != nil {
		return nil, err
	}
	return &iamv1.LoginResponse{Tokens: toTokenPair(pair)}, nil
}

func (h *IamHandler) ChangePassword(ctx context.Context, req *iamv1.ChangePasswordRequest) (*iamv1.ChangePasswordResponse, error) {
	if err := h.auth.ChangePassword(ctx, req.GetCurrentPassword(), req.GetNewPassword()); err != nil {
		return nil, err
	}
	return &iamv1.ChangePasswordResponse{}, nil
}

func (h *IamHandler) GetMe(ctx context.Context, _ *iamv1.GetMeRequest) (*iamv1.GetMeResponse, error) {
	user, err := h.auth.Me(ctx)
	if err != nil {
		return nil, err
	}
	return &iamv1.GetMeResponse{User: toUser(user)}, nil
}

// ---- tokens ----

func (h *IamHandler) DevLogin(ctx context.Context, req *iamv1.DevLoginRequest) (*iamv1.DevLoginResponse, error) {
	pair, err := h.tokens.DevLogin(ctx, req.GetUserId(), req.GetApp(), req.GetTenantId())
	if err != nil {
		return nil, err
	}
	return &iamv1.DevLoginResponse{Tokens: toTokenPair(pair)}, nil
}

func (h *IamHandler) SwitchTenant(ctx context.Context, req *iamv1.SwitchTenantRequest) (*iamv1.SwitchTenantResponse, error) {
	pair, err := h.tokens.SwitchTenant(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	return &iamv1.SwitchTenantResponse{Tokens: toTokenPair(pair)}, nil
}

func (h *IamHandler) RefreshTokens(ctx context.Context, req *iamv1.RefreshTokensRequest) (*iamv1.RefreshTokensResponse, error) {
	pair, err := h.tokens.Refresh(ctx, req.GetRefreshToken())
	if err != nil {
		return nil, err
	}
	return &iamv1.RefreshTokensResponse{Tokens: toTokenPair(pair)}, nil
}

func (h *IamHandler) RevokeRefreshToken(ctx context.Context, req *iamv1.RevokeRefreshTokenRequest) (*iamv1.RevokeRefreshTokenResponse, error) {
	if err := h.tokens.Revoke(ctx, req.GetRefreshToken()); err != nil {
		return nil, err
	}
	return &iamv1.RevokeRefreshTokenResponse{}, nil
}

func (h *IamHandler) GetMembershipVersion(ctx context.Context, req *iamv1.GetMembershipVersionRequest) (*iamv1.GetMembershipVersionResponse, error) {
	v, err := h.tokens.MembershipVersion(ctx, req.GetAppId(), req.GetTenantId(), req.GetUserId())
	if err != nil {
		return nil, err
	}
	return &iamv1.GetMembershipVersionResponse{Version: v}, nil
}

// ---- tenants ----

func (h *IamHandler) CreateTenant(ctx context.Context, req *iamv1.CreateTenantRequest) (*iamv1.CreateTenantResponse, error) {
	t, err := h.tenants.Create(ctx, req.GetName())
	if err != nil {
		return nil, err
	}
	return &iamv1.CreateTenantResponse{Tenant: toTenant(t)}, nil
}

func (h *IamHandler) ListMyTenants(ctx context.Context, _ *iamv1.ListMyTenantsRequest) (*iamv1.ListMyTenantsResponse, error) {
	mine, err := h.tenants.ListMine(ctx)
	if err != nil {
		return nil, err
	}
	res := &iamv1.ListMyTenantsResponse{Tenants: make([]*iamv1.MyTenant, 0, len(mine))}
	for i := range mine {
		res.Tenants = append(res.Tenants, &iamv1.MyTenant{Tenant: toTenant(&mine[i].Tenant), Role: mine[i].Role})
	}
	return res, nil
}

// ---- members ----

func (h *IamHandler) ListMembers(ctx context.Context, _ *iamv1.ListMembersRequest) (*iamv1.ListMembersResponse, error) {
	members, err := h.members.List(ctx)
	if err != nil {
		return nil, err
	}
	res := &iamv1.ListMembersResponse{Members: make([]*iamv1.Member, 0, len(members))}
	for i := range members {
		res.Members = append(res.Members, toMember(&members[i]))
	}
	return res, nil
}

func (h *IamHandler) AddMember(ctx context.Context, req *iamv1.AddMemberRequest) (*iamv1.AddMemberResponse, error) {
	m, err := h.members.Add(ctx, service.MemberRef{UserID: req.GetUserId(), Email: req.GetEmail()}, req.GetRole())
	if err != nil {
		return nil, err
	}
	return &iamv1.AddMemberResponse{Member: toMember(m)}, nil
}

func (h *IamHandler) UpdateMemberRole(ctx context.Context, req *iamv1.UpdateMemberRoleRequest) (*iamv1.UpdateMemberRoleResponse, error) {
	m, err := h.members.UpdateRole(ctx, req.GetUserId(), req.GetRole())
	if err != nil {
		return nil, err
	}
	return &iamv1.UpdateMemberRoleResponse{Member: toMember(m)}, nil
}

func (h *IamHandler) RemoveMember(ctx context.Context, req *iamv1.RemoveMemberRequest) (*iamv1.RemoveMemberResponse, error) {
	if err := h.members.Remove(ctx, req.GetUserId()); err != nil {
		return nil, err
	}
	return &iamv1.RemoveMemberResponse{}, nil
}

// ---- mapping ----

func toTokenPair(p *model.TokenPair) *iamv1.TokenPair {
	return &iamv1.TokenPair{
		AccessToken:            p.AccessToken,
		AccessTokenExpireTime:  timestamppb.New(p.AccessExpiresAt),
		RefreshToken:           p.RefreshToken,
		RefreshTokenExpireTime: timestamppb.New(p.RefreshExpiresAt),
	}
}

func toTenant(t *model.Tenant) *iamv1.Tenant {
	return &iamv1.Tenant{Id: t.ID, AppId: t.AppID, Name: t.Name, CreateTime: timestamppb.New(t.CreatedAt)}
}

func toMember(m *model.MemberView) *iamv1.Member {
	return &iamv1.Member{
		TenantId:   m.TenantID,
		UserId:     m.UserID,
		Role:       m.Role,
		Email:      m.Email,
		CreateTime: timestamppb.New(m.CreatedAt),
		UpdateTime: timestamppb.New(m.UpdatedAt),
	}
}

func toUser(u *model.User) *iamv1.User {
	return &iamv1.User{Id: u.ID, Email: u.EmailOrEmpty(), CreateTime: timestamppb.New(u.CreatedAt)}
}
