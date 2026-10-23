package service

import (
	"context"
	"time"

	"iam/internal/model"
)

// Store is implemented by internal/repository. Getters return apperr.ErrNotFound.
// Calls made with the ctx of a unit of work (core/uow) share its transaction.
type Store interface {
	EnsureApps(ctx context.Context, ids []string) error
	GetApp(ctx context.Context, id string) (*model.App, error)

	CreateTenant(ctx context.Context, t *model.Tenant) error
	GetTenant(ctx context.Context, id string) (*model.Tenant, error)
	LockTenant(ctx context.Context, tenantID string) error
	ListTenantsForUser(ctx context.Context, appID, userID string) ([]model.MyTenant, error)

	GetMembership(ctx context.Context, tenantID, userID string) (*model.Membership, error)
	GetMembershipAny(ctx context.Context, tenantID, userID string) (*model.Membership, error)
	ListMembers(ctx context.Context, tenantID string) ([]model.Membership, error)
	CountOwners(ctx context.Context, tenantID string) (int64, error)
	SaveMembership(ctx context.Context, m *model.Membership) error

	ListMemberViews(ctx context.Context, tenantID string) ([]model.MemberView, error)

	CreateUser(ctx context.Context, u *model.User) error
	EnsureUser(ctx context.Context, id string) error
	GetUserByID(ctx context.Context, id string) (*model.User, error)
	GetUserByEmail(ctx context.Context, email string) (*model.User, error)
	UpdateUserPassword(ctx context.Context, id, hash string) error

	GetLoginThrottle(ctx context.Context, email string) (*model.LoginThrottle, error)
	RecordLoginFailure(ctx context.Context, email string, now time.Time, window time.Duration, maxFailures int, lockFor time.Duration) error
	ResetLoginThrottle(ctx context.Context, email string) error

	CreateRefreshToken(ctx context.Context, t *model.RefreshToken) error
	GetRefreshToken(ctx context.Context, hash string) (*model.RefreshToken, error)
	MarkRefreshTokenUsed(ctx context.Context, id uint) (bool, error)
	RevokeRefreshTokenFamily(ctx context.Context, familyID string) error
	RevokeRefreshTokensOf(ctx context.Context, tenantID, userID string) error
	RevokeRefreshTokensOfUser(ctx context.Context, userID string) error
}

// PasswordHasher is satisfied by *password.Hasher.
type PasswordHasher interface {
	Hash(ctx context.Context, password string) (string, error)
	Verify(ctx context.Context, password, encoded string) (ok, needsRehash bool, err error)
	VerifyDummy(ctx context.Context, password string)
}

// VersionCache publishes membership versions for the gateway (Redis).
// Failures are logged, not returned: the gateway falls back to asking iam.
type VersionCache interface {
	SetMembershipVersion(ctx context.Context, tenantID, userID string, version int64) error
}

// AccessTokenSigner is satisfied by *sdk/token.Signer.
type AccessTokenSigner interface {
	Sign(userID, appID, tenantID, role string, membershipVersion int64) (string, time.Time, error)
}
