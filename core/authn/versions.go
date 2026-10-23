package authn

import (
	"context"
	"errors"
	"strconv"
	"time"

	"core/apperr"
	"core/logger"
	"core/token"

	"github.com/redis/go-redis/v9"
)

// versionSource reads the current version of a membership from the database
// (service.TokenService.MembershipVersion): apperr.ErrNotFound when it doesn't exist.
type versionSource interface {
	MembershipVersion(ctx context.Context, tenantID, userID string) (int64, error)
}

// versionLookup reads membership versions from Redis (written by the member
// services on every change) and falls back to the database on a miss or when
// Redis is unavailable. Fills use SET NX so they never overwrite a newer version.
type versionLookup struct {
	rdb    *redis.Client // nil: always read the database
	db     versionSource
	ttl    time.Duration
	logger logger.Logger
}

// NewVersionLookup: ttl must be at least the access token lifetime.
func NewVersionLookup(rdb *redis.Client, db versionSource, ttl time.Duration, l logger.Logger) MembershipVersions {
	return &versionLookup{rdb: rdb, db: db, ttl: ttl, logger: l}
}

func (v *versionLookup) Current(ctx context.Context, appID, tenantID, userID string) (int64, error) {
	key := token.MembershipVersionKey(appID, tenantID, userID)

	if v.rdb != nil {
		s, err := v.rdb.Get(ctx, key).Result()
		switch {
		case err == nil:
			if n, perr := strconv.ParseInt(s, 10, 64); perr == nil {
				return n, nil
			}
		case !errors.Is(err, redis.Nil):
			v.logger.Warnf("redis get %s: %v (falling back to the database)", key, err)
		}
	}

	version, err := v.db.MembershipVersion(ctx, tenantID, userID)
	switch {
	case errors.Is(err, apperr.ErrNotFound):
		version = token.RemovedMembershipVersion
	case err != nil:
		return 0, err
	}

	if v.rdb != nil {
		if err := v.rdb.SetNX(ctx, key, strconv.FormatInt(version, 10), v.ttl).Err(); err != nil {
			v.logger.Warnf("redis setnx %s: %v", key, err)
		}
	}
	return version, nil
}
