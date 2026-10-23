// Package rebac is relationship-based authorization (Zanzibar / OpenFGA style)
// embedded in a service: the authorization model is compiled from OpenFGA
// module files (the base module + the app's), tuples live in the service's
// own database (schema "authz", migrations.FS applied with
// migrate.UpInSchema), and checks run in-process.
//
// Tenant roles are not stored: each check adds the caller's role from their
// verified access token as a contextual tuple (tenant:<t>#<role>@user:<u>), so
// memberships stay owned by the app's authentication.
//
//	eng, err := rebac.New(db, rebac.Config{Modules: files})
//	az := eng.Authorizer()          // core/authz.Authorizer (+ CheckMany)
//	rel := eng.Relationships()      // core/authz.Relationships, in the caller's transaction
package rebac

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"core/apperr"
	"core/authz/rebac/engine"
	"core/authz/rebac/store"
	"core/reqctx"
	"core/uow"

	"github.com/oklog/ulid/v2"
	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// Consistency of a read (Check, ListObjects).
type Consistency int

const (
	// MinimizeLatency: cached answers; may miss changes made on other replicas
	// during the last RevisionTTL.
	MinimizeLatency Consistency = iota
	// Higher: answers reflect every committed change.
	Higher
)

const (
	// how long a replica reuses the tuple store revision it last read; bounds how
	// stale MinimizeLatency answers can be after a change on another replica
	RevisionTTL = time.Second
	// cached sub-results (object#relation@subject) per replica
	CacheSize = 100_000
)

const (
	maxBatchChecks = 50
	// checks of one batch evaluated at the same time
	batchConcurrency  = 8
	maxTuplesPerWrite = 100
	defaultPageSize   = 100
	maxPageSize       = 1000
	// how long a replica may keep using the previous active model after a switch
	activeModelTTL = 5 * time.Second
)

// TupleInput is a tuple as received from callers ("note:42", "viewer", "team:eng#member").
type TupleInput struct {
	Object, Relation, Subject, TenantID string
}

// Engine evaluates and stores relationships of one service.
type Engine struct {
	store *store.Store
	uow   uow.UnitOfWork
	cache *checkCache

	mu       sync.Mutex
	models   map[string]*engine.Model // by id; models are immutable
	activeID string
	activeAt time.Time
	revision int64
	revAt    time.Time
}

// NewEngine: st must be bound to the schema holding the tables (store.For).
func NewEngine(st *store.Store, u uow.UnitOfWork) *Engine {
	return &Engine{store: st, uow: u, cache: newCheckCache(CacheSize), models: map[string]*engine.Model{}}
}

// ---- checks ----

// Check: does subject have relation on object? contextual tuples count as
// stored for this check only.
func (e *Engine) Check(ctx context.Context, object, relation, subject string, contextual []TupleInput, consistency Consistency) (bool, error) {
	obj, err := engine.ParseObject("object", object)
	if err != nil {
		return false, err
	}
	if err := engine.ValidateRelation("relation", relation); err != nil {
		return false, err
	}
	sub, err := engine.ParseSubject("subject", subject)
	if err != nil {
		return false, err
	}
	checker, err := e.checker(ctx, contextual, consistency)
	if err != nil {
		return false, err
	}
	return checker.Check(ctx, obj, relation, sub)
}

// CheckInput is one check of a batch.
type CheckInput struct {
	Object, Relation, Subject string
}

// CheckResult: Err is set when the check could not be evaluated (Allowed is then false).
type CheckResult struct {
	Allowed bool
	Err     error
}

