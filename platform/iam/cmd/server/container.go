package main

import (
	"context"
	"time"

	authzv1 "api/gen/go/platform/authz/v1"
	"api/gen/go/platform/authz/v1/authzv1connect"
	"core/outbox"

	"core/authz"
	"core/di"
	"core/logger"
	"core/uow"
	"iam/internal/repository"
	"iam/internal/service"
	grpctransport "iam/internal/transport/rpc"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type deps struct {
	db     *gorm.DB
	redis  *redis.Client // nil: gateway falls back to GetMembershipVersion
	signer service.AccessTokenSigner
	hasher service.PasswordHasher
	tokens service.TokenConfig
	// cache entries must outlive the access tokens they protect
	versionTTL time.Duration
	logger     logger.Logger
	// nil without AUTHZ_SERVICE_URL: memberships are not synced to the authz service
	authz authzv1connect.AuthzServiceClient
}

// relationships: memberships are synced through the outbox when the authz service is configured.
func (d deps) relationships(ob *outbox.Outbox) authz.Relationships {
	if d.authz == nil {
		return authz.NoRelationships{}
	}
	return authz.NewOutboxRelationships(ob)
}

// writeTuples delivers outbox tuple changes to the authz service.
func (d deps) writeTuples(ctx context.Context, c authz.TupleChanges) error {
	toProto := func(tuples []authz.Tuple) []*authzv1.Tuple {
		out := make([]*authzv1.Tuple, len(tuples))
		for i, t := range tuples {
			out[i] = &authzv1.Tuple{Object: t.Object, Relation: t.Relation, Subject: t.Subject, TenantId: t.TenantID}
		}
		return out
	}
	_, err := d.authz.WriteTuples(ctx, &authzv1.WriteTuplesRequest{
		Writes: toProto(c.Writes), Deletes: toProto(c.Deletes), DeleteObjects: c.DeleteObjects,
	})
	return err
}

// buildContainer wires stores, services and handlers.
// Infrastructure comes from sctx and is bridged in via di.Instance.
func buildContainer(d deps) (*di.Container, error) {
	c := di.New()

	steps := []func() error{
		// infrastructure
		func() error { return di.Instance[*gorm.DB](c, d.db) },
		func() error { return di.Instance[logger.Logger](c, d.logger) },
		func() error { return di.Instance[service.AccessTokenSigner](c, d.signer) },
		func() error { return di.Instance[service.TokenConfig](c, d.tokens) },
		func() error { return di.Instance[service.PasswordHasher](c, d.hasher) },
		func() error {
			return di.Instance[service.VersionCache](c, repository.NewVersionCache(d.redis, d.versionTTL))
		},

		// unit of work: the store uses *uow.Gorm, services the interface
		func() error {
			return di.Provide[*uow.Gorm](c, func(db *gorm.DB) *uow.Gorm { return uow.New(db) }, di.Singleton)
		},
		func() error {
			return di.Provide[uow.UnitOfWork](c, func(u *uow.Gorm) uow.UnitOfWork { return u }, di.Singleton)
		},

		func() error { return di.Provide[*outbox.Outbox](c, outbox.New, di.Singleton) },
		func() error { return di.Provide[authz.Relationships](c, d.relationships, di.Singleton) },

		// iam
		func() error { return di.Provide[service.Store](c, repository.NewStore, di.Singleton) },
		func() error { return di.Instance[authz.Authorizer](c, authz.NewRBAC(service.MemberPolicy)) },
		func() error { return di.Provide[*service.TokenService](c, service.NewTokenService, di.Singleton) },
		func() error { return di.Provide[*service.AuthService](c, service.NewAuthService, di.Singleton) },
		func() error { return di.Provide[*service.TenantService](c, service.NewTenantService, di.Singleton) },
		func() error { return di.Provide[*service.MemberService](c, service.NewMemberService, di.Singleton) },
		func() error {
			return di.Provide[*grpctransport.IamHandler](c, grpctransport.NewIamHandler, di.Singleton)
		},
	}

	for _, step := range steps {
		if err := step(); err != nil {
			return nil, err
		}
	}

	return c, c.Build()
}
