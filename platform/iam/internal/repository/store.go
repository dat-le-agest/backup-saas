package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"core/apperr"
	"core/migrate"
	"core/uow"
	"iam/internal/model"
	"iam/internal/service"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// store implements service.Store on gorm/Postgres. iam is the one service whose
// queries span tenants (e.g. "my tenants"), so it uses neither tenancy.Scope nor
// row-level security: the service passes tenant ids explicitly.
//
// Shared tables (apps, users, login_throttles) are in the default schema. The
// tables of an app (tenants, memberships, refresh_tokens) are in the app's
// schema: the app comes from ctx (service.AppOf) and every query on them goes
// through in(), which qualifies the table with that schema.
type store struct {
	uow *uow.Gorm
}

// in returns a query on table of the app of ctx, e.g. notes.tenants.
func (s *store) in(ctx context.Context, table string) (*gorm.DB, error) {
	app, err := appSchema(ctx)
	if err != nil {
		return nil, err
	}
	// gorm quotes each part of "schema.table"; app ids are validated identifiers
	return s.conn(ctx).Table(app + "." + table), nil
}

// appSchema is the app of ctx, checked to be a valid schema name.
func appSchema(ctx context.Context) (string, error) {
	app := service.AppOf(ctx)
	if app == "" {
		return "", errors.New("iam store: no app in context")
	}
	if _, err := migrate.QuoteSchema(app); err != nil {
		return "", fmt.Errorf("iam store: %w", err)
	}
	return app, nil
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

// EnsureApps registers the apps that don't exist yet (their schemas are
// created by the migrations, core/migrate.UpInSchema).
func (s *store) EnsureApps(ctx context.Context, ids []string) error {
	apps := make([]model.App, len(ids))
	for i, id := range ids {
		apps[i] = model.App{ID: id}
	}
	return s.conn(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&apps).Error
}

func (s *store) ListApps(ctx context.Context) ([]string, error) {
	var ids []string
	err := s.conn(ctx).Model(&model.App{}).Order("id").Pluck("id", &ids).Error
	return ids, err
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
	db, err := s.in(ctx, "tenants")
	if err != nil {
		return err
	}
	if err := db.Create(t).Error; err != nil {
		return err
	}
	t.AppID = service.AppOf(ctx)
	return nil
}

func (s *store) GetTenant(ctx context.Context, id string) (*model.Tenant, error) {
	db, err := s.in(ctx, "tenants")
	if err != nil {
		return nil, err
	}
	var t model.Tenant
	if err := db.Where("id = ?", id).First(&t).Error; err != nil {
		return nil, notFound(err)
	}
	t.AppID = service.AppOf(ctx)
	return &t, nil
}

// LockTenant serializes membership changes of a tenant (e.g. two admins demoting
// the last two owners at once). A row update takes a row lock on MySQL and the
// write lock on SQLite.
func (s *store) LockTenant(ctx context.Context, tenantID string) error {
	db, err := s.in(ctx, "tenants")
	if err != nil {
		return err
	}
	res := db.Where("id = ?", tenantID).Update("updated_at", time.Now())
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return apperr.ErrNotFound
	}
	return nil
}

