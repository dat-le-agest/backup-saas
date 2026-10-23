package httpapi

import (
	"time"

	"core/authn/model"
)

// Public JSON shapes, mapped from the model: internal types never reach the
// client directly, so the API stays stable when they change.

type TokenPair struct {
	TokenType             string    `json:"token_type"`
	AccessToken           string    `json:"access_token"`
	AccessTokenExpiresAt  time.Time `json:"access_token_expires_at"`
	RefreshToken          string    `json:"refresh_token"`
	RefreshTokenExpiresAt time.Time `json:"refresh_token_expires_at"`
}

func tokenPair(p *model.TokenPair) TokenPair {
	return TokenPair{
		TokenType:             "Bearer",
		AccessToken:           p.AccessToken,
		AccessTokenExpiresAt:  p.AccessExpiresAt,
		RefreshToken:          p.RefreshToken,
		RefreshTokenExpiresAt: p.RefreshExpiresAt,
	}
}

type User struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

func user(u *model.User) User {
	return User{ID: u.ID, Email: u.EmailOrEmpty(), CreatedAt: u.CreatedAt}
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

func tenant(t *model.Tenant) Tenant {
	return Tenant{ID: t.ID, Name: t.Name, CreatedAt: t.CreatedAt}
}

type MyTenant struct {
	Tenant
	Role string `json:"role"`
}

func myTenants(in []model.MyTenant) []MyTenant {
	out := make([]MyTenant, 0, len(in))
	for i := range in {
		out = append(out, MyTenant{Tenant: tenant(&in[i].Tenant), Role: in[i].Role})
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

func member(m *model.MemberView) Member {
	return Member{UserID: m.UserID, Email: m.Email, Role: m.Role, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt}
}

func members(in []model.MemberView) []Member {
	out := make([]Member, 0, len(in))
	for i := range in {
		out = append(out, member(&in[i]))
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

// ---- admin API ----

type Paging struct {
	Page  int   `json:"page"`
	Limit int   `json:"limit"`
	Total int64 `json:"total"`
}

type AdminUser struct {
	ID         string     `json:"id"`
	Email      string     `json:"email"`
	Disabled   bool       `json:"disabled"`
	DisabledAt *time.Time `json:"disabled_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	// set by get
	Memberships []AdminMembership `json:"memberships,omitempty"`
}

type AdminMembership struct {
	TenantID  string    `json:"tenant_id"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

func adminUser(u *model.User) AdminUser {
	return AdminUser{ID: u.ID, Email: u.EmailOrEmpty(), Disabled: u.Disabled(), DisabledAt: u.DisabledAt, CreatedAt: u.CreatedAt}
}

func adminUserView(v *model.UserView) AdminUser {
	out := adminUser(&v.User)
	out.Memberships = make([]AdminMembership, 0, len(v.Memberships))
	for _, m := range v.Memberships {
		out.Memberships = append(out.Memberships, AdminMembership{TenantID: m.TenantID, Role: m.Role, CreatedAt: m.CreatedAt})
	}
	return out
}

type AdminTenant struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Members   int64     `json:"members"`
	CreatedAt time.Time `json:"created_at"`
	// set by get
	MemberList []Member `json:"member_list,omitempty"`
}

type SetDisabledRequest struct {
	Disabled bool `json:"disabled"`
}

type SetPasswordRequest struct {
	Password string `json:"password"`
}

type AdminStats struct {
	App     string `json:"app"`
	Users   int64  `json:"users"`
	Tenants int64  `json:"tenants"`
}
