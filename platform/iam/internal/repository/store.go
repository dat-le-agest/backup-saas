package repository

import (
	"context"
	"errors"
	"time"

	"core/apperr"
	"core/uow"
	"iam/internal/model"
	"iam/internal/service"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// store implements service.Store on gorm/Postgres. iam is the one service whose
// queries span tenants (e.g. "my tenants"), so it uses neither tenancy.Scope nor
// row-level security: the service passes tenant ids explicitly.
type store struct {
	uow *uow.Gorm
}

func NewStore(u *uow.Gorm) *store {
	return &store{uow: u}
}

// conn joins the unit of work of ctx, if any (no row-level security in iam,
// so a plain connection is fine outside one).
func (s *store) conn(ctx context.Context) *gorm.DB {
	return s.uow.DB(ctx)
}

var _ service.Store = (*store)(nil)

func notFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apperr.ErrNotFound
	}
	return err
}

// ---- apps ----

// EnsureApps registers the apps that don't exist yet.
func (s *store) EnsureApps(ctx context.Context, ids []string) error {
	apps := make([]model.App, len(ids))
	for i, id := range ids {
		apps[i] = model.App{ID: id}
	}
	return s.conn(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&apps).Error
}

func (s *store) GetApp(ctx context.Context, id string) (*model.App, error) {
	var a model.App
	if err := s.conn(ctx).Where("id = ?", id).First(&a).Error; err != nil {
		return nil, notFound(err)
	}
	return &a, nil
}

// ---- tenants ----

func (s *store) CreateTenant(ctx context.Context, t *model.Tenant) error {
	return s.conn(ctx).Create(t).Error
}

func (s *store) GetTenant(ctx context.Context, id string) (*model.Tenant, error) {
	var t model.Tenant
	if err := s.conn(ctx).Where("id = ?", id).First(&t).Error; err != nil {
		return nil, notFound(err)
	}
	return &t, nil
}

// LockTenant serializes membership changes of a tenant (e.g. two admins demoting
// the last two owners at once). A row update takes a row lock on MySQL and the
// write lock on SQLite.
func (s *store) LockTenant(ctx context.Context, tenantID string) error {
	res := s.conn(ctx).Model(&model.Tenant{}).Where("id = ?", tenantID).Update("updated_at", time.Now())
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return apperr.ErrNotFound
	}
	return nil
}

// ListTenantsForUser lists the tenants of appID the user is an active member of.
func (s *store) ListTenantsForUser(ctx context.Context, appID, userID string) ([]model.MyTenant, error) {
	var rows []struct {
		model.Tenant
		Role string
	}
	err := s.conn(ctx).
		Table("tenants").
		Select("tenants.*, memberships.role").
		Joins("JOIN memberships ON memberships.tenant_id = tenants.id").
		Where("tenants.app_id = ? AND memberships.user_id = ? AND memberships.removed_at IS NULL", appID, userID).
		Order("tenants.created_at").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	out := make([]model.MyTenant, 0, len(rows))
	for _, r := range rows {
		out = append(out, model.MyTenant{Tenant: r.Tenant, Role: r.Role})
	}
	return out, nil
}

// ---- memberships ----

// GetMembership returns an active membership, apperr.ErrNotFound otherwise.
func (s *store) GetMembership(ctx context.Context, tenantID, userID string) (*model.Membership, error) {
	m, err := s.GetMembershipAny(ctx, tenantID, userID)
	if err != nil {
		return nil, err
	}
	if !m.Active() {
		return nil, apperr.ErrNotFound
	}
	return m, nil
}

// GetMembershipAny includes removed memberships (to re-add with a higher version).
func (s *store) GetMembershipAny(ctx context.Context, tenantID, userID string) (*model.Membership, error) {
	var m model.Membership
	err := s.conn(ctx).Where("tenant_id = ? AND user_id = ?", tenantID, userID).First(&m).Error
	if err != nil {
		return nil, notFound(err)
	}
	return &m, nil
}

func (s *store) ListMembers(ctx context.Context, tenantID string) ([]model.Membership, error) {
	members := []model.Membership{}
	err := s.conn(ctx).
		Where("tenant_id = ? AND removed_at IS NULL", tenantID).
		Order("created_at, user_id").
		Find(&members).Error
	return members, err
}

func (s *store) CountOwners(ctx context.Context, tenantID string) (int64, error) {
	var n int64
	err := s.conn(ctx).Model(&model.Membership{}).
		Where("tenant_id = ? AND role = ? AND removed_at IS NULL", tenantID, "owner").
		Count(&n).Error
	return n, err
}

// SaveMembership inserts or fully updates a membership row.
func (s *store) SaveMembership(ctx context.Context, m *model.Membership) error {
	return s.conn(ctx).Save(m).Error
}

// ---- refresh tokens ----

func (s *store) CreateRefreshToken(ctx context.Context, t *model.RefreshToken) error {
	return s.conn(ctx).Create(t).Error
}

func (s *store) GetRefreshToken(ctx context.Context, hash string) (*model.RefreshToken, error) {
	var t model.RefreshToken
	if err := s.conn(ctx).Where("token_hash = ?", hash).First(&t).Error; err != nil {
		return nil, notFound(err)
	}
	return &t, nil
}

