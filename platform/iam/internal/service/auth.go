package service

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"core/apperr"
	"core/logger"
	"core/uow"
	"iam/internal/model"
)

const (
	minPasswordRunes = 10
	maxPasswordRunes = 128

	// failed logins per email before it is locked
	loginMaxFailures = 5
	loginLockFor     = 15 * time.Minute
	loginWindow      = 15 * time.Minute
)

var (
	// the single error for every login failure: never reveal whether the email exists
	errInvalidCredentials = fmt.Errorf("%w: invalid email or password", apperr.ErrUnauthenticated)
	errLocked             = fmt.Errorf("%w: too many failed attempts, try again later", apperr.ErrTooManyRequests)
)

// AuthService handles password accounts. Successful logins end in
// TokenService.login, like every other way of signing in.
type AuthService struct {
	store   Store
	uow     uow.UnitOfWork
	metrics *authMetrics
	hasher  PasswordHasher
	tokens  *TokenService
	logger  logger.Logger
	now     func() time.Time
}

func NewAuthService(store Store, u uow.UnitOfWork, hasher PasswordHasher, tokens *TokenService, l logger.Logger) *AuthService {
	return &AuthService{store: store, uow: u, metrics: newAuthMetrics(), hasher: hasher, tokens: tokens, logger: l, now: time.Now}
}

// Register creates a password account and signs it in to appID.
func (s *AuthService) Register(ctx context.Context, email, password, appID string) (*model.User, *model.TokenPair, error) {
	email, err := normalizeEmail(email)
	if err != nil {
		return nil, nil, err
	}
	if err := requireApp(ctx, s.store, appID); err != nil {
		return nil, nil, err
	}
	if err := validatePassword("password", password, email); err != nil {
		return nil, nil, err
	}

	hash, err := s.hasher.Hash(ctx, password)
	if err != nil {
		return nil, nil, err
	}
	user := &model.User{ID: newUserID(), Email: &email, PasswordHash: hash}
	if err := s.store.CreateUser(ctx, user); err != nil {
		if errors.Is(err, apperr.ErrConflict) {
			return nil, nil, fmt.Errorf("%w: email is already registered", apperr.ErrConflict)
		}
		return nil, nil, err
	}

	pair, err := s.tokens.login(ctx, user.ID, appID, "")
	if err != nil {
		return nil, nil, err
	}
	s.metrics.registrations.Add(ctx, 1)
	return user, pair, nil
}

// Login verifies email + password and signs in to appID. All credential failures
// take the same time and return the same error, whether the email is registered or not.
func (s *AuthService) Login(ctx context.Context, email, password, appID, tenantID string) (*model.TokenPair, error) {
	// apps are not secret: an unknown app is reported before checking credentials
	if err := requireApp(ctx, s.store, appID); err != nil {
		return nil, err
	}
	email, err := normalizeEmail(email)
	if err != nil || password == "" || len([]rune(password)) > maxPasswordRunes {
		s.hasher.VerifyDummy(ctx, password)
		s.metrics.login(ctx, "invalid_credentials")
		return nil, errInvalidCredentials
	}

	user, err := s.verify(ctx, email, password)
	switch {
	case errors.Is(err, errInvalidCredentials):
		s.metrics.login(ctx, "invalid_credentials")
		return nil, err
	case errors.Is(err, errLocked):
		s.metrics.login(ctx, "locked")
		return nil, err
	case err != nil:
		return nil, err
	}
	s.metrics.login(ctx, "success")
	return s.tokens.login(ctx, user.ID, appID, tenantID)
}

