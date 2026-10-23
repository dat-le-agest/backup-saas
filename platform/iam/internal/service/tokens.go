package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"core/apperr"
	"core/logger"
	"core/uow"
	"iam/internal/model"
	"sdk/token"
)

const DefaultRefreshTTL = 14 * 24 * time.Hour

var errInvalidRefresh = fmt.Errorf("%w: invalid or expired refresh token", apperr.ErrUnauthenticated)

type TokenService struct {
	store      Store
	uow        uow.UnitOfWork
	metrics    *authMetrics
	signer     AccessTokenSigner
	logger     logger.Logger
	refreshTTL time.Duration
	devLogin   bool
	now        func() time.Time
}

type TokenConfig struct {
	RefreshTTL time.Duration
	// DevLogin enables DevLogin (tokens without credentials). Dev environment only.
	DevLogin bool
}

func NewTokenService(store Store, u uow.UnitOfWork, signer AccessTokenSigner, cfg TokenConfig, l logger.Logger) *TokenService {
	if cfg.RefreshTTL <= 0 {
		cfg.RefreshTTL = DefaultRefreshTTL
	}
	return &TokenService{
		store: store, uow: u, metrics: newAuthMetrics(), signer: signer, logger: l,
		refreshTTL: cfg.RefreshTTL, devLogin: cfg.DevLogin, now: time.Now,
	}
}

// DevLogin issues tokens of appID for userID without any credential check,
// creating a credential-less user if needed. Every real login (password, later
// OIDC) also ends in s.login(...).
func (s *TokenService) DevLogin(ctx context.Context, userID, appID, tenantID string) (*model.TokenPair, error) {
	if !s.devLogin {
		return nil, fmt.Errorf("%w: dev login is disabled", apperr.ErrForbidden)
	}
	if err := validateUserID(userID); err != nil {
		return nil, err
	}
	if err := requireApp(ctx, s.store, appID); err != nil {
		return nil, err
	}
	if err := s.store.EnsureUser(ctx, userID); err != nil {
		return nil, err
	}
	return s.login(ctx, userID, appID, tenantID)
}

// SwitchTenant issues tokens scoped to tenantID for the calling user, in the
// app of the caller's token.
func (s *TokenService) SwitchTenant(ctx context.Context, tenantID string) (*model.TokenPair, error) {
	id, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if tenantID == "" {
		return nil, fmt.Errorf("%w: tenant_id is required", apperr.ErrInvalid)
	}
	return s.login(ctx, id.UserID, id.AppID, tenantID)
}

// login starts a new refresh token family in appID (a registered app).
func (s *TokenService) login(ctx context.Context, userID, appID, tenantID string) (*model.TokenPair, error) {
	var pair *model.TokenPair
	err := s.uow.Do(ctx, func(ctx context.Context) error {
		var err error
		pair, err = s.issue(ctx, userID, appID, tenantID, randomHex(16))
		return err
	})
	return pair, err
}

// Refresh rotates a refresh token. Reusing a token that was already exchanged
// means it leaked (or the client raced): the whole family is revoked.
func (s *TokenService) Refresh(ctx context.Context, raw string) (*model.TokenPair, error) {
	if raw == "" {
		return nil, errInvalidRefresh
	}

	var (
		pair   *model.TokenPair
		reused *model.RefreshToken
	)
	err := s.uow.Do(ctx, func(ctx context.Context) error {
		rt, err := s.store.GetRefreshToken(ctx, hashRefreshToken(raw))
		if errors.Is(err, apperr.ErrNotFound) {
			return errInvalidRefresh
		}
		if err != nil {
			return err
		}
		if rt.RevokedAt != nil || !s.now().Before(rt.ExpiresAt) {
			return errInvalidRefresh
		}

		ok, err := s.store.MarkRefreshTokenUsed(ctx, rt.ID)
		if err != nil {
			return err
		}
		if !ok {
			reused = rt
			return s.store.RevokeRefreshTokenFamily(ctx, rt.FamilyID) // committed below
		}

		pair, err = s.issue(ctx, rt.UserID, rt.AppID, rt.TenantID, rt.FamilyID)
		if errors.Is(err, apperr.ErrNotFound) {
			// membership removed meanwhile: end this session
			if rerr := s.store.RevokeRefreshTokenFamily(ctx, rt.FamilyID); rerr != nil {
				return rerr
			}
			pair = nil
			return nil
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	if reused != nil {
		logger.WithContext(ctx, s.logger).WithFields(logger.Fields{
			"user_id": reused.UserID, "tenant_id": reused.TenantID, "family_id": reused.FamilyID,
		}).Warn("refresh token reuse detected, family revoked")
		s.metrics.refreshReuse.Add(ctx, 1)
		return nil, errInvalidRefresh
	}
	if pair == nil {
		return nil, errInvalidRefresh
	}
	return pair, nil
}

// Revoke ends the session of a refresh token (logout). Unknown tokens are ignored.
func (s *TokenService) Revoke(ctx context.Context, raw string) error {
	rt, err := s.store.GetRefreshToken(ctx, hashRefreshToken(raw))
	if errors.Is(err, apperr.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.store.RevokeRefreshTokenFamily(ctx, rt.FamilyID)
}

// MembershipVersion returns the current version, apperr.ErrNotFound if not an active member.
func (s *TokenService) MembershipVersion(ctx context.Context, tenantID, userID string) (int64, error) {
	if tenantID == "" || userID == "" {
		return 0, fmt.Errorf("%w: tenant_id and user_id are required", apperr.ErrInvalid)
	}
	m, err := s.store.GetMembership(ctx, tenantID, userID)
	if err != nil {
		return 0, err
	}
	return m.Version, nil
}

// issue signs an access token of appID with the membership's current role and
// version, and stores a new refresh token in family. tenantID may be empty;
// otherwise it must be a tenant of appID the user is an active member of.
func (s *TokenService) issue(ctx context.Context, userID, appID, tenantID, family string) (*model.TokenPair, error) {
	var (
		role    string
		version int64
	)
	if tenantID != "" {
		// not a member, or a tenant of another app: indistinguishable from a missing tenant
		tenant, err := s.store.GetTenant(ctx, tenantID)
		if err != nil {
			return nil, err
		}
		if tenant.AppID != appID {
			return nil, apperr.ErrNotFound
		}
		m, err := s.store.GetMembership(ctx, tenantID, userID)
		if err != nil {
			return nil, err
		}
		role, version = m.Role, m.Version
	}

	access, accessExp, err := s.signer.Sign(userID, appID, tenantID, role, version)
	if err != nil {
		return nil, err
	}

	raw, hash := newRefreshToken()
	refreshExp := s.now().Add(s.refreshTTL)
	if err := s.store.CreateRefreshToken(ctx, &model.RefreshToken{
		TokenHash: hash, FamilyID: family, AppID: appID, UserID: userID, TenantID: tenantID, ExpiresAt: refreshExp,
	}); err != nil {
		return nil, err
	}

	return &model.TokenPair{
		AccessToken: access, AccessExpiresAt: accessExp,
		RefreshToken: raw, RefreshExpiresAt: refreshExp,
	}, nil
}

var _ AccessTokenSigner = (*token.Signer)(nil)
