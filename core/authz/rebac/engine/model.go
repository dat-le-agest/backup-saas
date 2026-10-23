package engine

import (
	"fmt"
	"sort"
	"strings"

	"core/apperr"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/openfga/language/pkg/go/transformer"
)

// Model is a validated authorization model, indexed for evaluation.
type Model struct {
	ID    string
	proto *openfgav1.AuthorizationModel
	types map[string]*openfgav1.TypeDefinition
}

// ParseDSL compiles OpenFGA DSL (schema 1.1) and validates it.
func ParseDSL(dsl string) (*openfgav1.AuthorizationModel, error) {
	m, err := transformer.TransformDSLToProto(dsl)
	if err != nil {
		return nil, apperr.Invalid("dsl", err.Error())
	}
	if _, err := NewModel("", m); err != nil {
		return nil, err
	}
	return m, nil
}

// ModuleFile is one OpenFGA module file (`module <name>` + types, `extend type`).
type ModuleFile struct {
	Name     string
	Contents string
}

// ParseModules compiles module files into one model (schema 1.2), e.g. the
// platform module (user, tenant) and an app's module, which may add relations
// to platform types with `extend type tenant`. It also returns the source: the
// files in name order, each preceded by its name, the same for the same files
// (to tell whether a model changed).
func ParseModules(files []ModuleFile) (*openfgav1.AuthorizationModel, string, error) {
	if len(files) == 0 {
		return nil, "", apperr.Invalid("modules", "no module files")
	}
	sorted := append([]ModuleFile(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	in := make([]transformer.ModuleFile, len(sorted))
	var source strings.Builder
	for i, f := range sorted {
		in[i] = transformer.ModuleFile{Name: f.Name, Contents: f.Contents}
		fmt.Fprintf(&source, "# file: %s\n%s\n", f.Name, strings.TrimRight(f.Contents, "\n"))
	}
	m, err := transformer.TransformModuleFilesToModel(in, "1.2")
	if err != nil {
		return nil, "", apperr.Invalid("modules", err.Error())
	}
	if _, err := NewModel("", m); err != nil {
		return nil, "", err
	}
	return m, source.String(), nil
}

// NewModel validates m: every referenced type and relation exists, tuplesets of
// "x from y" are directly assignable relations, and no unsupported feature
// (conditions) is used. Schema 1.2 is 1.1 composed from modules.
func NewModel(id string, m *openfgav1.AuthorizationModel) (*Model, error) {
	if v := m.GetSchemaVersion(); v != "1.1" && v != "1.2" {
		return nil, invalidModel("schema version must be 1.1 or 1.2, got %q", v)
	}
	if len(m.GetConditions()) > 0 {
		return nil, invalidModel("conditions are not supported")
	}

	model := &Model{ID: id, proto: m, types: map[string]*openfgav1.TypeDefinition{}}
	for _, td := range m.GetTypeDefinitions() {
		if !validName(td.GetType()) {
			return nil, invalidModel("invalid type name %q", td.GetType())
		}
		if _, dup := model.types[td.GetType()]; dup {
			return nil, invalidModel("type %q defined twice", td.GetType())
		}
		model.types[td.GetType()] = td
	}

	for _, td := range m.GetTypeDefinitions() {
		for rel, rewrite := range td.GetRelations() {
			if !validName(rel) {
				return nil, invalidModel("invalid relation name %s#%s", td.GetType(), rel)
			}
			if err := model.validateRewrite(td, rel, rewrite); err != nil {
				return nil, err
			}
			direct := model.directTypes(td.GetType(), rel)
			if hasThis(rewrite) != (len(direct) > 0) {
				return nil, invalidModel("%s#%s: directly assignable types and [..] in the rewrite don't match", td.GetType(), rel)
			}
			for _, ref := range direct {
				if ref.GetCondition() != "" {
					return nil, invalidModel("%s#%s: conditions are not supported", td.GetType(), rel)
				}
				if _, ok := model.types[ref.GetType()]; !ok {
					return nil, invalidModel("%s#%s: unknown type %q", td.GetType(), rel, ref.GetType())
				}
				if r := ref.GetRelation(); r != "" && !model.HasRelation(ref.GetType(), r) {
					return nil, invalidModel("%s#%s: unknown relation %s#%s", td.GetType(), rel, ref.GetType(), r)
				}
			}
		}
	}
	return model, nil
}

func (m *Model) validateRewrite(td *openfgav1.TypeDefinition, rel string, us *openfgav1.Userset) error {
	switch {
	case isThis(us):
		return nil
	case us.GetComputedUserset() != nil:
		target := us.GetComputedUserset().GetRelation()
		if !m.HasRelation(td.GetType(), target) {
			return invalidModel("%s#%s: unknown relation %q", td.GetType(), rel, target)
		}
		return nil
	case us.GetTupleToUserset() != nil:
		ttu := us.GetTupleToUserset()
		tupleset := ttu.GetTupleset().GetRelation()
		if !m.HasRelation(td.GetType(), tupleset) {
			return invalidModel("%s#%s: unknown relation %q in \"from %s\"", td.GetType(), rel, tupleset, tupleset)
		}
		// the tupleset must point at plain objects: "admin from tenant" needs tenant: [tenant]
		ts := m.types[td.GetType()].GetRelations()[tupleset]
		if !isThis(ts) {
			return invalidModel("%s#%s: %q in \"from %s\" must be a directly assignable relation", td.GetType(), rel, tupleset, tupleset)
		}
		found := false
		for _, ref := range m.directTypes(td.GetType(), tupleset) {
			if ref.GetRelation() != "" || ref.GetWildcard() != nil {
				return invalidModel("%s#%s: %q in \"from %s\" may only hold objects, not usersets or wildcards", td.GetType(), rel, tupleset, tupleset)
			}
			if m.HasRelation(ref.GetType(), ttu.GetComputedUserset().GetRelation()) {
				found = true
			}
		}
		if !found {
			return invalidModel("%s#%s: no type in %q defines relation %q", td.GetType(), rel, tupleset, ttu.GetComputedUserset().GetRelation())
		}
		return nil
	case us.GetUnion() != nil:
		return m.validateChildren(td, rel, us.GetUnion().GetChild())
	case us.GetIntersection() != nil:
		return m.validateChildren(td, rel, us.GetIntersection().GetChild())
	case us.GetDifference() != nil:
		return m.validateChildren(td, rel, []*openfgav1.Userset{us.GetDifference().GetBase(), us.GetDifference().GetSubtract()})
	}
	return invalidModel("%s#%s: unsupported rewrite", td.GetType(), rel)
}

func (m *Model) validateChildren(td *openfgav1.TypeDefinition, rel string, children []*openfgav1.Userset) error {
	for _, c := range children {
		if err := m.validateRewrite(td, rel, c); err != nil {
			return err
		}
	}
	return nil
}

// isThis: the rewrite is "[types]" (direct). Checked on the oneof wrapper: the
// DSL parser sets Userset_This with a nil DirectUserset, so GetThis() is nil.
func isThis(us *openfgav1.Userset) bool {
	_, ok := us.GetUserset().(*openfgav1.Userset_This)
	return ok
}

func hasThis(us *openfgav1.Userset) bool {
	switch {
	case isThis(us):
		return true
	case us.GetUnion() != nil:
		return anyThis(us.GetUnion().GetChild())
	case us.GetIntersection() != nil:
		return anyThis(us.GetIntersection().GetChild())
	case us.GetDifference() != nil:
		return hasThis(us.GetDifference().GetBase()) || hasThis(us.GetDifference().GetSubtract())
	}
	return false
}

func anyThis(children []*openfgav1.Userset) bool {
	for _, c := range children {
		if hasThis(c) {
			return true
		}
	}
	return false
}

func (m *Model) HasType(t string) bool { _, ok := m.types[t]; return ok }

func (m *Model) HasRelation(objType, rel string) bool {
	td, ok := m.types[objType]
	if !ok {
		return false
	}
	_, ok = td.GetRelations()[rel]
	return ok
}

func (m *Model) rewrite(objType, rel string) *openfgav1.Userset {
	return m.types[objType].GetRelations()[rel]
}

func (m *Model) directTypes(objType, rel string) []*openfgav1.RelationReference {
	return m.types[objType].GetMetadata().GetRelations()[rel].GetDirectlyRelatedUserTypes()
}

// Proto returns the underlying model (for storage).
func (m *Model) Proto() *openfgav1.AuthorizationModel { return m.proto }

// ValidateTuple checks that t can be stored: the relation is directly assignable
// on the object type and accepts this kind of subject (type, userset, wildcard).
func (m *Model) ValidateTuple(t Tuple) error {
	if !m.HasType(t.Object.Type) {
		return apperr.Invalid("object", fmt.Sprintf("unknown type %q", t.Object.Type))
	}
	if !m.HasRelation(t.Object.Type, t.Relation) {
		return apperr.Invalid("relation", fmt.Sprintf("type %q has no relation %q", t.Object.Type, t.Relation))
	}
	if t.TenantID == "" {
		return apperr.Invalid("tenant_id", "is required")
	}
	for _, ref := range m.directTypes(t.Object.Type, t.Relation) {
		if ref.GetType() != t.Subject.Type {
			continue
		}
		switch {
		case t.Subject.IsWildcard() && ref.GetWildcard() != nil:
			return nil
		case !t.Subject.IsWildcard() && ref.GetWildcard() == nil && ref.GetRelation() == t.Subject.Relation:
			return nil
		}
	}
	return apperr.Invalid("subject", fmt.Sprintf("%s#%s does not accept %s", t.Object.Type, t.Relation, t.Subject))
}

func invalidModel(format string, args ...any) error {
	return apperr.Invalid("dsl", fmt.Sprintf(format, args...))
}
