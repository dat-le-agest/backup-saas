package dto

import (
	"time"

	iamv1 "api/gen/go/platform/iam/v1"
)

type TokenPair struct {
	TokenType             string    `json:"token_type"`
	AccessToken           string    `json:"access_token"`
	AccessTokenExpiresAt  time.Time `json:"access_token_expires_at"`
	RefreshToken          string    `json:"refresh_token"`
	RefreshTokenExpiresAt time.Time `json:"refresh_token_expires_at"`
}

func TokenPairFromProto(p *iamv1.TokenPair) TokenPair {
	return TokenPair{
		TokenType:             "Bearer",
		AccessToken:           p.GetAccessToken(),
		AccessTokenExpiresAt:  p.GetAccessTokenExpireTime().AsTime(),
		RefreshToken:          p.GetRefreshToken(),
		RefreshTokenExpiresAt: p.GetRefreshTokenExpireTime().AsTime(),
	}
}

type User struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

func UserFromProto(u *iamv1.User) User {
	return User{ID: u.GetId(), Email: u.GetEmail(), CreatedAt: u.GetCreateTime().AsTime()}
}

type RegisterRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type RegisterResponse struct {
	User   User      `json:"user"`
	Tokens TokenPair `json:"tokens"`
}

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	TenantID string `json:"tenant_id"`
}

type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

type DevLoginRequest struct {
	UserID   string `json:"user_id"`
	TenantID string `json:"tenant_id"`
}

type RefreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type SwitchTenantRequest struct {
	TenantID string `json:"tenant_id"`
}

type Tenant struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

func TenantFromProto(t *iamv1.Tenant) Tenant {
	return Tenant{ID: t.GetId(), Name: t.GetName(), CreatedAt: t.GetCreateTime().AsTime()}
}

type MyTenant struct {
	Tenant
	Role string `json:"role"`
}

func MyTenantsFromProto(in []*iamv1.MyTenant) []MyTenant {
	out := make([]MyTenant, 0, len(in))
	for _, t := range in {
		out = append(out, MyTenant{Tenant: TenantFromProto(t.GetTenant()), Role: t.GetRole()})
	}
	return out
}

type CreateTenantRequest struct {
	Name string `json:"name"`
}

type Member struct {
	UserID    string    `json:"user_id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func MemberFromProto(m *iamv1.Member) Member {
	return Member{
		UserID: m.GetUserId(), Email: m.GetEmail(), Role: m.GetRole(),
		CreatedAt: m.GetCreateTime().AsTime(), UpdatedAt: m.GetUpdateTime().AsTime(),
	}
}

func MembersFromProto(in []*iamv1.Member) []Member {
	out := make([]Member, 0, len(in))
	for _, m := range in {
		out = append(out, MemberFromProto(m))
	}
	return out
}

// AddMemberRequest: either user_id or email.
type AddMemberRequest struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Role   string `json:"role"`
}

type UpdateMemberRequest struct {
	Role string `json:"role"`
}
