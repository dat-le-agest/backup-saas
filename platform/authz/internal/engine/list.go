package engine

import (
	"context"
	"fmt"
	"sort"

	"core/apperr"
)

const (
	// candidates and graph nodes explored by ListObjects before giving up
	maxListNodes = 10000
)

// ListObjects returns ids of objects of objType on which subject has relation,
// sorted, at most limit.
//
// It walks the tuple graph backwards from the subject (every object that names
// the subject, or a userset/object the subject reaches, directly or through
// usersets and "x from y" links) to collect candidates, then confirms each one
// with Check. Candidates are a superset of the answer, so the result is exact
// even with "and" / "but not"; the cost grows with what the subject can reach.
func (c *Checker) ListObjects(ctx context.Context, objType, relation string, subject Subject, limit int) ([]string, error) {
	if !c.model.HasRelation(objType, relation) {
		return nil, apperr.Invalid("relation", fmt.Sprintf("type %q has no relation %q", objType, relation))
	}
	if subject.IsWildcard() {
		return nil, apperr.Invalid("subject", "a wildcard can't be listed")
	}

	candidates := map[string]bool{}
	visited := map[Subject]bool{subject: true}
	queue := []Subject{subject}

	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		node := queue[0]
		queue = queue[1:]

		tuples, err := c.reader.BySubject(ctx, node)
		if err != nil {
			return nil, err
		}
		for _, t := range tuples {
			if t.Object.Type == objType {
				candidates[t.Object.ID] = true
			}
			// next: the userset this tuple grants (for tuples naming it), and the
			// object itself (for "x from y" links pointing at it)
			for _, next := range []Subject{
				{Type: t.Object.Type, ID: t.Object.ID, Relation: t.Relation},
				{Type: t.Object.Type, ID: t.Object.ID},
			} {
				if !visited[next] {
					visited[next] = true
					queue = append(queue, next)
				}
			}
		}
		if len(visited)+len(candidates) > maxListNodes {
			return nil, fmt.Errorf("%w: the subject reaches too many objects to list", apperr.ErrInvalid)
		}
	}

	ids := make([]string, 0, len(candidates))
	for id := range candidates {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	st := c.newState(subject)
	out := []string{}
	for _, id := range ids {
		ok, err := st.check(ctx, Object{Type: objType, ID: id}, relation, 0)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, id)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}
