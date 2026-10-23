// Package service holds the admin service's use cases: admin accounts and
// sessions, and the registry of managed apps.
package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"admin/internal/model"
	"admin/internal/store"
	"core/apperr"
	"core/token"
)

// PasswordHasher is satisfied by *core/authn/password.Hasher.
type PasswordHasher interface {
	Hash(ctx context.Context, password string) (string, error)
	Verify(ctx context.Context, password, encoded string) (ok, needsRehash bool, err error)
	VerifyDummy(ctx context.Context, password string)
}

var errInvalidCredentials = fmt.Errorf("%w: invalid email or password", apperr.ErrUnauthenticated)

// Admins manages admin accounts and their sessions (signed tokens kept in a
// cookie by the HTTP layer).
type Admins struct {
	store    *store.Store
	hasher   PasswordHasher
	signer   *token.Signer
	verifier *token.Verifier
	audience string
	now      func() time.Time
}

func NewAdmins(st *store.Store, hasher PasswordHasher, signer *token.Signer, audience string) *Admins {
	return &Admins{store: st, hasher: hasher, signer: signer, verifier: signer.Verifier(audience), audience: audience, now: time.Now}
}

// Bootstrap creates the first admin when there is none (from configuration).
// Returns whether an account was created.
func (s *Admins) Bootstrap(ctx context.Context, email, password string) (bool, error) {
	n, err := s.store.Count(ctx)
	if err != nil || n > 0 {
		return false, err
	}
	if email == "" || password == "" {
		return false, errors.New("no admin account yet: set ADMIN_BOOTSTRAP_EMAIL and ADMIN_BOOTSTRAP_PASSWORD")
	}
	_, err = s.Create(ctx, email, password)
	return err == nil, err
}

// Create adds an admin account.
func (s *Admins) Create(ctx context.Context, email, password string) (*model.Admin, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if addr, err := mail.ParseAddress(email); err != nil || addr.Address != email {
		return nil, apperr.Invalid("email", "is not a valid email address")
	}
	if n := len([]rune(password)); n < 12 || n > 128 {
		return nil, apperr.Invalid("password", "must be 12-128 characters")
	}
	hash, err := s.hasher.Hash(ctx, password)
	if err != nil {
		return nil, err
	}
	a := &model.Admin{ID: "adm_" + randomHex(8), Email: email, PasswordHash: hash}
	if err := s.store.Create(ctx, a); err != nil {
		if errors.Is(err, apperr.ErrConflict) {
			return nil, fmt.Errorf("%w: an admin with this email exists", apperr.ErrConflict)
		}
		return nil, err
	}
	return a, nil
}

// Login checks the credentials and returns a session token and its expiry.
// Every failure takes about the same time and returns the same error.
func (s *Admins) Login(ctx context.Context, email, password string) (*model.Admin, string, time.Time, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	a, err := s.store.ByEmail(ctx, email)
	if errors.Is(err, apperr.ErrNotFound) {
		s.hasher.VerifyDummy(ctx, password)
		return nil, "", time.Time{}, errInvalidCredentials
	}
	if err != nil {
		return nil, "", time.Time{}, err
	}
	ok, _, err := s.hasher.Verify(ctx, password, a.PasswordHash)
	if err != nil || !ok {
		return nil, "", time.Time{}, errInvalidCredentials
	}
	session, exp, err := s.signer.Sign(a.ID, s.audience, "", "", 0)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	_ = s.store.TouchLogin(ctx, a.ID, s.now())
	return a, session, exp, nil
}

// Session returns the admin of a session token.
func (s *Admins) Session(ctx context.Context, session string) (*model.Admin, error) {
	claims, err := s.verifier.Verify(session)
	if err != nil {
		return nil, fmt.Errorf("%w: not signed in", apperr.ErrUnauthenticated)
	}
	a, err := s.store.ByID(ctx, claims.UserID())
	if errors.Is(err, apperr.ErrNotFound) {
		return nil, fmt.Errorf("%w: not signed in", apperr.ErrUnauthenticated)
	}
	return a, err
}

func (s *Admins) List(ctx context.Context) ([]model.Admin, error) { return s.store.List(ctx) }

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
