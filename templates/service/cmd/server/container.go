package main

import (
	"context"
	"errors"

	authzv1 "api/gen/go/authz/v1"
	"api/gen/go/authz/v1/authzv1connect"
	"core/apperr"
	"core/authz"
	"core/components/authzc"
	"core/components/rpcc"
	"core/di"
	"core/logger"
	"core/outbox"
	"core/uow"
	"template/internal/repository"
	"template/internal/service"
	grpctransport "template/internal/transport/rpc"

	"gorm.io/gorm"
)

// authzSetup: how the service relates to the central authz service.
type authzSetup struct {
	// nil without AUTHZ_SERVICE_URL: no relationship sync
	client authzv1connect.AuthzServiceClient
	// authzc.EnforceRBAC or authzc.EnforceReBAC
	enforce string
}

// newAuthzSetup builds the authz client when AUTHZ_SERVICE_URL is set.
func newAuthzSetup(cc rpcc.ClientComponent, enforce string) (authzSetup, error) {
	setup := authzSetup{enforce: enforce}
	if cc.Enabled() {
		setup.client = rpcc.New(cc, authzv1connect.NewAuthzServiceClient)
	}
	if enforce == authzc.EnforceReBAC && setup.client == nil {
		return setup, errors.New("authz-enforce=rebac needs authz-service-url")
	}
	return setup, nil
}

// authorizer: the central authz service (ReBAC, model deploy/authz/model.fga)
// when enforced, otherwise this service's static RBAC policy. Both implement
// authz.Authorizer, so the service code is identical.
func (a authzSetup) authorizer() authz.Authorizer {
	if a.enforce != authzc.EnforceReBAC {
		return authz.NewRBAC(service.NotePolicy)
	}
	return authz.NewReBAC(authz.CheckFunc(func(ctx context.Context, object, relation, subject string, c authz.Consistency) (bool, error) {
		res, err := a.client.Check(ctx, &authzv1.CheckRequest{
			Object: object, Relation: relation, Subject: subject, Consistency: toProtoConsistency(c),
		})
		return res.GetAllowed(), err
	}), authz.WithBatchChecker(a.batchCheck))
}

// batchCheck: authz.CheckMany in one BatchCheck call.
func (a authzSetup) batchCheck(ctx context.Context, checks []authz.RelationCheck, c authz.Consistency) ([]authz.RelationResult, error) {
	req := &authzv1.BatchCheckRequest{Checks: make([]*authzv1.BatchCheckItem, len(checks)), Consistency: toProtoConsistency(c)}
	for i, ch := range checks {
		req.Checks[i] = &authzv1.BatchCheckItem{Object: ch.Object, Relation: ch.Relation, Subject: ch.Subject}
	}
	res, err := a.client.BatchCheck(ctx, req)
	if err != nil {
		return nil, err
	}
	out := make([]authz.RelationResult, len(res.GetResults()))
	for i, r := range res.GetResults() {
		out[i] = authz.RelationResult{Allowed: r.GetAllowed()}
		if e := r.GetError(); e != nil {
			out[i].Err = apperr.FromCode(e.GetCode(), e.GetMessage())
		}
	}
	return out, nil
}

func toProtoConsistency(c authz.Consistency) authzv1.Consistency {
	if c == authz.Fresh {
		return authzv1.Consistency_CONSISTENCY_HIGHER
	}
	return authzv1.Consistency_CONSISTENCY_MINIMIZE_LATENCY
}

// relationships: synced through the outbox whenever the authz service is configured.
func (a authzSetup) relationships(ob *outbox.Outbox) authz.Relationships {
	if a.client == nil {
		return authz.NoRelationships{}
	}
	return authz.NewOutboxRelationships(ob)
}

// writeTuples delivers outbox tuple changes to the authz service.
func (a authzSetup) writeTuples(ctx context.Context, c authz.TupleChanges) error {
	_, err := a.client.WriteTuples(ctx, &authzv1.WriteTuplesRequest{
		Writes: toProto(c.Writes), Deletes: toProto(c.Deletes), DeleteObjects: c.DeleteObjects,
	})
	return err
}

func toProto(tuples []authz.Tuple) []*authzv1.Tuple {
	out := make([]*authzv1.Tuple, len(tuples))
	for i, t := range tuples {
		out[i] = &authzv1.Tuple{Object: t.Object, Relation: t.Relation, Subject: t.Subject, TenantId: t.TenantID}
	}
	return out
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
		func() error { return di.Provide[*outbox.Outbox](c, outbox.New, di.Singleton) },
		func() error { return di.Instance[logger.Logger](c, log) },

		// authorization
		func() error { return di.Instance[authz.Authorizer](c, a.authorizer()) },
		func() error { return di.Provide[authz.Relationships](c, a.relationships, di.Singleton) },

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
