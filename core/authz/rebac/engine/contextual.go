package engine

import "context"

// WithContextual returns a reader that sees tuples as if stored, in addition to
// r's. Contextual tuples hold for one evaluation only, e.g. the caller's tenant
// role taken from their verified access token (tenant:t1#member@user:u1), so
// memberships never have to be copied into the tuple store.
func WithContextual(r TupleReader, tuples []Tuple) TupleReader {
	if len(tuples) == 0 {
		return r
	}
	return &contextualReader{TupleReader: r, tuples: tuples}
}

type contextualReader struct {
	TupleReader
	tuples []Tuple
}

func (c *contextualReader) HasDirect(ctx context.Context, obj Object, relation string, subject Subject) (bool, error) {
	for _, t := range c.tuples {
		if t.Object == obj && t.Relation == relation && t.Subject.Type == subject.Type &&
			((t.Subject.ID == subject.ID && t.Subject.Relation == subject.Relation) ||
				(subject.Relation == "" && t.Subject.ID == "*" && t.Subject.Relation == "")) {
			return true, nil
		}
	}
	return c.TupleReader.HasDirect(ctx, obj, relation, subject)
}

func (c *contextualReader) Usersets(ctx context.Context, obj Object, relation string) ([]Subject, error) {
	out, err := c.TupleReader.Usersets(ctx, obj, relation)
	for _, t := range c.tuples {
		if t.Object == obj && t.Relation == relation && t.Subject.Relation != "" {
			out = append(out, t.Subject)
		}
	}
	return out, err
}

func (c *contextualReader) DirectObjects(ctx context.Context, obj Object, relation string) ([]Object, error) {
	out, err := c.TupleReader.DirectObjects(ctx, obj, relation)
	for _, t := range c.tuples {
		if t.Object == obj && t.Relation == relation && t.Subject.Relation == "" && t.Subject.ID != "*" {
			out = append(out, Object{Type: t.Subject.Type, ID: t.Subject.ID})
		}
	}
	return out, err
}

func (c *contextualReader) BySubject(ctx context.Context, s Subject) ([]Tuple, error) {
	out, err := c.TupleReader.BySubject(ctx, s)
	for _, t := range c.tuples {
		if t.Subject.Type == s.Type && ((t.Subject.ID == s.ID && t.Subject.Relation == s.Relation) ||
			(s.Relation == "" && t.Subject.ID == "*" && t.Subject.Relation == "")) {
			out = append(out, t)
		}
	}
	return out, err
}
