package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"core/apperr"
	"core/reqctx"
)

// app ids are the folder names under apps/: lowercase letters, digits, hyphens
var appIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// RegisterApps makes sure the configured apps exist (iam --iam-apps, at startup).
// Apps are never removed here: tenants of an app dropped from the list keep it.
func RegisterApps(ctx context.Context, store Store, ids []string) error {
	if len(ids) == 0 {
		return errors.New("no apps configured (--iam-apps / IAM_APPS, e.g. notes)")
	}
	for _, id := range ids {
		if !appIDPattern.MatchString(id) {
			return fmt.Errorf("invalid app id %q (lowercase letters, digits and hyphens)", id)
		}
	}
	return store.EnsureApps(ctx, ids)
}

// requireApp returns an invalid input error unless appID is a registered app.
func requireApp(ctx context.Context, store Store, appID string) error {
	if appID == "" {
		return apperr.Invalid("app", "is required")
	}
	_, err := store.GetApp(ctx, appID)
	if errors.Is(err, apperr.ErrNotFound) {
		return apperr.Invalid("app", "is not a known app")
	}
	return err
}

// caller is the identity of a signed-in caller: a user and the app of their token.
func caller(ctx context.Context) (reqctx.Identity, error) {
	id, ok := reqctx.IdentityFrom(ctx)
	if !ok || id.UserID == "" {
		return reqctx.Identity{}, fmt.Errorf("%w: no caller identity", apperr.ErrUnauthenticated)
	}
	if id.AppID == "" {
		return reqctx.Identity{}, fmt.Errorf("%w: the access token has no app", apperr.ErrUnauthenticated)
	}
	return id, nil
}
