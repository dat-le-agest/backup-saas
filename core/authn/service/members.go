package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"core/apperr"
	"core/authn/model"
	"core/authz"
	"core/logger"
	"core/reqctx"
	"core/tenancy"
	"core/token"
	"core/uow"
)

// MemberService manages memberships of the caller's tenant.
//
// The caller's authority is re-read from the database inside the transaction
// rather than trusted from the token: a demoted admin can't use a token issued
// before the change, even through a path that skips the gateway's version check.
// Every change bumps the membership version so outstanding tokens go stale.
type MemberService struct {
	store  Store
	uow    uow.UnitOfWork
	az     authz.Authorizer
	cache  VersionCache
	logger logger.Logger
}

func NewMemberService(store Store, u uow.UnitOfWork, az authz.Authorizer, cache VersionCache, l logger.Logger) *MemberService {
	return &MemberService{store: store, uow: u, az: az, cache: cache, logger: l}
}

func (s *MemberService) List(ctx context.Context) ([]model.MemberView, error) {
	var members []model.MemberView
	err := s.uow.Do(ctx, func(ctx context.Context) error {
		caller, err := s.caller(ctx, authz.Read)
		if err != nil {
			return err
		}
		members, err = s.store.ListMemberViews(ctx, caller.TenantID)
		return err
	})
	return members, err
}

// MemberRef identifies the user to add: by id or by email (exactly one).
type MemberRef struct {
	UserID string
	Email  string
}

func (s *MemberService) Add(ctx context.Context, ref MemberRef, role string) (*model.MemberView, error) {
	if (ref.UserID == "") == (ref.Email == "") {
		return nil, fmt.Errorf("%w: give either user_id or email", apperr.ErrInvalid)
	}
	if ref.UserID != "" {
		if err := validateUserID(ref.UserID); err != nil {
			return nil, err
		}
	} else {
		email, err := normalizeEmail(ref.Email)
		if err != nil {
			return nil, err
		}
		ref.Email = email
	}
	if err := validateRole(role); err != nil {
		return nil, err
	}

	var (
		added *model.Membership
		user  *model.User
	)
	err := s.uow.Do(ctx, func(ctx context.Context) error {
		caller, err := s.caller(ctx, authz.Create)
		if err != nil {
			return err
		}
		if role == authz.RoleOwner && caller.Role != authz.RoleOwner {
			return fmt.Errorf("%w: only owners can grant the owner role", apperr.ErrForbidden)
		}

		// only existing users can be added (checked after authorization so
		// non-admins can't probe which emails are registered)
		if ref.Email != "" {
			user, err = s.store.GetUserByEmail(ctx, ref.Email)
		} else {
			user, err = s.store.GetUserByID(ctx, ref.UserID)
		}
		if errors.Is(err, apperr.ErrNotFound) {
			return fmt.Errorf("%w: no such user", apperr.ErrNotFound)
		}
		if err != nil {
			return err
		}
		userID := user.ID

		existing, err := s.store.GetMembershipAny(ctx, caller.TenantID, userID)
		switch {
		case err == nil && existing.Active():
			return fmt.Errorf("%w: user is already a member", apperr.ErrConflict)
		case err == nil:
			// re-add: keep the version increasing so old tokens never match again
			existing.Role, existing.Version, existing.RemovedAt = role, existing.Version+1, nil
			added = existing
		case errors.Is(err, apperr.ErrNotFound):
			added = &model.Membership{TenantID: caller.TenantID, UserID: userID, Role: role, Version: 1}
		default:
			return err
		}
		return s.store.SaveMembership(ctx, added)
	})
	if err != nil {
		return nil, err
	}
	s.publish(ctx, added.TenantID, added.UserID, added.Version)
	return &model.MemberView{Membership: *added, Email: user.EmailOrEmpty()}, nil
}

func (s *MemberService) UpdateRole(ctx context.Context, userID, role string) (*model.MemberView, error) {
	if err := validateRole(role); err != nil {
		return nil, err
	}

	var target *model.Membership
	err := s.uow.Do(ctx, func(ctx context.Context) error {
		caller, err := s.caller(ctx, authz.Update)
		if err != nil {
			return err
		}
		target, err = s.store.GetMembership(ctx, caller.TenantID, userID)
		if err != nil {
			return err
		}
		if caller.Role != authz.RoleOwner && (target.Role == authz.RoleOwner || role == authz.RoleOwner) {
			return fmt.Errorf("%w: only owners can change owners or grant the owner role", apperr.ErrForbidden)
		}
		if target.Role == role {
			return nil
		}
		if err := s.keepAnOwner(ctx, target, role); err != nil {
			return err
		}

		target.Role, target.Version = role, target.Version+1
		return s.store.SaveMembership(ctx, target)
	})
	if err != nil {
		return nil, err
	}
	s.publish(ctx, target.TenantID, target.UserID, target.Version)

	view := &model.MemberView{Membership: *target}
	if u, err := s.store.GetUserByID(ctx, target.UserID); err == nil {
		view.Email = u.EmailOrEmpty()
	}
	return view, nil
}

