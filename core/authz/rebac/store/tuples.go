// Package repository stores tuples, the changelog and authorization models in
// Postgres (migrations/app). Every app has them in its own schema: a Store is
// bound to one app (For) and qualifies every table with the app's schema. The
// authz store is cross-tenant: no row-level security, tenant_id is data.
package store

import (
	"context"
	"errors"
	"time"

	"core/authz/rebac/engine"
	"core/migrate"
	"core/uow"

	"gorm.io/gorm"
)

type Store struct {
	uow    *uow.Gorm
	app    string // schema name, for gorm's Table (which quotes it)
	schema string // quoted schema, for raw SQL; "" until For
}

// NewStore returns a store not bound to an app yet: use For.
func NewStore(u *uow.Gorm) *Store {
	return &Store{uow: u}
}

// For returns the store of app (its schema).
func (s *Store) For(app string) (*Store, error) {
	q, err := migrate.QuoteSchema(app)
	if err != nil {
		return nil, err
	}
	return &Store{uow: s.uow, app: app, schema: q}, nil
}

// t qualifies a table with the app's schema. Panics on a store not bound to
// an app: that's a wiring bug, never a request error.
func (s *Store) t(table string) string {
	if s.schema == "" {
		panic(errors.New("authz repository: store not bound to an app (Store.For)"))
	}
	return s.schema + "." + table
}

// table is t for gorm's Table, which quotes each part of "schema.table" itself.
func (s *Store) table(table string) string {
	s.t(table) // same check
	return s.app + "." + table
}

func (s *Store) db(ctx context.Context) *gorm.DB { return s.uow.DB(ctx) }

var _ engine.TupleReader = (*Store)(nil)

// HasDirect uses the primary key (exact tuple) and the subject index (wildcard).
func (s *Store) HasDirect(ctx context.Context, obj engine.Object, rel string, sub engine.Subject) (bool, error) {
	var found bool
	err := s.db(ctx).Raw(`
		SELECT EXISTS (
			SELECT 1 FROM `+s.t("relation_tuples")+`
			WHERE object_type = @ot AND object_id = @oid AND relation = @rel AND subject_type = @st
			  AND ((subject_id = @sid AND subject_relation = @srel)
			       OR (@srel = '' AND subject_id = '*' AND subject_relation = ''))
		)`,
		map[string]any{"ot": obj.Type, "oid": obj.ID, "rel": rel, "st": sub.Type, "sid": sub.ID, "srel": sub.Relation},
	).Scan(&found).Error
	return found, err
}

// Usersets is served by the partial index relation_tuples_usersets.
func (s *Store) Usersets(ctx context.Context, obj engine.Object, rel string) ([]engine.Subject, error) {
	var rows []struct{ SubjectType, SubjectID, SubjectRelation string }
	err := s.db(ctx).Raw(`
		SELECT subject_type, subject_id, subject_relation FROM `+s.t("relation_tuples")+`
		WHERE object_type = ? AND object_id = ? AND relation = ? AND subject_relation <> ''`,
		obj.Type, obj.ID, rel,
	).Scan(&rows).Error
	out := make([]engine.Subject, 0, len(rows))
	for _, r := range rows {
		out = append(out, engine.Subject{Type: r.SubjectType, ID: r.SubjectID, Relation: r.SubjectRelation})
	}
	return out, err
}

func (s *Store) DirectObjects(ctx context.Context, obj engine.Object, rel string) ([]engine.Object, error) {
	var rows []struct{ SubjectType, SubjectID string }
	err := s.db(ctx).Raw(`
		SELECT subject_type, subject_id FROM `+s.t("relation_tuples")+`
		WHERE object_type = ? AND object_id = ? AND relation = ? AND subject_relation = '' AND subject_id <> '*'`,
		obj.Type, obj.ID, rel,
	).Scan(&rows).Error
	out := make([]engine.Object, 0, len(rows))
	for _, r := range rows {
		out = append(out, engine.Object{Type: r.SubjectType, ID: r.SubjectID})
	}
	return out, err
}

// BySubject is served by relation_tuples_by_subject.
func (s *Store) BySubject(ctx context.Context, sub engine.Subject) ([]engine.Tuple, error) {
	var rows []tupleRow
	err := s.db(ctx).Raw(`
		SELECT object_type, object_id, relation, subject_type, subject_id, subject_relation, tenant_id
		FROM `+s.t("relation_tuples")+`
		WHERE subject_type = @st AND (
			(subject_id = @sid AND subject_relation = @srel)
			OR (@srel = '' AND subject_id = '*' AND subject_relation = ''))`,
		map[string]any{"st": sub.Type, "sid": sub.ID, "srel": sub.Relation},
	).Scan(&rows).Error
	out := make([]engine.Tuple, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.tuple())
	}
	return out, err
}

type tupleRow struct {
	ObjectType, ObjectID, Relation, SubjectType, SubjectID, SubjectRelation, TenantID string
}

