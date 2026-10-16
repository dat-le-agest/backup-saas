package service

import (
	"context"
	"fmt"

	"core/apperr"
	"core/authz"
	"core/reqctx"
	"core/uow"
	"iam/internal/model"
)

type TenantService struct {
	store Store
	uow   uow.UnitOfWork
	cache VersionCache
}

func NewTenantService(store Store, u uow.UnitOfWork, cache VersionCache) *TenantService {
	return &TenantService{store: store, uow: u, cache: cache}
}

// Create makes a tenant owned by the caller. Needs a logged-in user, not a tenant.
func (s *TenantService) Create(ctx context.Context, name string) (*model.Tenant, error) {
	userID, err := callerUserID(ctx)
	if err != nil {
		return nil, err
	}
	name, err = validateTenantName(name)
	if err != nil {
		return nil, err
	}

	tenant := &model.Tenant{ID: newTenantID(), Name: name}
	owner := &model.Membership{TenantID: tenant.ID, UserID: userID, Role: authz.RoleOwner, Version: 1}

	err = s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.store.CreateTenant(ctx, tenant); err != nil {
			return err
		}
		return s.store.SaveMembership(ctx, owner)
	})
	if err != nil {
		return nil, err
	}
	uow.AfterCommit(ctx, func(ctx context.Context) {
		_ = s.cache.SetMembershipVersion(ctx, tenant.ID, userID, owner.Version)
	})
	return tenant, nil
}

func (s *TenantService) ListMine(ctx context.Context) ([]model.MyTenant, error) {
	userID, err := callerUserID(ctx)
	if err != nil {
		return nil, err
	}
	return s.store.ListTenantsForUser(ctx, userID)
}

func callerUserID(ctx context.Context) (string, error) {
	id, ok := reqctx.IdentityFrom(ctx)
	if !ok || id.UserID == "" {
		return "", fmt.Errorf("%w: no caller identity", apperr.ErrUnauthenticated)
	}
	return id.UserID, nil
}
