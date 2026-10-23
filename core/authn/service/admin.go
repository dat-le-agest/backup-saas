package service

import (
	"context"
	"fmt"
	"time"

	"core/apperr"
	"core/authn/model"
	"core/logger"
	"core/token"
	"core/uow"
)

// AdminService is what platform administrators may do in the app, through its
// admin API (never exposed publicly): browse and manage users, tenants and
// members. Changes take effect at once: outstanding tokens of affected
// memberships go stale and their sessions end.
type AdminService struct {
	store   Store
	uow     uow.UnitOfWork
	members *MemberService
	hasher  PasswordHasher
	cache   VersionCache
	logger  logger.Logger
	now     func() time.Time
}

func NewAdminService(store Store, u uow.UnitOfWork, members *MemberService, hasher PasswordHasher, cache VersionCache, l logger.Logger) *AdminService {
	return &AdminService{store: store, uow: u, members: members, hasher: hasher, cache: cache, logger: l, now: time.Now}
}

// Stats are totals for the dashboard.
type Stats struct {
	Users, Tenants int64
}

func (s *AdminService) Stats(ctx context.Context) (Stats, error) {
	_, users, err := s.store.ListUsers(ctx, "", model.Page{Number: 1, Limit: 1})
	if err != nil {
		return Stats{}, err
	}
	_, tenants, err := s.store.ListTenants(ctx, "", model.Page{Number: 1, Limit: 1})
	if err != nil {
		return Stats{}, err
	}
	return Stats{Users: users, Tenants: tenants}, nil
}

// ---- users ----

func (s *AdminService) ListUsers(ctx context.Context, query string, p model.Page) ([]model.User, int64, error) {
	return s.store.ListUsers(ctx, query, p)
}

// GetUser returns a user with their active memberships.
func (s *AdminService) GetUser(ctx context.Context, id string) (*model.UserView, error) {
	u, err := s.store.GetUserByID(ctx, id)
	if err != nil {
		return nil, err
	}
	ms, err := s.store.ListMembershipsOfUser(ctx, id)
	if err != nil {
		return nil, err
	}
	return &model.UserView{User: *u, Memberships: ms}, nil
}

// SetUserDisabled disables a user (sign-in and refresh refused, sessions end,
// tenant tokens go stale) or enables them again.
func (s *AdminService) SetUserDisabled(ctx context.Context, id string, disabled bool) (*model.User, error) {
	var bumped []model.Membership
	err := s.uow.Do(ctx, func(ctx context.Context) error {
		if !disabled {
			return s.store.SetUserDisabled(ctx, id, nil)
		}
		now := s.now()
		if err := s.store.SetUserDisabled(ctx, id, &now); err != nil {
			return err
		}
		var err error
		if bumped, err = s.store.BumpMembershipVersionsOfUser(ctx, id); err != nil {
			return err
		}
		return s.store.RevokeRefreshTokensOfUser(ctx, id)
	})
	if err != nil {
		return nil, err
	}
	for _, m := range bumped {
		s.members.publish(ctx, m.TenantID, m.UserID, m.Version)
	}
	return s.store.GetUserByID(ctx, id)
}

// SetPassword replaces the password of a user with an email and ends their sessions.
func (s *AdminService) SetPassword(ctx context.Context, id, password string) error {
	u, err := s.store.GetUserByID(ctx, id)
	if err != nil {
		return err
	}
	if u.Email == nil {
		return fmt.Errorf("%w: this user has no email: password sign-in isn't possible", apperr.ErrInvalid)
	}
	if err := validatePassword("password", password, *u.Email); err != nil {
		return err
	}
	hash, err := s.hasher.Hash(ctx, password)
	if err != nil {
		return err
	}
	return s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.store.UpdateUserPassword(ctx, id, hash); err != nil {
			return err
		}
		return s.store.RevokeRefreshTokensOfUser(ctx, id)
	})
}

// ---- tenants ----

func (s *AdminService) ListTenants(ctx context.Context, query string, p model.Page) ([]model.TenantView, int64, error) {
	return s.store.ListTenants(ctx, query, p)
}

// GetTenant returns a tenant with its members.
func (s *AdminService) GetTenant(ctx context.Context, id string) (*model.Tenant, []model.MemberView, error) {
	t, err := s.store.GetTenant(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	members, err := s.store.ListMemberViews(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	return t, members, nil
}

// DeleteTenant removes every membership of the tenant and hides it. The app's
// own data of the tenant (e.g. notes) is left to the app.
func (s *AdminService) DeleteTenant(ctx context.Context, id string) error {
	var removed []model.Membership
	err := s.uow.Do(ctx, func(ctx context.Context) error {
		var err error
		removed, err = s.store.DeleteTenant(ctx, id)
		return err
	})
	if err != nil {
		return err
	}
	for _, m := range removed {
		s.members.publish(ctx, m.TenantID, m.UserID, token.RemovedMembershipVersion)
	}
	return nil
}

// UpdateMemberRole sets a member's role in any tenant. The last owner can't be demoted.
func (s *AdminService) UpdateMemberRole(ctx context.Context, tenantID, userID, role string) (*model.MemberView, error) {
	if err := validateRole(role); err != nil {
		return nil, err
	}
	var target *model.Membership
	err := s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.store.LockTenant(ctx, tenantID); err != nil {
			return err
		}
		var err error
		if target, err = s.store.GetMembership(ctx, tenantID, userID); err != nil {
			return err
		}
		if target.Role == role {
			return nil
		}
		if err := s.members.keepAnOwner(ctx, target, role); err != nil {
			return err
		}
		target.Role, target.Version = role, target.Version+1
		return s.store.SaveMembership(ctx, target)
	})
	if err != nil {
		return nil, err
	}
	s.members.publish(ctx, tenantID, userID, target.Version)
	view := &model.MemberView{Membership: *target}
	if u, err := s.store.GetUserByID(ctx, userID); err == nil {
		view.Email = u.EmailOrEmpty()
	}
	return view, nil
}

// RemoveMember removes a member from a tenant and ends their sessions there.
// The last owner can't be removed (delete the tenant instead).
func (s *AdminService) RemoveMember(ctx context.Context, tenantID, userID string) error {
	err := s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.store.LockTenant(ctx, tenantID); err != nil {
			return err
		}
		target, err := s.store.GetMembership(ctx, tenantID, userID)
		if err != nil {
			return err
		}
		if err := s.members.keepAnOwner(ctx, target, ""); err != nil {
			return err
		}
		now := s.now()
		target.RemovedAt, target.Version = &now, target.Version+1
		if err := s.store.SaveMembership(ctx, target); err != nil {
			return err
		}
		return s.store.RevokeRefreshTokensOf(ctx, tenantID, userID)
	})
	if err != nil {
		return err
	}
	s.members.publish(ctx, tenantID, userID, token.RemovedMembershipVersion)
	return nil
}
