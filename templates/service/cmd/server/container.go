package main

import (
	"core/authz"
	"core/di"
	"core/logger"
	"core/uow"
	"template/internal/repository"
	"template/internal/service"
	grpctransport "template/internal/transport/rpc"

	"gorm.io/gorm"
)

// buildContainer wires repositories, services and handlers.
// Infrastructure (db, logger...) comes from sctx and is bridged in via di.Instance.
func buildContainer(db *gorm.DB, log logger.Logger) (*di.Container, error) {
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

		// note module
		func() error { return di.Instance[authz.Authorizer](c, authz.NewRBAC(service.NotePolicy)) },
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
