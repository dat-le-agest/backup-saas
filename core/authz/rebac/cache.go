package rebac

import (
	"context"

	lru "github.com/hashicorp/golang-lru/v2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// checkCache is the replica-local cache of Check sub-results. Keys include the
// model id and the tuple store revision, so a tuple change makes old entries
// unreachable; the LRU evicts them.
type checkCache struct {
	lru     *lru.Cache[string, bool]
	lookups metric.Int64Counter
}

func newCheckCache(size int) *checkCache {
	c, _ := lru.New[string, bool](size)
	lookups, _ := otel.Meter("authz").Int64Counter("authz.check_cache.lookups",
		metric.WithDescription("Check cache lookups by result (hit, miss)"))
	return &checkCache{lru: c, lookups: lookups}
}

var (
	hit  = metric.WithAttributes(attribute.String("result", "hit"))
	miss = metric.WithAttributes(attribute.String("result", "miss"))
)

func (c *checkCache) Get(key string) (bool, bool) {
	v, ok := c.lru.Get(key)
	if ok {
		c.lookups.Add(context.Background(), 1, hit)
	} else {
		c.lookups.Add(context.Background(), 1, miss)
	}
	return v, ok
}

func (c *checkCache) Add(key string, allowed bool) { c.lru.Add(key, allowed) }
