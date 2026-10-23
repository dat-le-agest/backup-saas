// Package authn is the authentication of one app, embedded in the app's
// gateway: users and passwords, access and refresh tokens, tenants and
// memberships in the app's own database, the public endpoints
// (/v1/auth/*, /v1/me, /v1/tenants, /v1/members) and an admin API for the
// platform's admin dashboard. Wire it with New.
package authn

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"core/apperr"
	"core/reqctx"
	"core/token"
)

// MembershipVersions returns the current version of a membership,
// token.RemovedMembershipVersion if it no longer exists.
type MembershipVersions interface {
	Current(ctx context.Context, appID, tenantID, userID string) (int64, error)
}

var (
	errMissingToken = fmt.Errorf(`%w: expected "Authorization: Bearer <access token>"`, apperr.ErrUnauthenticated)
	errBadToken     = fmt.Errorf("%w: invalid or expired access token", apperr.ErrUnauthenticated)
	errStaleToken   = fmt.Errorf("%w: membership changed, refresh the access token", apperr.ErrUnauthenticated)
)

// JWTAuthenticator verifies "Authorization: Bearer <jwt>" and, for tenant-scoped
// tokens, rejects them once the membership changed (role change, removal) even
// before they expire.
type JWTAuthenticator struct {
	verifier *token.Verifier
	versions MembershipVersions
}

func NewJWTAuthenticator(v *token.Verifier, versions MembershipVersions) *JWTAuthenticator {
	return &JWTAuthenticator{verifier: v, versions: versions}
}

func (a *JWTAuthenticator) Authenticate(r *http.Request) (reqctx.Identity, error) {
	scheme, raw, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(raw) == "" {
		return reqctx.Identity{}, errMissingToken
	}

	claims, err := a.verifier.Verify(strings.TrimSpace(raw))
	if err != nil {
		return reqctx.Identity{}, errBadToken
	}

	id := reqctx.Identity{AppID: claims.AppID(), UserID: claims.UserID(), TenantID: claims.TenantID, Role: claims.Role}
	if claims.TenantID == "" {
		return id, nil
	}

	current, err := a.versions.Current(r.Context(), claims.AppID(), claims.TenantID, claims.UserID())
	if err != nil {
		// can't confirm the membership: fail closed with 503/504 (not 401) so clients retry
		return reqctx.Identity{}, err
	}
	if current != claims.MembershipVersion {
		return reqctx.Identity{}, errStaleToken
	}
	return id, nil
}