// ChangePassword requires the current password and ends every session of the
// user (all tenants). Access tokens already issued stay valid until they expire.
func (s *AuthService) ChangePassword(ctx context.Context, current, next string) error {
	userID, err := callerUserID(ctx)
	if err != nil {
		return err
	}
	user, err := s.store.GetUserByID(ctx, userID)
	if err != nil {
		return err
	}
	if user.Email == nil || user.PasswordHash == "" {
		return fmt.Errorf("%w: this account has no password", apperr.ErrInvalid)
	}

	// same throttling as login: a stolen access token must not allow guessing the password
	if _, err := s.verify(ctx, *user.Email, current); err != nil {
		if errors.Is(err, errInvalidCredentials) {
			return fmt.Errorf("%w: current password is incorrect", apperr.ErrForbidden)
		}
		return err
	}
	if err := validatePassword("new_password", next, *user.Email); err != nil {
		return err
	}

	hash, err := s.hasher.Hash(ctx, next)
	if err != nil {
		return err
	}
	return s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.store.UpdateUserPassword(ctx, user.ID, hash); err != nil {
			return err
		}
		return s.store.RevokeRefreshTokensOfUser(ctx, user.ID)
	})
}

func (s *AuthService) Me(ctx context.Context) (*model.User, error) {
	userID, err := callerUserID(ctx)
	if err != nil {
		return nil, err
	}
	return s.store.GetUserByID(ctx, userID)
}

// verify checks the password of email with throttling. On success it resets the
// throttle and upgrades the hash if the parameters changed.
func (s *AuthService) verify(ctx context.Context, email, password string) (*model.User, error) {
	if t, err := s.store.GetLoginThrottle(ctx, email); err == nil {
		if t.LockedUntil != nil && s.now().Before(*t.LockedUntil) {
			return nil, errLocked
		}
	} else if !errors.Is(err, apperr.ErrNotFound) {
		return nil, err
	}

	user, err := s.store.GetUserByEmail(ctx, email)
	switch {
	case errors.Is(err, apperr.ErrNotFound):
		s.hasher.VerifyDummy(ctx, password)
		return nil, s.fail(ctx, email)
	case err != nil:
		return nil, err
	case user.PasswordHash == "":
		s.hasher.VerifyDummy(ctx, password)
		return nil, s.fail(ctx, email)
	}

	ok, needsRehash, err := s.hasher.Verify(ctx, password, user.PasswordHash)
	if err != nil {
		logger.WithContext(ctx, s.logger).WithFields(logger.Fields{"user_id": user.ID}).Errorf("verify password: %v", err)
		return nil, s.fail(ctx, email)
	}
	if !ok {
		return nil, s.fail(ctx, email)
	}

	if err := s.store.ResetLoginThrottle(ctx, email); err != nil {
		s.logger.Warnf("reset login throttle: %v", err)
	}
	if needsRehash {
		if hash, err := s.hasher.Hash(ctx, password); err == nil {
			if err := s.store.UpdateUserPassword(ctx, user.ID, hash); err != nil {
				s.logger.Warnf("upgrade password hash: %v", err)
			}
		}
	}
	return user, nil
}

func (s *AuthService) fail(ctx context.Context, email string) error {
	if err := s.store.RecordLoginFailure(ctx, email, s.now(), loginWindow, loginMaxFailures, loginLockFor); err != nil {
		s.logger.Errorf("record login failure: %v", err)
	}
	return errInvalidCredentials
}

func normalizeEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || len(email) > 254 || !strings.Contains(email[strings.LastIndex(email, "@")+1:], ".") {
		return "", apperr.Invalid("email", "is not a valid email address")
	}
	return email, nil
}

// validatePassword follows NIST SP 800-63B: length matters, composition rules don't.
// field is the public name of the input ("password", "new_password").
func validatePassword(field, password, email string) error {
	n := len([]rune(password))
	if n < minPasswordRunes || n > maxPasswordRunes {
		return apperr.Invalid(field, fmt.Sprintf("must be %d-%d characters", minPasswordRunes, maxPasswordRunes))
	}
	if strings.EqualFold(password, email) || strings.EqualFold(password, strings.Split(email, "@")[0]) {
		return apperr.Invalid(field, "must not be your email")
	}
	if strings.TrimSpace(password) == "" {
		return apperr.Invalid(field, "must not be blank")
	}
	return nil
}

func newUserID() string { return "usr_" + randomHex(8) }
