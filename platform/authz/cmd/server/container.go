package main

import (
	"authz/internal/repository"
	"authz/internal/service"
	grpctransport "authz/internal/transport/rpc"
	"core/di"
	"core/logger"
	"core/uow"

	"gorm.io/gorm"
)

// buildContainer wires repositories, services and handlers.
// Infrastructure (db, logger...) comes from sctx and is bridged in via di.Instance.
func buildContainer(db *gorm.DB, log logger.Logger, apps service.Apps) (*di.Container, error) {
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
		func() error { return di.Instance[service.Apps](c, apps) },

		// authz
		func() error { return di.Provide[*repository.Store](c, repository.NewStore, di.Singleton) },
		func() error { return di.Provide[*service.AuthzService](c, service.NewAuthzService, di.Singleton) },
		func() error {
			return di.Provide[*grpctransport.AuthzHandler](c, grpctransport.NewAuthzHandler, di.Singleton)
		},
	}

	for _, step := range steps {
		if err := step(); err != nil {
			return nil, err
		}
	}

	return c, c.Build()
}
