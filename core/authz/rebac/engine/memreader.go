package engine

import "context"

// MemReader is an in-memory TupleReader, e.g. to evaluate a model over the
// relationships implied by a request (RBAC from the model) instead of stored ones.
type MemReader struct{ tuples []Tuple }

// NewMemReader reads tuples.
func NewMemReader(tuples ...Tuple) *MemReader { return &MemReader{tuples: tuples} }

func (m *MemReader) HasDirect(_ context.Context, obj Object, rel string, s Subject) (bool, error) {
	for _, t := range m.tuples {
		if t.Object != obj || t.Relation != rel || t.Subject.Type != s.Type {
			continue
		}
		if t.Subject == s || (s.Relation == "" && t.Subject.ID == "*") {
			return true, nil
		}
	}
	return false, nil
}

func (m *MemReader) Usersets(_ context.Context, obj Object, rel string) ([]Subject, error) {
	var out []Subject
	for _, t := range m.tuples {
		if t.Object == obj && t.Relation == rel && t.Subject.Relation != "" {
			out = append(out, t.Subject)
		}
	}
	return out, nil
}

func (m *MemReader) DirectObjects(_ context.Context, obj Object, rel string) ([]Object, error) {
	var out []Object
	for _, t := range m.tuples {
		if t.Object == obj && t.Relation == rel && t.Subject.Relation == "" && !t.Subject.IsWildcard() {
			out = append(out, t.Subject.Object())
		}
	}
	return out, nil
}

func (m *MemReader) BySubject(_ context.Context, s Subject) ([]Tuple, error) {
	var out []Tuple
	for _, t := range m.tuples {
		if t.Subject == s || (s.Relation == "" && t.Subject.Type == s.Type && t.Subject.ID == "*") {
			out = append(out, t)
		}
	}
	return out, nil
}