// BatchCheck evaluates checks concurrently against one model and one tuple
// revision, so answers are consistent with each other. Results are in input
// order; identical checks are evaluated once. A failing check doesn't fail the
// batch; the request fails only as a whole (too many checks, no model, ctx done).
func (e *Engine) BatchCheck(ctx context.Context, checks []CheckInput, contextual []TupleInput, consistency Consistency) ([]CheckResult, error) {
	if len(checks) == 0 {
		return nil, apperr.Invalid("checks", "is required")
	}
	if len(checks) > maxBatchChecks {
		return nil, apperr.Invalid("checks", fmt.Sprintf("at most %d checks per request", maxBatchChecks))
	}
	checker, err := e.checker(ctx, contextual, consistency)
	if err != nil {
		return nil, err
	}

	// first index of each distinct check
	first := make(map[CheckInput]int, len(checks))
	var unique []int
	for i, c := range checks {
		if _, ok := first[c]; !ok {
			first[c] = i
			unique = append(unique, i)
		}
	}

	results := make([]CheckResult, len(checks))
	sem := make(chan struct{}, batchConcurrency)
	var wg sync.WaitGroup
	for _, i := range unique {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer func() { <-sem; wg.Done() }()
			allowed, err := checkOne(ctx, checker, fmt.Sprintf("checks[%d]", i), checks[i])
			results[i] = CheckResult{Allowed: allowed && err == nil, Err: err}
		}(i)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for i, c := range checks {
		results[i] = results[first[c]]
	}
	return results, nil
}

func checkOne(ctx context.Context, checker *engine.Checker, field string, c CheckInput) (bool, error) {
	obj, err := engine.ParseObject(field+".object", c.Object)
	if err != nil {
		return false, err
	}
	if err := engine.ValidateRelation(field+".relation", c.Relation); err != nil {
		return false, err
	}
	sub, err := engine.ParseSubject(field+".subject", c.Subject)
	if err != nil {
		return false, err
	}
	return checker.Check(ctx, obj, c.Relation, sub)
}

// ListObjects lists objects of a type on which subject has relation.
func (e *Engine) ListObjects(ctx context.Context, objType, relation, subject string, contextual []TupleInput, limit int, consistency Consistency) ([]string, error) {
	if err := engine.ValidateRelation("type", objType); err != nil {
		return nil, err
	}
	if err := engine.ValidateRelation("relation", relation); err != nil {
		return nil, err
	}
	sub, err := engine.ParseSubject("subject", subject)
	if err != nil {
		return nil, err
	}
	checker, err := e.checker(ctx, contextual, consistency)
	if err != nil {
		return nil, err
	}
	ids, err := checker.ListObjects(ctx, objType, relation, sub, pageSize(limit))
	if err != nil {
		return nil, err
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = objType + ":" + id
	}
	return out, nil
}

// ---- tuples ----

// WriteTuples validates every written tuple against the active model, then
// removes deleted objects, deletes and writes. It joins the unit of work of
// ctx when there is one (relationships commit with the data they describe).
// Idempotent.
func (e *Engine) WriteTuples(ctx context.Context, writes, deletes []TupleInput, deleteObjects []string) error {
	n := len(writes) + len(deletes) + len(deleteObjects)
	if n == 0 {
		return nil
	}
	if n > maxTuplesPerWrite {
		return apperr.Invalid("writes", fmt.Sprintf("at most %d tuples and objects per request", maxTuplesPerWrite))
	}
	objects := make([]engine.Object, 0, len(deleteObjects))
	for i, o := range deleteObjects {
		obj, err := engine.ParseObject(fmt.Sprintf("delete_objects[%d]", i), o)
		if err != nil {
			return err
		}
		objects = append(objects, obj)
	}
	m, err := e.model(ctx, "")
	if err != nil {
		return err
	}
	ws, err := parseTuples(m, "writes", writes, true)
	if err != nil {
		return err
	}
	// deletes are only parsed: removing a tuple the current model no longer
	// accepts (after a model change) must stay possible
	ds, err := parseTuples(m, "deletes", deletes, false)
	if err != nil {
		return err
	}

	err = e.uow.Do(ctx, func(ctx context.Context) error {
		if err := e.store.SetActor(ctx, actor(ctx), reqctx.RequestIDFrom(ctx)); err != nil {
			return err
		}
		if err := e.store.DeleteObjects(ctx, objects); err != nil {
			return err
		}
		if err := e.store.DeleteTuples(ctx, ds); err != nil {
			return err
		}
		return e.store.WriteTuples(ctx, ws)
	})
	if err != nil {
		return err
	}
	// this replica sees its own writes right away (after commit); others within RevisionTTL
	uow.AfterCommit(ctx, func(ctx context.Context) { _, _ = e.currentRevision(ctx, Higher) })
	return nil
}