// MarkRefreshTokenUsed atomically flips used_at; false means it was already used
// (concurrent or replayed refresh).
func (s *store) MarkRefreshTokenUsed(ctx context.Context, id uint) (bool, error) {
	res := s.conn(ctx).Model(&model.RefreshToken{}).
		Where("id = ? AND used_at IS NULL", id).
		Update("used_at", time.Now())
	return res.RowsAffected == 1, res.Error
}

func (s *store) RevokeRefreshTokenFamily(ctx context.Context, familyID string) error {
	return s.conn(ctx).Model(&model.RefreshToken{}).
		Where("family_id = ? AND revoked_at IS NULL", familyID).
		Update("revoked_at", time.Now()).Error
}

// RevokeRefreshTokensOf revokes every refresh token of a user in a tenant
// (membership removed).
func (s *store) RevokeRefreshTokensOf(ctx context.Context, tenantID, userID string) error {
	return s.conn(ctx).Model(&model.RefreshToken{}).
		Where("user_id = ? AND tenant_id = ? AND revoked_at IS NULL", userID, tenantID).
		Update("revoked_at", time.Now()).Error
}

// ---- users ----

// CreateUser returns apperr.ErrConflict if the email is taken.
func (s *store) CreateUser(ctx context.Context, u *model.User) error {
	err := s.conn(ctx).Create(u).Error
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return apperr.ErrConflict
	}
	return err
}

// EnsureUser creates a credential-less user with this id if it doesn't exist (dev login).
// Safe under concurrency: a parallel insert of the same id is ignored.
func (s *store) EnsureUser(ctx context.Context, id string) error {
	return s.conn(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&model.User{ID: id}).Error
}

func (s *store) GetUserByID(ctx context.Context, id string) (*model.User, error) {
	var u model.User
	if err := s.conn(ctx).Where("id = ?", id).First(&u).Error; err != nil {
		return nil, notFound(err)
	}
	return &u, nil
}

func (s *store) GetUserByEmail(ctx context.Context, email string) (*model.User, error) {
	var u model.User
	if err := s.conn(ctx).Where("email = ?", email).First(&u).Error; err != nil {
		return nil, notFound(err)
	}
	return &u, nil
}

func (s *store) UpdateUserPassword(ctx context.Context, id, hash string) error {
	return s.conn(ctx).Model(&model.User{}).Where("id = ?", id).Update("password_hash", hash).Error
}

// RevokeRefreshTokensOfUser revokes every session of a user in every tenant.
func (s *store) RevokeRefreshTokensOfUser(ctx context.Context, userID string) error {
	return s.conn(ctx).Model(&model.RefreshToken{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Update("revoked_at", time.Now()).Error
}

// ---- login throttling ----

func (s *store) GetLoginThrottle(ctx context.Context, email string) (*model.LoginThrottle, error) {
	var t model.LoginThrottle
	if err := s.conn(ctx).Where("email = ?", email).First(&t).Error; err != nil {
		return nil, notFound(err)
	}
	return &t, nil
}

// RecordLoginFailure counts a failure for email. Failures older than window are
// forgotten; reaching maxFailures locks the email until now+lockFor.
//
// The increment is a single upsert, so concurrent failures (a parallel brute
// force) are all counted: a read-then-write would lose updates.
func (s *store) RecordLoginFailure(ctx context.Context, email string, now time.Time, window time.Duration, maxFailures int, lockFor time.Duration) error {
	return s.conn(ctx).Transaction(func(tx *gorm.DB) error {
		var failures int
		err := tx.Raw(`
			INSERT INTO login_throttles AS t (email, failures, locked_until, updated_at)
			VALUES (@email, 1, NULL, @now)
			ON CONFLICT (email) DO UPDATE SET
				failures = CASE
					WHEN t.updated_at < @window_start OR (t.locked_until IS NOT NULL AND t.locked_until <= @now) THEN 1
					ELSE t.failures + 1
				END,
				locked_until = CASE WHEN t.locked_until > @now THEN t.locked_until END,
				updated_at = @now
			RETURNING failures`,
			map[string]any{"email": email, "now": now, "window_start": now.Add(-window)},
		).Scan(&failures).Error
		if err != nil {
			return err
		}
		if failures < maxFailures {
			return nil
		}
		return tx.Model(&model.LoginThrottle{}).
			Where("email = ? AND failures >= ?", email, maxFailures).
			Updates(map[string]any{"failures": 0, "locked_until": now.Add(lockFor)}).Error
	})
}

func (s *store) ResetLoginThrottle(ctx context.Context, email string) error {
	return s.conn(ctx).Where("email = ?", email).Delete(&model.LoginThrottle{}).Error
}

// ListMemberViews lists active members with their email.
func (s *store) ListMemberViews(ctx context.Context, tenantID string) ([]model.MemberView, error) {
	views := []model.MemberView{}
	err := s.conn(ctx).
		Table("memberships").
		Select("memberships.*, COALESCE(users.email, '') AS email").
		Joins("LEFT JOIN users ON users.id = memberships.user_id").
		Where("memberships.tenant_id = ? AND memberships.removed_at IS NULL", tenantID).
		Order("memberships.created_at, memberships.user_id").
		Scan(&views).Error
	return views, err
}
