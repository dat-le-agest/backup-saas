package redisc

import (
	"context"
	"testing"

	"core/logger"
	"core/sctx"

	"github.com/alicebob/miniredis/v2"
)

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

type fakeSC struct{ sctx.ServiceContext }

func (fakeSC) Logger(string) logger.Logger { return nopLogger{} }

func TestDisabledWithoutURL(t *testing.T) {
	r := NewRedis("redis", "")
	if err := r.Activate(fakeSC{}); err != nil || r.Client() != nil {
		t.Fatalf("expected disabled: %v", err)
	}
	_ = r.Stop()
}

func TestConnects(t *testing.T) {
	mr := miniredis.RunT(t)
	r := NewRedis("redis", "")
	r.url = "redis://" + mr.Addr() + "/0"
	if err := r.Activate(fakeSC{}); err != nil {
		t.Fatal(err)
	}
	defer r.Stop()
	if err := r.Client().Set(context.Background(), "k", "v", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if v, _ := mr.Get("k"); v != "v" {
		t.Fatalf("got %q", v)
	}
}

func TestFailsWhenUnreachable(t *testing.T) {
	mr := miniredis.RunT(t)
	addr := mr.Addr()
	mr.Close()

	r := NewRedis("redis", "")
	r.url = "redis://" + addr
	if err := r.Activate(fakeSC{}); err == nil {
		t.Fatal("expected ping error")
	}
	if r.Client() != nil {
		t.Fatal("client should be nil after failed activation")
	}
}
