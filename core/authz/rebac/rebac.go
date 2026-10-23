package rebac

import (
	"context"
	"io/fs"

	"core/authz"
	"core/authz/rebac/migrations"
	"core/authz/rebac/store"
	"core/logger"
	"core/migrate"
	"core/reqctx"
	"core/uow"

	"gorm.io/gorm"
)

// Schema holds the tables of the engine in the service's database.
const Schema = "authz"

// Config of an embedded engine.
type Config struct {
	// Modules: the app's OpenFGA module files (*.fga at the root, usually an
	// embed.FS), composed with the base module.
	Modules fs.FS
	// DSN of the service's database, to migrate the authz schema; "" skips the
	// migrations (already applied).
	DSN    string
	Logger logger.Logger
}

// New migrates the authz schema, loads the model (a new version is activated
// only when the files changed) and returns the engine.
func New(ctx context.Context, db *gorm.DB, cfg Config) (*Engine, error) {
	if cfg.DSN != "" {
		if err := migrate.UpInSchema(ctx, cfg.DSN, migrations.FS, Schema, cfg.Logger); err != nil {
			return nil, err
		}
	}
	st, err := store.NewStore(uow.New(db)).For(Schema)
	if err != nil {
		return nil, err
	}
	e := NewEngine(st, uow.New(db))
	files, err := Modules(cfg.Modules)
	if err != nil {
		return nil, err
	}
	id, changed, err := e.LoadModules(ctx, files)
	if err != nil {
		return nil, err
	}
	if changed {
		cfg.Logger.Infof("rebac: authorization model %s activated (%d module files)", id, len(files))
	} else {
		cfg.Logger.Infof("rebac: authorization model %s unchanged", id)
	}
	return e, nil
}

// Authorizer decides with the engine, adding the caller's tenant role from
// their access token as a contextual tuple. It also answers authz.CheckMany in
// one batch.
func (e *Engine) Authorizer(opts ...authz.ReBACOption) authz.Authorizer {
	check := authz.CheckFunc(func(ctx context.Context, object, relation, subject string, c authz.Consistency) (bool, error) {
		return e.Check(ctx, object, relation, subject, roleTuples(ctx), consistency(c))
	})
	batch := func(ctx context.Context, checks []authz.RelationCheck, c authz.Consistency) ([]authz.RelationResult, error) {
		in := make([]CheckInput, len(checks))
		for i, ch := range checks {
			in[i] = CheckInput{Object: ch.Object, Relation: ch.Relation, Subject: ch.Subject}
		}
		res, err := e.BatchCheck(ctx, in, roleTuples(ctx), consistency(c))
		if err != nil {
			return nil, err
		}
		out := make([]authz.RelationResult, len(res))
		for i, r := range res {
			out[i] = authz.RelationResult{Allowed: r.Allowed, Err: r.Err}
		}
		return out, nil
	}
	return authz.NewReBAC(check, append([]authz.ReBACOption{authz.WithBatchChecker(batch)}, opts...)...)
}

// Relationships records relationship changes in the engine's tables, in the
// caller's unit of work: they commit with the data they describe.
func (e *Engine) Relationships() authz.Relationships { return relationships{e} }

type relationships struct{ e *Engine }

func (r relationships) Write(ctx context.Context, tuples ...authz.Tuple) error {
	return r.e.WriteTuples(ctx, inputs(tuples), nil, nil)
}

func (r relationships) Delete(ctx context.Context, tuples ...authz.Tuple) error {
	return r.e.WriteTuples(ctx, nil, inputs(tuples), nil)
}

func (r relationships) Change(ctx context.Context, writes, deletes []authz.Tuple) error {
	return r.e.WriteTuples(ctx, inputs(writes), inputs(deletes), nil)
}

func (r relationships) DeleteObjects(ctx context.Context, objects ...string) error {
	return r.e.WriteTuples(ctx, nil, nil, objects)
}

func inputs(tuples []authz.Tuple) []TupleInput {
	out := make([]TupleInput, len(tuples))
	for i, t := range tuples {
		out[i] = TupleInput{Object: t.Object, Relation: t.Relation, Subject: t.Subject, TenantID: t.TenantID}
	}
	return out
}

// roleTuples: the caller's tenant role, from their verified access token.
func roleTuples(ctx context.Context) []TupleInput {
	id, ok := reqctx.IdentityFrom(ctx)
	if !ok || id.UserID == "" || id.TenantID == "" || id.Role == "" {
		return nil
	}
	return []TupleInput{{Object: "tenant:" + id.TenantID, Relation: id.Role, Subject: "user:" + id.UserID, TenantID: id.TenantID}}
}

func consistency(c authz.Consistency) Consistency {
	if c == authz.Fresh {
		return Higher
	}
	return MinimizeLatency
}
