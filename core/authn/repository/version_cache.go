package repository

import (
	"context"
	"strconv"
	"time"

	"core/token"

	"github.com/redis/go-redis/v9"
)

// versionCache publishes membership versions of an app to Redis (write-through
// after each committed change), for its authenticator. The authenticator fills
// missing keys with SET NX, so a stale fill can never overwrite a version
// written here. A nil client disables it: the authenticator reads the database.
type versionCache struct {
	rdb *redis.Client
	app string
	ttl time.Duration
}

// NewVersionCache: ttl should be at least the access token lifetime.
func NewVersionCache(rdb *redis.Client, app string, ttl time.Duration) *versionCache {
	return &versionCache{rdb: rdb, app: app, ttl: ttl}
}

func (c *versionCache) SetMembershipVersion(ctx context.Context, tenantID, userID string, version int64) error {
	if c.rdb == nil {
		return nil
	}
	return c.rdb.Set(ctx, token.MembershipVersionKey(c.app, tenantID, userID), strconv.FormatInt(version, 10), c.ttl).Err()
}
