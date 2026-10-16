package di

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

type DB struct{ name string }

type UserRepo interface{ Name() string }

type userRepo struct{ db *DB }

func (r *userRepo) Name() string { return r.db.name }

type UserService struct{ repo UserRepo }

func TestInstanceBridgeAndInterfaceBinding(t *testing.T) {
	c := New()
	if err := Instance[*DB](c, &DB{name: "main"}); err != nil {
		t.Fatal(err)
	}
	// concrete constructor registered under interface type
	if err := Provide[UserRepo](c, func(db *DB) *userRepo { return &userRepo{db: db} }, Singleton); err != nil {
		t.Fatal(err)
	}
	if err := Provide[*UserService](c, func(r UserRepo) (*UserService, error) { return &UserService{repo: r}, nil }, Singleton); err != nil {
		t.Fatal(err)
	}
	if err := c.Build(); err != nil {
		t.Fatal(err)
	}

	svc := MustGet[*UserService](c)
	if svc.repo.Name() != "main" {
		t.Fatalf("got %q", svc.repo.Name())
	}
	if MustGet[*UserService](c) != svc {
		t.Fatal("singleton not cached")
	}
}

func TestProviderError(t *testing.T) {
	c := New()
	_ = Provide[*DB](c, func() (*DB, error) { return nil, errors.New("boom") }, Singleton)
	if _, err := Get[*DB](c); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected boom, got %v", err)
	}
}

func TestTypeMismatch(t *testing.T) {
	c := New()
	if err := Provide[UserRepo](c, func() *DB { return nil }, Singleton); err == nil {
		t.Fatal("expected mismatch error")
	}
}

func TestCycleWithoutBuild(t *testing.T) {
	type A struct{}
	type B struct{}
	c := New()
	_ = Provide[*A](c, func(*B) *A { return nil }, Singleton)
	_ = Provide[*B](c, func(*A) *B { return nil }, Singleton)
	if _, err := Get[*A](c); err == nil || !strings.Contains(err.Error(), "circular") {
		t.Fatalf("expected cycle error, got %v", err)
	}
	if err := c.Build(); err == nil {
		t.Fatal("Build should detect cycle")
	}
}

func TestConcurrentSingleton(t *testing.T) {
	c := New()
	calls := 0
	_ = Provide[*DB](c, func() *DB { calls++; return &DB{} }, Singleton)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); MustGet[*DB](c) }()
	}
	wg.Wait()
	if calls != 1 {
		t.Fatalf("provider called %d times", calls)
	}
}
