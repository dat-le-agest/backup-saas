package service

import (
	"context"
	"fmt"

	"core/apperr"
	"core/reqctx"
	"core/uow"
	"iam/internal/model"
	"sdk/authz"
)

type TenantService struct {
	store Store
	uow   uow.UnitOfWork
	rel   authz.Relationships
	cache VersionCache
}

func NewTenantService(store Store, u uow.UnitOfWork, rel authz.Relationships, cache VersionCache) *TenantService {
	return &TenantService{store: store, uow: u, rel: rel, cache: cache}
}

// Create makes a tenant of the caller's app, owned by the caller. Needs a
// logged-in user, not a tenant.
func (s *TenantService) Create(ctx context.Context, name string) (*model.Tenant, error) {
	id, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	userID := id.UserID
	name, err = validateTenantName(name)
	if err != nil {
		return nil, err
	}

	tenant := &model.Tenant{ID: newTenantID(), AppID: id.AppID, Name: name}
	owner := &model.Membership{TenantID: tenant.ID, UserID: userID, Role: authz.RoleOwner, Version: 1}

	err = s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.store.CreateTenant(ctx, tenant); err != nil {
			return err
		}
		if err := s.store.SaveMembership(ctx, owner); err != nil {
			return err
		}
		return s.rel.Write(ctx, roleTuple(tenant.ID, userID, authz.RoleOwner))
	})
	if err != nil {
		return nil, err
	}
	uow.AfterCommit(ctx, func(ctx context.Context) {
		_ = s.cache.SetMembershipVersion(ctx, id.AppID, tenant.ID, userID, owner.Version)
	})
	return tenant, nil
}

// ListMine lists the caller's tenants in the app of their token.
func (s *TenantService) ListMine(ctx context.Context) ([]model.MyTenant, error) {
	id, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	return s.store.ListTenantsForUser(ctx, id.UserID) // in the app of the caller's token
}

func callerUserID(ctx context.Context) (string, error) {
	id, ok := reqctx.IdentityFrom(ctx)
	if !ok || id.UserID == "" {
		return "", fmt.Errorf("%w: no caller identity", apperr.ErrUnauthenticated)
	}
	return id.UserID, nil
}
