package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestRateLimitPerIP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RateLimit(60, 3)) // 1/s, burst 3
	r.GET("/", func(c *gin.Context) { c.Status(204) })

	hit := func(ip string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = ip + ":1234"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	for i := 0; i < 3; i++ {
		if w := hit("10.0.0.1"); w.Code != 204 {
			t.Fatalf("burst request %d: %d", i, w.Code)
		}
	}
	w := hit("10.0.0.1")
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("over limit: %d %v", w.Code, w.Header())
	}
	if w := hit("10.0.0.2"); w.Code != 204 {
		t.Fatalf("other ip limited: %d", w.Code)
	}
}

func TestRateLimitForgetsIdleClients(t *testing.T) {
	now := time.Unix(0, 0)
	l := &ipLimiter{limit: 1, burst: 1, idle: time.Minute, limiters: map[string]*ipEntry{}, now: func() time.Time { return now }}
	for i := 0; i < 100; i++ {
		l.allow(string(rune('a' + i%26)))
	}
	now = now.Add(5 * time.Minute)
	l.allow("z")
	if len(l.limiters) != 1 {
		t.Fatalf("idle limiters kept: %d", len(l.limiters))
	}
}
