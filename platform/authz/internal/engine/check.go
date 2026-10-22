package engine

import (
	"context"
	"errors"
	"fmt"

	"core/apperr"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
)

// DefaultMaxDepth bounds recursion (nested usersets, "x from y" chains), like OpenFGA.
const DefaultMaxDepth = 25

// ErrTooComplex: resolution needed more than the maximum depth. Treated as a
// failure (not as "denied") so callers don't silently deny or allow.
var ErrTooComplex = fmt.Errorf("%w: authorization check exceeds the maximum depth", apperr.ErrInvalid)

// TupleReader is the storage the engine reads.
type TupleReader interface {
	// HasDirect: is there a tuple object#relation@subject, or object#relation@subject_type:*
	// when subject is not a userset?
	HasDirect(ctx context.Context, obj Object, relation string, subject Subject) (bool, error)
	// Usersets: subjects of object#relation that are usersets (team:eng#member).
	Usersets(ctx context.Context, obj Object, relation string) ([]Subject, error)
	// DirectObjects: plain (non-userset, non-wildcard) subjects of object#relation,
	// the parents followed by "x from relation".
	DirectObjects(ctx context.Context, obj Object, relation string) ([]Object, error)
	// BySubject: tuples whose subject is exactly s, plus wildcard tuples of
	// s.Type when s is not a userset (reverse lookup for ListObjects).
	BySubject(ctx context.Context, s Subject) ([]Tuple, error)
}

// Cache shares sub-results (object#relation@subject → allowed) across requests.
// Keys passed to it already include the model and the tuple store revision.
type Cache interface {
	Get(key string) (allowed, ok bool)
	Add(key string, allowed bool)
}

// Checker evaluates one model over one reader.
type Checker struct {
	model    *Model
	reader   TupleReader
	maxDepth int

	cache       Cache
	cachePrefix string
}

// WithCache shares results through cache. prefix must identify the model and
// the revision of the tuples, so entries never outlive the data they reflect.
func (c *Checker) WithCache(cache Cache, prefix string) *Checker {
	c.cache, c.cachePrefix = cache, prefix
	return c
}

func NewChecker(m *Model, r TupleReader) *Checker {
	return &Checker{model: m, reader: r, maxDepth: DefaultMaxDepth}
}

// WithMaxDepth overrides DefaultMaxDepth (tests).
func (c *Checker) WithMaxDepth(d int) *Checker { c.maxDepth = d; return c }

// Check reports whether subject has relation on obj.
func (c *Checker) Check(ctx context.Context, obj Object, relation string, subject Subject) (bool, error) {
	if !c.model.HasType(obj.Type) {
		return false, apperr.Invalid("object", fmt.Sprintf("unknown type %q", obj.Type))
	}
	if !c.model.HasRelation(obj.Type, relation) {
		return false, apperr.Invalid("relation", fmt.Sprintf("type %q has no relation %q", obj.Type, relation))
	}
	if subject.IsWildcard() {
		return false, apperr.Invalid("subject", "a wildcard can't be checked")
	}
	return c.newState(subject).check(ctx, obj, relation, 0)
}

type nodeKey struct {
	obj Object
	rel string
}

// state is per request: one subject, memoized results, cycle detection.
type state struct {
	*Checker
	subject  Subject
	memo     map[nodeKey]bool
	visiting map[nodeKey]bool
	// incremented each time a cycle is cut; results computed while a cycle was
	// cut may be incomplete and are not memoized
	cycles int
}

func (c *Checker) newState(s Subject) *state {
	return &state{Checker: c, subject: s, memo: map[nodeKey]bool{}, visiting: map[nodeKey]bool{}}
}

