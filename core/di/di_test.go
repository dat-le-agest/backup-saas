package di

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"core/graph"
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

func TestBuildNamesTheCycle(t *testing.T) {
	type A struct{}
	type B struct{}
	type C struct{}
	c := New()
	_ = Provide[*A](c, func(*C) *A { return nil }, Singleton)
	_ = Provide[*B](c, func(*A) *B { return nil }, Singleton)
	_ = Provide[*C](c, func(*B) *C { return nil }, Singleton)

	err := c.Build()
	var cerr *graph.CycleError[reflect.Type]
	if !errors.As(err, &cerr) {
		t.Fatalf("want a graph.CycleError, got %v", err)
	}
	for _, name := range []string{"di.A", "di.B", "di.C", "→"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("%q does not mention %s", err, name)
		}
	}
}

func TestBuildMissingProvider(t *testing.T) {
	c := New()
	_ = Provide[*UserService](c, func(r UserRepo) *UserService { return &UserService{repo: r} }, Singleton)
	err := c.Build()
	if err == nil || !strings.Contains(err.Error(), "missing provider for type di.UserRepo") {
		t.Fatalf("got %v", err)
	}
}

func TestBuildCreatesSingletonsInDependencyOrder(t *testing.T) {
	var built []string
	c := New()
	// registered dependents first: Build still constructs dependencies first
	_ = Provide[*UserService](c, func(r UserRepo) *UserService {
		built = append(built, "service")
		return &UserService{repo: r}
	}, Singleton)
	_ = Provide[UserRepo](c, func(db *DB) *userRepo {
		built = append(built, "repo")
		return &userRepo{db: db}
	}, Singleton)
	_ = Provide[*DB](c, func() *DB {
		built = append(built, "db")
		return &DB{name: "main"}
	}, Singleton)
	_ = Provide[string](c, func() string {
		built = append(built, "transient")
		return "x"
	}, Transient)

	if err := c.Build(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"db", "repo", "service"}; !slices.Equal(built, want) {
		t.Fatalf("built %v, want %v (transient stays lazy)", built, want)
	}

	MustGet[*UserService](c)
	if len(built) != 3 {
		t.Fatalf("Get rebuilt a singleton: %v", built)
	}
	if err := c.Build(); err != nil || len(built) != 3 {
		t.Fatalf("second Build: %v, built %v", err, built)
	}
}

func TestBuildSurfacesProviderError(t *testing.T) {
	c := New()
	_ = Provide[*DB](c, func() (*DB, error) { return nil, errors.New("boom") }, Singleton)
	if err := c.Build(); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected boom from Build, got %v", err)
	}
}
