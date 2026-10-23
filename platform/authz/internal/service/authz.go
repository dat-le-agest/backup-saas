// Package service runs authorization requests: resolves the authorization model,
// validates input against it and evaluates with the engine over the store.
//
// Every app has its own store (a Postgres schema): models, tuples, changelog
// and revision. Every call names the app; caches are per app.
package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"authz/internal/engine"
	"authz/internal/repository"
	"core/apperr"
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
	// cached sub-results (object#relation@subject) per replica, all apps together
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

// Apps are the apps served (PLATFORM_APPS); each has a store (schema).
type Apps []string

type AuthzService struct {
	uow   uow.UnitOfWork
	cache *checkCache
	apps  map[string]*appState // fixed after construction
}

// appState is the store and the replica-local state of one app.
type appState struct {
	store *repository.Store

	mu       sync.Mutex
	models   map[string]*engine.Model // by id; models are immutable
	activeID string
	activeAt time.Time
	revision int64
	revAt    time.Time
}

func NewAuthzService(store *repository.Store, u uow.UnitOfWork, apps Apps) (*AuthzService, error) {
	s := &AuthzService{uow: u, cache: newCheckCache(CacheSize), apps: make(map[string]*appState, len(apps))}
	for _, app := range apps {
		st, err := store.For(app)
		if err != nil {
			return nil, err
		}
		s.apps[app] = &appState{store: st, models: map[string]*engine.Model{}}
	}
	return s, nil
}

// app returns the state of a served app; invalid input otherwise.
func (s *AuthzService) app(app string) (*appState, error) {
	if app == "" {
		return nil, apperr.Invalid("app", "is required")
	}
	a, ok := s.apps[app]
	if !ok {
		return nil, apperr.Invalid("app", "is not a known app")
	}
	return a, nil
}

// ---- checks ----