// checker evaluates with the active model, the cache of the current revision
// and the contextual tuples.
func (e *Engine) checker(ctx context.Context, contextual []TupleInput, consistency Consistency) (*engine.Checker, error) {
	m, err := e.model(ctx, "")
	if err != nil {
		return nil, err
	}
	extra, err := parseTuples(m, "contextual", contextual, false)
	if err != nil {
		return nil, err
	}
	rev, err := e.currentRevision(ctx, consistency)
	if err != nil {
		return nil, err
	}
	// answers depend on the contextual tuples: they are part of the cache key
	prefix := m.ID + "@" + strconv.FormatInt(rev, 10)
	for _, t := range extra {
		prefix += "+" + t.Object.String() + "#" + t.Relation + "@" + t.Subject.String()
	}
	return engine.NewChecker(m, engine.WithContextual(e.store, extra)).WithCache(e.cache, prefix+"|"), nil
}

// currentRevision returns the tuple store revision: read again when older than
// RevisionTTL, or always for Higher consistency.
func (e *Engine) currentRevision(ctx context.Context, consistency Consistency) (int64, error) {
	e.mu.Lock()
	if consistency != Higher && !e.revAt.IsZero() && time.Since(e.revAt) < RevisionTTL {
		rev := e.revision
		e.mu.Unlock()
		return rev, nil
	}
	e.mu.Unlock()

	rev, err := e.store.Revision(ctx)
	if err != nil {
		return 0, err
	}
	e.mu.Lock()
	if rev >= e.revision { // never go back (a slower concurrent read)
		e.revision, e.revAt = rev, time.Now()
	}
	rev = e.revision
	e.mu.Unlock()
	return rev, nil
}

// ReadChanges returns tuple changes after afterSeq (e.g. to keep an index in sync).
func (e *Engine) ReadChanges(ctx context.Context, objectType string, afterSeq int64, limit int) ([]store.Change, int64, error) {
	if objectType != "" {
		if err := engine.ValidateRelation("object_type", objectType); err != nil {
			return nil, 0, err
		}
	}
	if afterSeq < 0 {
		return nil, 0, apperr.Invalid("after_seq", "must be >= 0")
	}
	changes, err := e.store.ReadChanges(ctx, objectType, afterSeq, pageSize(limit))
	if err != nil {
		return nil, 0, err
	}
	last := afterSeq
	if len(changes) > 0 {
		last = changes[len(changes)-1].Seq
	}
	return changes, last, nil
}

// ---- models ----

// LoadModules compiles the model from OpenFGA module files and, if it differs
// from the active model, stores it as a new version and activates it. Returns
// the active model id and whether a new version was activated. Call at startup.
func (e *Engine) LoadModules(ctx context.Context, files []engine.ModuleFile) (string, bool, error) {
	compiled, source, err := engine.ParseModules(files)
	if err != nil {
		return "", false, err
	}
	if active, err := e.store.GetModel(ctx, ""); err == nil && active.DSL == source {
		return active.ID, false, nil
	} else if err != nil && !errors.Is(err, apperr.ErrInvalid) { // "no active model" is fine
		return "", false, err
	}
	id, err := e.writeModel(ctx, compiled, source, true)
	return id, err == nil, err
}

// WriteModel stores a model written as one schema 1.1 DSL document, optionally
// activating it (tests and single-file models).
func (e *Engine) WriteModel(ctx context.Context, dsl string, activate bool) (string, error) {
	compiled, err := engine.ParseDSL(dsl)
	if err != nil {
		return "", err
	}
	return e.writeModel(ctx, compiled, dsl, activate)
}

