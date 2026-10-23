package main

import (
	"core/authz"
	"core/authz/rebac"
	"core/components/authzc"
	"core/di"
	"core/logger"
	"core/uow"
	"note/internal/repository"
	"note/internal/service"
	grpctransport "note/internal/transport/rpc"

	"gorm.io/gorm"
)

// authzSetup: how the service authorizes. The embedded ReBAC engine
// (core/authz/rebac: model in authz/*.fga, tuples in this service's database)
// always records relationships; AUTHZ_ENFORCE picks who decides: this
// service's RBAC policy (default) or the engine. Switching needs no backfill.
type authzSetup struct {
	// authzc.EnforceRBAC or authzc.EnforceReBAC
	enforce string
	engine  *rebac.Engine
}

// authorizer: the engine when enforced, otherwise the static RBAC policy. Both
// implement authz.Authorizer, so the service code is identical.
func (a authzSetup) authorizer() authz.Authorizer {
	if a.enforce == authzc.EnforceReBAC {
		return a.engine.Authorizer()
	}
	return authz.NewRBAC(service.NotePolicy)
}

// relationships: written by the engine in the caller's transaction.
func (a authzSetup) relationships() authz.Relationships {
	if a.engine == nil {
		return authz.NoRelationships{}
	}
	return a.engine.Relationships()
}

// buildContainer wires repositories, services and handlers.
// Infrastructure (db, logger, authz setup...) comes from main and is bridged in via di.Instance.
func buildContainer(db *gorm.DB, log logger.Logger, a authzSetup) (*di.Container, error) {
	c := di.New()

	steps := []func() error{
		// infrastructure
		func() error { return di.Instance[*gorm.DB](c, db) },
		// one unit of work for the service: repositories use *uow.Gorm, services the interface
		func() error {
			return di.Provide[*uow.Gorm](c, func(db *gorm.DB) *uow.Gorm { return uow.New(db) }, di.Singleton)
		},
		func() error {
			return di.Provide[uow.UnitOfWork](c, func(u *uow.Gorm) uow.UnitOfWork { return u }, di.Singleton)
		},
		func() error { return di.Instance[logger.Logger](c, log) },

		// authorization
		func() error { return di.Instance[authz.Authorizer](c, a.authorizer()) },
		func() error { return di.Instance[authz.Relationships](c, a.relationships()) },

		// note module
		func() error { return di.Provide[service.NoteRepository](c, repository.NewNoteRepo, di.Singleton) },
		func() error { return di.Provide[*service.NoteService](c, service.NewNoteService, di.Singleton) },
		func() error {
			return di.Provide[*grpctransport.NoteHandler](c, grpctransport.NewNoteHandler, di.Singleton)
		},
	}

	for _, step := range steps {
		if err := step(); err != nil {
			return nil, err
		}
	}

	return c, c.Build()
}
