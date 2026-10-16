// Package redisc is the sctx component for Redis.
//
//	sctx.WithComponent(redisc.NewRedis("redis", ""))
//	// flag --redis-url / env REDIS_URL, e.g. redis://localhost:6379/0
//
// Redis is optional: with an empty URL the component is disabled and Client()
// returns nil, so callers must have a fallback (e.g. ask the source of truth).
package redisc

import (
	"context"
	"core/logger"
	"core/sctx"
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
)

type RedisComponent interface {
	// Client returns nil when Redis is not configured.
	Client() *redis.Client
}

type redisComponent struct {
	id     string
	prefix string
	url    string
	logger logger.Logger
	client *redis.Client
}

func NewRedis(id, prefix string) *redisComponent {
	return &redisComponent{id: id, prefix: prefix}
}

func (r *redisComponent) ID() string { return r.id }

func (r *redisComponent) InitFlags() {
	prefix := r.prefix
	if prefix != "" {
		prefix += "-"
	}
	flag.StringVar(
		&r.url,
		prefix+"redis-url",
		"",
		"redis URL, e.g. redis://localhost:6379/0 - Default empty (disabled)",
	)
}

func (r *redisComponent) Activate(sc sctx.ServiceContext) error {
	r.logger = sc.Logger(r.id)
	if r.url == "" {
		r.logger.Warn("redis disabled (no url)")
		return nil
	}

	opts, err := redis.ParseURL(r.url)
	if err != nil {
		return fmt.Errorf("invalid redis url: %w", err)
	}
	opts.DialTimeout = 3 * time.Second
	opts.ReadTimeout = time.Second
	opts.WriteTimeout = time.Second

	r.client = redis.NewClient(opts)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := r.client.Ping(ctx).Err(); err != nil {
		_ = r.client.Close()
		r.client = nil
		return fmt.Errorf("redis ping %s: %w", opts.Addr, err)
	}

	if err := errors.Join(redisotel.InstrumentTracing(r.client), redisotel.InstrumentMetrics(r.client)); err != nil {
		return fmt.Errorf("redis otel: %w", err)
	}

	r.logger.Infof("redis connected (addr=%s, db=%d)", opts.Addr, opts.DB)
	return nil
}

func (r *redisComponent) Stop() error {
	if r.client == nil {
		return nil
	}
	return r.client.Close()
}

func (r *redisComponent) Client() *redis.Client {
	return r.client
}
