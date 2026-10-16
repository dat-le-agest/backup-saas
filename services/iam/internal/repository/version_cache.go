package repository

import (
	"context"
	"strconv"
	"time"

	"core/token"

	"github.com/redis/go-redis/v9"
)

// versionCache publishes membership versions to Redis for the gateway
// (write-through after each committed change). The gateway fills missing keys
// with SET NX, so a stale fill can never overwrite a version written here.
// A nil client disables it: the gateway then asks iam directly.
type versionCache struct {
	rdb *redis.Client
	ttl time.Duration
}

// NewVersionCache: ttl should be at least the access token lifetime.
func NewVersionCache(rdb *redis.Client, ttl time.Duration) *versionCache {
	return &versionCache{rdb: rdb, ttl: ttl}
}

func (c *versionCache) SetMembershipVersion(ctx context.Context, tenantID, userID string, version int64) error {
	if c.rdb == nil {
		return nil
	}
	return c.rdb.Set(ctx, token.MembershipVersionKey(tenantID, userID), strconv.FormatInt(version, 10), c.ttl).Err()
}