func (r tupleRow) tuple() engine.Tuple {
	return engine.Tuple{
		Object:   engine.Object{Type: r.ObjectType, ID: r.ObjectID},
		Relation: r.Relation,
		Subject:  engine.Subject{Type: r.SubjectType, ID: r.SubjectID, Relation: r.SubjectRelation},
		TenantID: r.TenantID,
	}
}

// SetActor records who changes tuples (read by the changelog trigger) for the
// rest of the current transaction.
func (s *Store) SetActor(ctx context.Context, actor, requestID string) error {
	return s.db(ctx).Exec(`SELECT set_config('app.actor', ?, true), set_config('app.request_id', ?, true)`,
		actor, requestID).Error
}

// WriteTuples inserts tuples; existing ones are left as they are (idempotent).
func (s *Store) WriteTuples(ctx context.Context, tuples []engine.Tuple) error {
	for _, t := range tuples {
		err := s.db(ctx).Exec(`
			INSERT INTO `+s.t("relation_tuples")+`
				(object_type, object_id, relation, subject_type, subject_id, subject_relation, tenant_id, created_by)
			VALUES (?, ?, ?, ?, ?, ?, ?, coalesce(current_setting('app.actor', true), ''))
			ON CONFLICT DO NOTHING`,
			t.Object.Type, t.Object.ID, t.Relation, t.Subject.Type, t.Subject.ID, t.Subject.Relation, t.TenantID,
		).Error
		if err != nil {
			return err
		}
	}
	return nil
}

// DeleteTuples removes tuples; missing ones are ignored (idempotent).
func (s *Store) DeleteTuples(ctx context.Context, tuples []engine.Tuple) error {
	for _, t := range tuples {
		err := s.db(ctx).Exec(`
			DELETE FROM `+s.t("relation_tuples")+`
			WHERE object_type = ? AND object_id = ? AND relation = ?
			  AND subject_type = ? AND subject_id = ? AND subject_relation = ?`,
			t.Object.Type, t.Object.ID, t.Relation, t.Subject.Type, t.Subject.ID, t.Subject.Relation,
		).Error
		if err != nil {
			return err
		}
	}
	return nil
}

// DeleteObjects removes every tuple on each object and every tuple naming it as
// subject (directly or as a userset), e.g. after the object was deleted.
// Two statements so each uses an index (primary key / relation_tuples_by_subject).
func (s *Store) DeleteObjects(ctx context.Context, objects []engine.Object) error {
	for _, o := range objects {
		if err := s.db(ctx).Exec(`DELETE FROM `+s.t("relation_tuples")+` WHERE object_type = ? AND object_id = ?`, o.Type, o.ID).Error; err != nil {
			return err
		}
		if err := s.db(ctx).Exec(`DELETE FROM `+s.t("relation_tuples")+` WHERE subject_type = ? AND subject_id = ?`, o.Type, o.ID).Error; err != nil {
			return err
		}
	}
	return nil
}

// Revision is the tuple store version, bumped by every statement changing tuples
// (migration 00002). Results cached under a revision reflect at least that state.
func (s *Store) Revision(ctx context.Context) (int64, error) {
	var rev int64
	err := s.db(ctx).Raw(`SELECT value FROM ` + s.t("authz_revision")).Scan(&rev).Error
	return rev, err
}

type Change struct {
	Seq       int64
	Operation string
	Tuple     engine.Tuple
	Actor     string
	ChangedAt time.Time
}

// ReadChanges returns changes after afterSeq, oldest first. objectType "" = all.
func (s *Store) ReadChanges(ctx context.Context, objectType string, afterSeq int64, limit int) ([]Change, error) {
	// flat fields: gorm doesn't fill an embedded struct of an unexported type
	var rows []struct {
		Seq                                                                               int64
		Operation                                                                         string
		ObjectType, ObjectID, Relation, SubjectType, SubjectID, SubjectRelation, TenantID string
		Actor                                                                             string
		ChangedAt                                                                         time.Time
	}
	q := s.db(ctx).Table(s.table("tuple_changes")).
		Select("seq, operation, object_type, object_id, relation, subject_type, subject_id, subject_relation, tenant_id, actor, changed_at").
		Where("seq > ?", afterSeq)
	if objectType != "" {
		q = q.Where("object_type = ?", objectType)
	}
	err := q.Order("seq").Limit(limit).Scan(&rows).Error
	out := make([]Change, 0, len(rows))
	for _, r := range rows {
		tuple := tupleRow{r.ObjectType, r.ObjectID, r.Relation, r.SubjectType, r.SubjectID, r.SubjectRelation, r.TenantID}.tuple()
		out = append(out, Change{Seq: r.Seq, Operation: r.Operation, Tuple: tuple, Actor: r.Actor, ChangedAt: r.ChangedAt})
	}
	return out, err
}