// Remove: owners/admins remove others; anyone may remove themselves (leave).
func (s *MemberService) Remove(ctx context.Context, userID string) error {
	var target *model.Membership
	err := s.uow.Do(ctx, func(ctx context.Context) error {
		self := false
		if id, ok := reqctx.IdentityFrom(ctx); ok && id.UserID == userID {
			self = true
		}

		action := authz.Delete
		if self {
			action = authz.Read // leaving only requires being a member
		}
		caller, err := s.caller(ctx, action)
		if err != nil {
			return err
		}

		target, err = s.store.GetMembership(ctx, caller.TenantID, userID)
		if err != nil {
			return err
		}
		if !self && caller.Role != authz.RoleOwner && target.Role == authz.RoleOwner {
			return fmt.Errorf("%w: only owners can remove owners", apperr.ErrForbidden)
		}
		if err := s.keepAnOwner(ctx, target, ""); err != nil {
			return err
		}

		now := time.Now()
		target.RemovedAt, target.Version = &now, target.Version+1
		if err := s.store.SaveMembership(ctx, target); err != nil {
			return err
		}
		// sessions in this tenant end now, not when their refresh token expires
		return s.store.RevokeRefreshTokensOf(ctx, target.TenantID, target.UserID)
	})
	if err != nil {
		return err
	}
	s.publish(ctx, target.TenantID, target.UserID, token.RemovedMembershipVersion)
	return nil
}

// caller locks the tenant, loads the caller's current membership and checks action.
func (s *MemberService) caller(ctx context.Context, action string) (*model.Membership, error) {
	tenantID, err := tenancy.TenantID(ctx)
	if err != nil {
		return nil, err
	}
	id, _ := reqctx.IdentityFrom(ctx)

	if err := s.store.LockTenant(ctx, tenantID); err != nil {
		return nil, err
	}
	caller, err := s.store.GetMembership(ctx, tenantID, id.UserID)
	if errors.Is(err, apperr.ErrNotFound) {
		return nil, fmt.Errorf("%w: not a member of this tenant", apperr.ErrForbidden)
	}
	if err != nil {
		return nil, err
	}

	// authorize with the role from the database, not the one in the token
	dbCtx := reqctx.WithIdentity(ctx, reqctx.Identity{UserID: id.UserID, TenantID: tenantID, Role: caller.Role})
	if err := s.az.Check(dbCtx, action, authz.Resource{Type: ResourceMember, TenantID: tenantID}); err != nil {
		return nil, err
	}
	return caller, nil
}

// keepAnOwner refuses to demote (newRole != owner) or remove (newRole == "") the last owner.
func (s *MemberService) keepAnOwner(ctx context.Context, target *model.Membership, newRole string) error {
	if target.Role != authz.RoleOwner || newRole == authz.RoleOwner {
		return nil
	}
	owners, err := s.store.CountOwners(ctx, target.TenantID)
	if err != nil {
		return err
	}
	if owners <= 1 {
		return fmt.Errorf("%w: a tenant must keep at least one owner", apperr.ErrInvalid)
	}
	return nil
}

// publish writes the new version for the gateway once the outermost transaction
// commits (also when this use case runs inside a larger unit of work). On failure
// the gateway may accept old tokens until the cached key expires (≤ access token TTL).
func (s *MemberService) publish(ctx context.Context, tenantID, userID string, version int64) {
	uow.AfterCommit(ctx, func(ctx context.Context) {
		if err := s.cache.SetMembershipVersion(ctx, tenantID, userID, version); err != nil {
			logger.WithContext(ctx, s.logger).WithFields(logger.Fields{"member_tenant_id": tenantID, "member_user_id": userID}).
				Errorf("publish membership version: %v", err)
		}
	})
}

func validateRole(role string) error {
	if !authz.IsDefaultRole(role) {
		return apperr.Invalid("role", "must be one of owner, admin, member, viewer")
	}
	return nil
}