// ListTenantsForUser lists the tenants of the app of ctx the user is an active member of.
func (s *store) ListTenantsForUser(ctx context.Context, userID string) ([]model.MyTenant, error) {
	app, err := appSchema(ctx)
	if err != nil {
		return nil, err
	}
	q, _ := migrate.QuoteSchema(app)
	var rows []struct {
		model.Tenant
		Role string
	}
	err = s.conn(ctx).
		Table(app+".tenants").
		Select("tenants.*, m.role").
		Joins("JOIN "+q+".memberships m ON m.tenant_id = tenants.id").
		Where("m.user_id = ? AND m.removed_at IS NULL", userID).
		Order("tenants.created_at").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	out := make([]model.MyTenant, 0, len(rows))
	for _, r := range rows {
		r.Tenant.AppID = app
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
	db, err := s.in(ctx, "memberships")
	if err != nil {
		return nil, err
	}
	var m model.Membership
	if err := db.Where("tenant_id = ? AND user_id = ?", tenantID, userID).First(&m).Error; err != nil {
		return nil, notFound(err)
	}
	return &m, nil
}

func (s *store) ListMembers(ctx context.Context, tenantID string) ([]model.Membership, error) {
	db, err := s.in(ctx, "memberships")
	if err != nil {
		return nil, err
	}
	members := []model.Membership{}
	err = db.
		Where("tenant_id = ? AND removed_at IS NULL", tenantID).
		Order("created_at, user_id").
		Find(&members).Error
	return members, err
}

func (s *store) CountOwners(ctx context.Context, tenantID string) (int64, error) {
	db, err := s.in(ctx, "memberships")
	if err != nil {
		return 0, err
	}
	var n int64
	err = db.
		Where("tenant_id = ? AND role = ? AND removed_at IS NULL", tenantID, "owner").
		Count(&n).Error
	return n, err
}

// SaveMembership inserts or fully updates a membership row.
func (s *store) SaveMembership(ctx context.Context, m *model.Membership) error {
	db, err := s.in(ctx, "memberships")
	if err != nil {
		return err
	}
	return db.Save(m).Error
}

// ---- refresh tokens ----

func (s *store) CreateRefreshToken(ctx context.Context, t *model.RefreshToken) error {
	db, err := s.in(ctx, "refresh_tokens")
	if err != nil {
		return err
	}
	return db.Create(t).Error
}

func (s *store) GetRefreshToken(ctx context.Context, hash string) (*model.RefreshToken, error) {
	db, err := s.in(ctx, "refresh_tokens")
	if err != nil {
		return nil, err
	}
	var t model.RefreshToken
	if err := db.Where("token_hash = ?", hash).First(&t).Error; err != nil {
		return nil, notFound(err)
	}
	return &t, nil
}

// MarkRefreshTokenUsed atomically flips used_at; false means it was already used
// (concurrent or replayed refresh).
func (s *store) MarkRefreshTokenUsed(ctx context.Context, id uint) (bool, error) {
	db, err := s.in(ctx, "refresh_tokens")
	if err != nil {
		return false, err
	}
	res := db.Where("id = ? AND used_at IS NULL", id).Update("used_at", time.Now())
	return res.RowsAffected == 1, res.Error
}

func (s *store) RevokeRefreshTokenFamily(ctx context.Context, familyID string) error {
	db, err := s.in(ctx, "refresh_tokens")
	if err != nil {
		return err
	}
	return db.Where("family_id = ? AND revoked_at IS NULL", familyID).Update("revoked_at", time.Now()).Error
}

// RevokeRefreshTokensOf revokes every refresh token of a user in a tenant
// (membership removed).
func (s *store) RevokeRefreshTokensOf(ctx context.Context, tenantID, userID string) error {
	db, err := s.in(ctx, "refresh_tokens")
	if err != nil {
		return err
	}
	return db.Where("user_id = ? AND tenant_id = ? AND revoked_at IS NULL", userID, tenantID).
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

// RevokeRefreshTokensOfUser revokes every session of a user, in every app.
func (s *store) RevokeRefreshTokensOfUser(ctx context.Context, userID string) error {
	apps, err := s.ListApps(ctx)
	if err != nil {
		return err
	}
	for _, app := range apps {
		db, err := s.in(service.InApp(ctx, app), "refresh_tokens")
		if err != nil {
			return err
		}
		if err := db.Where("user_id = ? AND revoked_at IS NULL", userID).Update("revoked_at", time.Now()).Error; err != nil {
			return err
		}
	}
	return nil
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
	db, err := s.in(ctx, "memberships")
	if err != nil {
		return nil, err
	}
	views := []model.MemberView{}
	err = db.
		Select("memberships.*, COALESCE(users.email, '') AS email").
		Joins("LEFT JOIN public.users ON users.id = memberships.user_id").
		Where("memberships.tenant_id = ? AND memberships.removed_at IS NULL", tenantID).
		Order("memberships.created_at, memberships.user_id").
		Scan(&views).Error
	return views, err
}