func (e *Engine) writeModel(ctx context.Context, compiled *openfgav1.AuthorizationModel, source string, activate bool) (string, error) {
	id := ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader).String()
	compiled.Id = id
	raw, err := protojson.Marshal(compiled)
	if err != nil {
		return "", err
	}

	err = e.uow.Do(ctx, func(ctx context.Context) error {
		if err := e.store.CreateModel(ctx, &store.StoredModel{
			ID: id, SchemaVersion: compiled.GetSchemaVersion(), DSL: source, Model: raw, CreatedBy: actor(ctx),
		}); err != nil {
			return err
		}
		if activate {
			return e.store.Activate(ctx, id)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if activate {
		e.forgetActive()
	}
	return id, nil
}

// ActivateModel makes a stored version the active one (e.g. a rollback; the
// next start activates the files again if they differ).
func (e *Engine) ActivateModel(ctx context.Context, id string) error {
	if id == "" {
		return apperr.Invalid("id", "is required")
	}
	if err := e.uow.Do(ctx, func(ctx context.Context) error { return e.store.Activate(ctx, id) }); err != nil {
		return err
	}
	e.forgetActive()
	return nil
}

func (e *Engine) GetModel(ctx context.Context, id string) (*store.StoredModel, error) {
	return e.store.GetModel(ctx, id)
}

// model resolves id ("" = active) to a compiled model, cached.
func (e *Engine) model(ctx context.Context, id string) (*engine.Model, error) {
	if id == "" {
		var err error
		if id, err = e.activeModelID(ctx); err != nil {
			return nil, err
		}
	}

	e.mu.Lock()
	m, ok := e.models[id]
	e.mu.Unlock()
	if ok {
		return m, nil
	}

	stored, err := e.store.GetModel(ctx, id)
	if err != nil {
		return nil, err
	}
	var p openfgav1.AuthorizationModel
	if err := protojson.Unmarshal(stored.Model, &p); err != nil {
		return nil, fmt.Errorf("decode authorization model %s: %w", id, err)
	}
	if m, err = engine.NewModel(id, &p); err != nil {
		return nil, err
	}

	e.mu.Lock()
	e.models[id] = m
	e.mu.Unlock()
	return m, nil
}

func (e *Engine) activeModelID(ctx context.Context) (string, error) {
	e.mu.Lock()
	if e.activeID != "" && time.Since(e.activeAt) < activeModelTTL {
		id := e.activeID
		e.mu.Unlock()
		return id, nil
	}
	e.mu.Unlock()

	id, err := e.store.ActiveModelID(ctx)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", apperr.Invalid("authorization_model_id", "no active authorization model")
	}
	e.mu.Lock()
	e.activeID, e.activeAt = id, time.Now()
	e.mu.Unlock()
	return id, nil
}

func (e *Engine) forgetActive() {
	e.mu.Lock()
	e.activeID = ""
	e.mu.Unlock()
}

func parseTuples(m *engine.Model, field string, in []TupleInput, validate bool) ([]engine.Tuple, error) {
	out := make([]engine.Tuple, 0, len(in))
	for i, t := range in {
		f := fmt.Sprintf("%s[%d]", field, i)
		obj, err := engine.ParseObject(f+".object", t.Object)
		if err != nil {
			return nil, err
		}
		if err := engine.ValidateRelation(f+".relation", t.Relation); err != nil {
			return nil, err
		}
		sub, err := engine.ParseSubject(f+".subject", t.Subject)
		if err != nil {
			return nil, err
		}
		tuple := engine.Tuple{Object: obj, Relation: t.Relation, Subject: sub, TenantID: t.TenantID}
		if validate {
			if err := m.ValidateTuple(tuple); err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}
		}
		out = append(out, tuple)
	}
	return out, nil
}

func actor(ctx context.Context) string {
	if id, ok := reqctx.IdentityFrom(ctx); ok && id.UserID != "" {
		return "user:" + id.UserID
	}
	return "service"
}

func pageSize(limit int) int {
	switch {
	case limit <= 0:
		return defaultPageSize
	case limit > maxPageSize:
		return maxPageSize
	}
	return limit
}
