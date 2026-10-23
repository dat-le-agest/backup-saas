package httpx

import (
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"

	"core/apperr"
	"core/response"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// RateLimit allows perMinute requests per client IP with bursts up to burst.
//
// Limits are per gateway instance (in memory). The client IP comes from gin's
// ClientIP, so set GIN_TRUSTED_PROXIES behind a load balancer, otherwise every
// request appears to come from the proxy.
func RateLimit(perMinute, burst int) gin.HandlerFunc {
	l := &ipLimiter{
		limit:    rate.Limit(float64(perMinute) / 60),
		burst:    burst,
		idle:     10 * time.Minute,
		limiters: map[string]*ipEntry{},
		now:      time.Now,
	}
	retryAfter := strconv.Itoa(int(math.Ceil(60 / float64(perMinute))))

	return func(c *gin.Context) {
		if !l.allow(c.ClientIP()) {
			c.Header("Retry-After", retryAfter)
			response.Fail(c, fmt.Errorf("%w: slow down", apperr.ErrTooManyRequests))
			return
		}
		c.Next()
	}
}

type ipEntry struct {
	limiter *rate.Limiter
	seen    time.Time
}

type ipLimiter struct {
	mu        sync.Mutex
	limit     rate.Limit
	burst     int
	idle      time.Duration
	limiters  map[string]*ipEntry
	lastSweep time.Time
	now       func() time.Time
}

func (l *ipLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	// forget idle clients now and then so the map doesn't grow forever
	if now.Sub(l.lastSweep) > time.Minute {
		for k, e := range l.limiters {
			if now.Sub(e.seen) > l.idle {
				delete(l.limiters, k)
			}
		}
		l.lastSweep = now
	}

	e, ok := l.limiters[ip]
	if !ok {
		e = &ipEntry{limiter: rate.NewLimiter(l.limit, l.burst)}
		l.limiters[ip] = e
	}
	e.seen = now
	return e.limiter.AllowN(now, 1)
}