func (s *AuthzService) Check(ctx context.Context, app, object, relation, subject, modelID string, consistency Consistency) (bool, error) {
	a, err := s.app(app)
	if err != nil {
		return false, err
	}
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
	checker, err := s.checker(ctx, app, a, modelID, consistency)
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
func (s *AuthzService) BatchCheck(ctx context.Context, app string, checks []CheckInput, modelID string, consistency Consistency) ([]CheckResult, error) {
	a, err := s.app(app)
	if err != nil {
		return nil, err
	}
	if len(checks) == 0 {
		return nil, apperr.Invalid("checks", "is required")
	}
	if len(checks) > maxBatchChecks {
		return nil, apperr.Invalid("checks", fmt.Sprintf("at most %d checks per request", maxBatchChecks))
	}
	checker, err := s.checker(ctx, app, a, modelID, consistency)
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

func (s *AuthzService) ListObjects(ctx context.Context, app, objType, relation, subject string, limit int, modelID string, consistency Consistency) ([]string, error) {
	a, err := s.app(app)
	if err != nil {
		return nil, err
	}
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
	checker, err := s.checker(ctx, app, a, modelID, consistency)
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

// WriteTuples validates every written tuple against the app's active model,
// then removes deleted objects, deletes and writes in one transaction. Idempotent.
func (s *AuthzService) WriteTuples(ctx context.Context, app string, writes, deletes []TupleInput, deleteObjects []string) error {
	a, err := s.app(app)
	if err != nil {
		return err
	}
	n := len(writes) + len(deletes) + len(deleteObjects)
	if n == 0 {
		return apperr.Invalid("writes", "nothing to write or delete")
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
	m, err := a.model(ctx, "")
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

	err = s.uow.Do(ctx, func(ctx context.Context) error {
		if err := a.store.SetActor(ctx, actor(ctx), reqctx.RequestIDFrom(ctx)); err != nil {
			return err
		}
		if err := a.store.DeleteObjects(ctx, objects); err != nil {
			return err
		}
		if err := a.store.DeleteTuples(ctx, ds); err != nil {
			return err
		}
		return a.store.WriteTuples(ctx, ws)
	})
	if err != nil {
		return err
	}
	// this replica sees its own writes right away; others within RevisionTTL
	_, err = a.currentRevision(ctx, Higher)
	return err
}

// checker evaluates with the app's model and the cache of its current revision.
func (s *AuthzService) checker(ctx context.Context, app string, a *appState, modelID string, consistency Consistency) (*engine.Checker, error) {
	m, err := a.model(ctx, modelID)
	if err != nil {
		return nil, err
	}
	rev, err := a.currentRevision(ctx, consistency)
	if err != nil {
		return nil, err
	}
	// model ids are unique, but the app keeps cache keys of apps apart regardless
	prefix := app + "/" + m.ID + "@" + strconv.FormatInt(rev, 10) + "|"
	return engine.NewChecker(m, a.store).WithCache(s.cache, prefix), nil
}

// currentRevision returns the app's tuple store revision: read again when
// older than RevisionTTL, or always for Higher consistency.
func (a *appState) currentRevision(ctx context.Context, consistency Consistency) (int64, error) {
	a.mu.Lock()
	if consistency != Higher && !a.revAt.IsZero() && time.Since(a.revAt) < RevisionTTL {
		rev := a.revision
		a.mu.Unlock()
		return rev, nil
	}
	a.mu.Unlock()

	rev, err := a.store.Revision(ctx)
	if err != nil {
		return 0, err
	}
	a.mu.Lock()
	if rev >= a.revision { // never go back (a slower concurrent read)
		a.revision, a.revAt = rev, time.Now()
	}
	rev = a.revision
	a.mu.Unlock()
	return rev, nil
}

func (s *AuthzService) ReadChanges(ctx context.Context, app, objectType string, afterSeq int64, limit int) ([]repository.Change, int64, error) {
	a, err := s.app(app)
	if err != nil {
		return nil, 0, err
	}
	if objectType != "" {
		if err := engine.ValidateRelation("object_type", objectType); err != nil {
			return nil, 0, err
		}
	}
	if afterSeq < 0 {
		return nil, 0, apperr.Invalid("after_seq", "must be >= 0")
	}
	changes, err := a.store.ReadChanges(ctx, objectType, afterSeq, pageSize(limit))
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

// LoadModules compiles the app's model from OpenFGA module files (the
// platform module and the app's) and, if it differs from the active model,
// stores it as a new version and activates it. Returns the active model id and
// whether a new version was activated. Called at startup.
func (s *AuthzService) LoadModules(ctx context.Context, app string, files []engine.ModuleFile) (string, bool, error) {
	a, err := s.app(app)
	if err != nil {
		return "", false, err
	}
	compiled, source, err := engine.ParseModules(files)
	if err != nil {
		return "", false, err
	}
	if active, err := a.store.GetModel(ctx, ""); err == nil && active.DSL == source {
		return active.ID, false, nil
	} else if err != nil && !errors.Is(err, apperr.ErrInvalid) { // "no active model" is fine
		return "", false, err
	}
	id, err := s.writeModel(ctx, a, compiled, source, true)
	return id, err == nil, err
}

// WriteModel stores a model written as one schema 1.1 DSL document, optionally
// activating it (tests and single-file models).
func (s *AuthzService) WriteModel(ctx context.Context, app, dsl string, activate bool) (string, error) {
	a, err := s.app(app)
	if err != nil {
		return "", err
	}
	compiled, err := engine.ParseDSL(dsl)
	if err != nil {
		return "", err
	}
	return s.writeModel(ctx, a, compiled, dsl, activate)
}

func (s *AuthzService) writeModel(ctx context.Context, a *appState, compiled *openfgav1.AuthorizationModel, source string, activate bool) (string, error) {
	id := ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader).String()
	compiled.Id = id
	raw, err := protojson.Marshal(compiled)
	if err != nil {
		return "", err
	}

	err = s.uow.Do(ctx, func(ctx context.Context) error {
		if err := a.store.CreateModel(ctx, &repository.StoredModel{
			ID: id, SchemaVersion: compiled.GetSchemaVersion(), DSL: source, Model: raw, CreatedBy: actor(ctx),
		}); err != nil {
			return err
		}
		if activate {
			return a.store.Activate(ctx, id)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if activate {
		a.forgetActive()
	}
	return id, nil
}

func (s *AuthzService) ActivateModel(ctx context.Context, app, id string) error {
	a, err := s.app(app)
	if err != nil {
		return err
	}
	if id == "" {
		return apperr.Invalid("id", "is required")
	}
	if err := s.uow.Do(ctx, func(ctx context.Context) error { return a.store.Activate(ctx, id) }); err != nil {
		return err
	}
	a.forgetActive()
	return nil
}

func (s *AuthzService) GetModel(ctx context.Context, app, id string) (*repository.StoredModel, error) {
	a, err := s.app(app)
	if err != nil {
		return nil, err
	}
	return a.store.GetModel(ctx, id)
}

// model resolves id ("" = active) to a compiled model of the app, cached.
func (a *appState) model(ctx context.Context, id string) (*engine.Model, error) {
	if id == "" {
		var err error
		if id, err = a.activeModelID(ctx); err != nil {
			return nil, err
		}
	}

	a.mu.Lock()
	m, ok := a.models[id]
	a.mu.Unlock()
	if ok {
		return m, nil
	}

	stored, err := a.store.GetModel(ctx, id)
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

	a.mu.Lock()
	a.models[id] = m
	a.mu.Unlock()
	return m, nil
}

func (a *appState) activeModelID(ctx context.Context) (string, error) {
	a.mu.Lock()
	if a.activeID != "" && time.Since(a.activeAt) < activeModelTTL {
		id := a.activeID
		a.mu.Unlock()
		return id, nil
	}
	a.mu.Unlock()

	id, err := a.store.ActiveModelID(ctx)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", apperr.Invalid("authorization_model_id", "no active authorization model")
	}
	a.mu.Lock()
	a.activeID, a.activeAt = id, time.Now()
	a.mu.Unlock()
	return id, nil
}

func (a *appState) forgetActive() {
	a.mu.Lock()
	a.activeID = ""
	a.mu.Unlock()
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
