package authz_test

import (
	"context"
	"errors"
	"testing"

	"core/authz"
	"core/logger"
	"core/outbox"
	"core/testdb"
	"core/uow"
)

func TestRelationshipsGoThroughTheOutbox(t *testing.T) {
	db := testdb.New(t)
	up := outbox.MigrationSQL[:len(outbox.MigrationSQL)-len("-- +goose Down\nDROP TABLE outbox_messages;\n")]
	if err := db.Exec(up).Error; err != nil {
		t.Fatal(err)
	}
	u := uow.New(db)
	ob := outbox.New(u)
	rel := authz.NewOutboxRelationships(ob)

	owner := authz.Tuple{Object: "note:1", Relation: "owner", Subject: "user:u1", TenantID: "t1"}
	err := u.Do(context.Background(), func(ctx context.Context) error {
		if err := rel.Write(ctx, owner, authz.Tuple{Object: "note:1", Relation: "tenant", Subject: "tenant:t1", TenantID: "t1"}); err != nil {
			return err
		}
		if err := rel.Delete(ctx, owner); err != nil {
			return err
		}
		return rel.DeleteObjects(ctx, "note:1")
	})
	if err != nil {
		t.Fatal(err)
	}

	var got []authz.TupleChanges
	relay := ob.NewRelay(db, nopLogger{}, outbox.RelayOptions{})
	relay.Handle(authz.TuplesTopic, authz.DeliverTuples(func(_ context.Context, c authz.TupleChanges) error {
		got = append(got, c)
		return nil
	}))
	if n, err := relay.RunOnce(context.Background()); err != nil || n != 3 {
		t.Fatalf("relay: %d %v", n, err)
	}
	if len(got) != 3 || len(got[0].Writes) != 2 || got[1].Deletes[0] != owner || got[2].DeleteObjects[0] != "note:1" {
		t.Fatalf("delivered %+v", got)
	}

	// outside a unit of work the change is refused, never written half-way
	if err := rel.Write(context.Background(), owner); !errors.Is(err, outbox.ErrNotInTransaction) {
		t.Fatalf("outside uow: %v", err)
	}
}

type nopLogger struct{}

func (nopLogger) Debug(...interface{})                     {}
func (nopLogger) Info(...interface{})                      {}
func (nopLogger) Warn(...interface{})                      {}
func (nopLogger) Error(...interface{})                     {}
func (nopLogger) Debugf(string, ...interface{})            {}
func (nopLogger) Infof(string, ...interface{})             {}
func (nopLogger) Warnf(string, ...interface{})             {}
func (nopLogger) Errorf(string, ...interface{})            {}
func (l nopLogger) With(string, interface{}) logger.Logger { return l }
func (l nopLogger) WithFields(logger.Fields) logger.Logger { return l }
