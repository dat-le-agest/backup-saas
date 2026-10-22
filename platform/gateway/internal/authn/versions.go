package authn

import (
	"context"
	"errors"
	"strconv"
	"time"

	iamv1 "api/gen/go/platform/iam/v1"
	"api/gen/go/platform/iam/v1/iamv1connect"
	"core/apperr"
	"core/logger"
	"core/token"

	"github.com/redis/go-redis/v9"
)

// versionLookup reads membership versions from Redis (written by iam on every
// change) and falls back to iam.GetMembershipVersion on a miss or when Redis is
// unavailable. Fills use SET NX so they never overwrite a newer version iam wrote.
type versionLookup struct {
	rdb    *redis.Client // nil: always ask iam
	iam    iamv1connect.IamServiceClient
	ttl    time.Duration
	logger logger.Logger
}

// NewVersionLookup: ttl must be at least the access token lifetime.
func NewVersionLookup(rdb *redis.Client, iam iamv1connect.IamServiceClient, ttl time.Duration, l logger.Logger) MembershipVersions {
	return &versionLookup{rdb: rdb, iam: iam, ttl: ttl, logger: l}
}

func (v *versionLookup) Current(ctx context.Context, tenantID, userID string) (int64, error) {
	key := token.MembershipVersionKey(tenantID, userID)

	if v.rdb != nil {
		s, err := v.rdb.Get(ctx, key).Result()
		switch {
		case err == nil:
			if n, perr := strconv.ParseInt(s, 10, 64); perr == nil {
				return n, nil
			}
		case !errors.Is(err, redis.Nil):
			v.logger.Warnf("redis get %s: %v (falling back to iam)", key, err)
		}
	}

	res, err := v.iam.GetMembershipVersion(ctx, &iamv1.GetMembershipVersionRequest{TenantId: tenantID, UserId: userID})
	var version int64
	switch {
	case errors.Is(err, apperr.ErrNotFound):
		version = token.RemovedMembershipVersion
	case err != nil:
		return 0, err
	default:
		version = res.GetVersion()
	}

	if v.rdb != nil {
		if err := v.rdb.SetNX(ctx, key, strconv.FormatInt(version, 10), v.ttl).Err(); err != nil {
			v.logger.Warnf("redis setnx %s: %v", key, err)
		}
	}
	return version, nil
}
