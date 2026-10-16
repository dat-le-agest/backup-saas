package model

import "time"

// The schema lives in migrations/*.sql; gorm tags only describe it.

type Tenant struct {
	ID        string `gorm:"primaryKey;size:64"`
	Name      string `gorm:"size:255;not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Membership of a user in a tenant. Rows are never deleted: removal sets
// RemovedAt, so Version keeps increasing across remove/re-add and an old
// token's mv can never match a later membership.
type Membership struct {
	TenantID  string `gorm:"primaryKey;size:64"`
	UserID    string `gorm:"primaryKey;size:64;index"`
	Role      string `gorm:"size:32;not null"`
	Version   int64  `gorm:"not null"`
	RemovedAt *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (m *Membership) Active() bool { return m.RemovedAt == nil }

type MyTenant struct {
	Tenant Tenant
	Role   string
}

// RefreshToken stores only a hash of the opaque token handed to the client.
// Tokens of one login share a FamilyID; each refresh marks the old token used
// and issues a new one in the same family.
type RefreshToken struct {
	ID        uint      `gorm:"primaryKey"`
	TokenHash string    `gorm:"size:64;not null;uniqueIndex"`
	FamilyID  string    `gorm:"size:64;not null;index"`
	UserID    string    `gorm:"size:64;not null;index:idx_refresh_tokens_user_tenant,priority:1"`
	TenantID  string    `gorm:"size:64;not null;index:idx_refresh_tokens_user_tenant,priority:2"`
	ExpiresAt time.Time `gorm:"not null"`
	UsedAt    *time.Time
	RevokedAt *time.Time
	CreatedAt time.Time
}

// TokenPair is what clients receive.
type TokenPair struct {
	AccessToken      string
	AccessExpiresAt  time.Time
	RefreshToken     string
	RefreshExpiresAt time.Time
}

// User of the platform. Email is nil for users created by dev login; an empty
// PasswordHash means password login is not possible for that user.
type User struct {
	ID           string  `gorm:"primaryKey;size:64"`
	Email        *string `gorm:"size:254;uniqueIndex"`
	PasswordHash string  `gorm:"size:255;not null;default:''"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (u *User) EmailOrEmpty() string {
	if u.Email == nil {
		return ""
	}
	return *u.Email
}

// LoginThrottle counts failed logins per email (registered or not, so it does
// not reveal which emails exist).
type LoginThrottle struct {
	Email       string `gorm:"primaryKey;size:254"`
	Failures    int    `gorm:"not null"`
	LockedUntil *time.Time
	UpdatedAt   time.Time
}

// MemberView is a membership with the member's email for display.
type MemberView struct {
	Membership
	Email string
}