func (s *state) check(ctx context.Context, obj Object, rel string, depth int) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if depth > s.maxDepth {
		return false, ErrTooComplex
	}
	// a userset is a member of itself: team:eng#member has member on team:eng
	if s.subject.Relation == rel && s.subject.Type == obj.Type && s.subject.ID == obj.ID {
		return true, nil
	}
	// "x from parent" may reach a type that doesn't define x: no access through it
	if !s.model.HasRelation(obj.Type, rel) {
		return false, nil
	}

	key := nodeKey{obj, rel}
	if res, ok := s.memo[key]; ok {
		return res, nil
	}
	var cacheKey string
	if s.cache != nil {
		cacheKey = s.cachePrefix + obj.String() + "#" + rel + "@" + s.subject.String()
		if res, ok := s.cache.Get(cacheKey); ok {
			s.memo[key] = res
			return res, nil
		}
	}
	if s.visiting[key] {
		// cycle (e.g. group a ⊂ group b ⊂ group a): this path adds nothing
		s.cycles++
		return false, nil
	}

	s.visiting[key] = true
	cyclesBefore := s.cycles
	res, err := s.eval(ctx, obj, rel, s.model.rewrite(obj.Type, rel), depth)
	delete(s.visiting, key)

	// results computed while a cycle was cut may be incomplete: keep them for nobody
	if err == nil && s.cycles == cyclesBefore {
		s.memo[key] = res
		if s.cache != nil {
			s.cache.Add(cacheKey, res)
		}
	}
	return res, err
}

func (s *state) eval(ctx context.Context, obj Object, rel string, us *openfgav1.Userset, depth int) (bool, error) {
	switch {
	case isThis(us):
		return s.direct(ctx, obj, rel, depth)

	case us.GetComputedUserset() != nil:
		return s.check(ctx, obj, us.GetComputedUserset().GetRelation(), depth+1)

	case us.GetTupleToUserset() != nil:
		ttu := us.GetTupleToUserset()
		parents, err := s.reader.DirectObjects(ctx, obj, ttu.GetTupleset().GetRelation())
		if err != nil {
			return false, err
		}
		return s.any(len(parents), func(i int) (bool, error) {
			return s.check(ctx, parents[i], ttu.GetComputedUserset().GetRelation(), depth+1)
		})

	case us.GetUnion() != nil:
		children := us.GetUnion().GetChild()
		return s.any(len(children), func(i int) (bool, error) {
			return s.eval(ctx, obj, rel, children[i], depth)
		})

	case us.GetIntersection() != nil:
		var firstErr error
		for _, child := range us.GetIntersection().GetChild() {
			ok, err := s.eval(ctx, obj, rel, child, depth)
			switch {
			case err != nil && firstErr == nil:
				firstErr = err
			case err == nil && !ok:
				return false, nil // one false decides, whatever the errors
			}
		}
		return firstErr == nil, firstErr

	case us.GetDifference() != nil:
		base, err := s.eval(ctx, obj, rel, us.GetDifference().GetBase(), depth)
		if err != nil || !base {
			return false, err
		}
		excluded, err := s.eval(ctx, obj, rel, us.GetDifference().GetSubtract(), depth)
		if err != nil {
			return false, err
		}
		return !excluded, nil
	}
	return false, errors.New("authz: unsupported rewrite")
}

// direct: a tuple names the subject (or its wildcard), or names a userset the
// subject belongs to.
func (s *state) direct(ctx context.Context, obj Object, rel string, depth int) (bool, error) {
	ok, err := s.reader.HasDirect(ctx, obj, rel, s.subject)
	if err != nil || ok {
		return ok, err
	}
	usersets, err := s.reader.Usersets(ctx, obj, rel)
	if err != nil {
		return false, err
	}
	return s.any(len(usersets), func(i int) (bool, error) {
		return s.check(ctx, usersets[i].Object(), usersets[i].Relation, depth+1)
	})
}

// any: true if one branch is true (even if another failed); otherwise the first error.
func (s *state) any(n int, branch func(i int) (bool, error)) (bool, error) {
	var firstErr error
	for i := 0; i < n; i++ {
		ok, err := branch(i)
		if err == nil && ok {
			return true, nil
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return false, firstErr
}
