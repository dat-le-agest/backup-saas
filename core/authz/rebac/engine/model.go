package engine

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"core/apperr"

	"go.yaml.in/yaml/v3"
)

// An authorization model is written in YAML files: a file defines types and
// their relations, and may extend types of other files with more relations.
//
//	name: notes                  # file (module) name
//	extend:                      # relations added to types of other files
//	  tenant:
//	    relations:
//	      can_create_note: member
//	types:
//	  note:
//	    relations:
//	      tenant: {assignable: [tenant]}
//	      owner: {assignable: [user]}
//	      editor:
//	        union:
//	          - assignable: [user, "team#member"]
//	          - owner
//	          - from: {relation: admin, via: tenant}
//	      can_view:
//	        exclude: {base: viewer, subtract: {from: {relation: suspended, via: tenant}}}
//
// A relation is a Rule. A plain string is a Rule computing another relation of
// the same object ("can_read: viewer").

// Rule decides who has a relation. Exactly one field is set.
type Rule struct {
	// Assignable: subjects stored directly in tuples: "user" (a user), "user:*"
	// (every user, wildcard), "team#member" (the members of a team).
	Assignable []string `json:"assignable,omitempty"`
	// Computed: another relation of the same object.
	Computed string `json:"computed,omitempty"`
	// From: Relation on the objects stored in the Via relation ("admin from tenant").
	From *From `json:"from,omitempty"`
	// Union: any of the rules.
	Union []Rule `json:"union,omitempty"`
	// Intersection: all of the rules.
	Intersection []Rule `json:"intersection,omitempty"`
	// Exclude: Base but not Subtract.
	Exclude *Exclude `json:"exclude,omitempty"`
}

type From struct {
	Relation string `json:"relation" yaml:"relation"`
	Via      string `json:"via" yaml:"via"`
}

type Exclude struct {
	Base     Rule `json:"base" yaml:"base"`
	Subtract Rule `json:"subtract" yaml:"subtract"`
}

// UnmarshalYAML: a string computes a relation; a mapping has exactly one of the
// rule keys (unknown keys are errors, not silently ignored).
func (r *Rule) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		r.Computed = n.Value
		return nil
	}
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: a rule is a relation name or a mapping", n.Line)
	}
	allowed := map[string]bool{"assignable": true, "computed": true, "from": true, "union": true, "intersection": true, "exclude": true}
	for i := 0; i < len(n.Content); i += 2 {
		if k := n.Content[i].Value; !allowed[k] {
			return fmt.Errorf("line %d: unknown rule %q (assignable, computed, from, union, intersection, exclude)", n.Content[i].Line, k)
		}
	}
	type plain struct {
		Assignable   []string `yaml:"assignable"`
		Computed     string   `yaml:"computed"`
		From         *From    `yaml:"from"`
		Union        []Rule   `yaml:"union"`
		Intersection []Rule   `yaml:"intersection"`
		Exclude      *Exclude `yaml:"exclude"`
	}
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	*r = Rule(p)
	return nil
}

func (r *Rule) kinds() int {
	n := 0
	for _, set := range []bool{r.Assignable != nil, r.Computed != "", r.From != nil, r.Union != nil, r.Intersection != nil, r.Exclude != nil} {
		if set {
			n++
		}
	}
	return n
}

// TypeDef is a type and its relations.
type TypeDef struct {
	Relations map[string]Rule `json:"relations,omitempty" yaml:"relations"`
}

// File is one YAML file of a model.
type File struct {
	Name   string             `yaml:"name"`
	Types  map[string]TypeDef `yaml:"types"`
	Extend map[string]TypeDef `yaml:"extend"`
}

// ModuleFile is a model file as read from disk: its name and YAML content.
type ModuleFile struct {
	Name     string
	Contents string
}

// Model is a validated authorization model, indexed for evaluation.
type Model struct {
	ID    string
	types map[string]TypeDef
}

// SchemaVersion of the stored models (the compiled JSON format).
const SchemaVersion = "yaml/1"

// ParseModules composes and validates a model from its files (e.g. the base
// file and an app's): every file's types first, then every file's extend. It
// also returns the source: the files in name order, each preceded by its name,
// the same for the same files (to tell whether a model changed).
func ParseModules(files []ModuleFile) (*Model, string, error) {
	if len(files) == 0 {
		return nil, "", invalidModel("no model files")
	}
	sorted := append([]ModuleFile(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	var source strings.Builder
	parsed := make([]File, len(sorted))
	for i, f := range sorted {
		fmt.Fprintf(&source, "# file: %s\n%s\n", f.Name, strings.TrimRight(f.Contents, "\n"))
		dec := yaml.NewDecoder(strings.NewReader(f.Contents))
		dec.KnownFields(true)
		if err := dec.Decode(&parsed[i]); err != nil {
			return nil, "", invalidModel("%s: %v", f.Name, err)
		}
		if !validName(parsed[i].Name) {
			return nil, "", invalidModel("%s: name %q must be lowercase letters, digits and _", f.Name, parsed[i].Name)
		}
	}

	types := map[string]TypeDef{}
	owner := map[string]string{} // type → file that defines it
	for i, f := range parsed {
		for name, td := range f.Types {
			if prev, dup := owner[name]; dup {
				return nil, "", invalidModel("type %q defined in %s and %s", name, prev, sorted[i].Name)
			}
			owner[name] = sorted[i].Name
			types[name] = TypeDef{Relations: copyRelations(td.Relations)}
		}
	}
	for i, f := range parsed {
		for name, ext := range f.Extend {
			td, ok := types[name]
			if !ok {
				return nil, "", invalidModel("%s: extends unknown type %q", sorted[i].Name, name)
			}
			for rel, rule := range ext.Relations {
				if _, dup := td.Relations[rel]; dup {
					return nil, "", invalidModel("%s: %s#%s is already defined", sorted[i].Name, name, rel)
				}
				td.Relations[rel] = rule
			}
		}
	}
	m, err := NewModel("", types)
	if err != nil {
		return nil, "", err
	}
	return m, source.String(), nil
}

// ParseYAML parses a model written as one complete file.
func ParseYAML(src string) (*Model, error) {
	m, _, err := ParseModules([]ModuleFile{{Name: "model.yml", Contents: src}})
	return m, err
}

func copyRelations(in map[string]Rule) map[string]Rule {
	out := make(map[string]Rule, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// MarshalJSON stores the model's types (the compiled form).
func (m *Model) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Types map[string]TypeDef `json:"types"`
	}{m.types})
}

// ModelFromJSON loads a stored model and validates it again.
func ModelFromJSON(id string, raw []byte) (*Model, error) {
	var stored struct {
		Types map[string]TypeDef `json:"types"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, fmt.Errorf("decode authorization model %s: %w", id, err)
	}
	return NewModel(id, stored.Types)
}

// NewModel validates types: names, every referenced type and relation exists,
// each rule has exactly one kind, and the Via of a "from" is a relation that
// only holds objects.
func NewModel(id string, types map[string]TypeDef) (*Model, error) {
	m := &Model{ID: id, types: types}
	names := make([]string, 0, len(types))
	for name := range types {
		names = append(names, name)
	}
	sort.Strings(names) // stable error messages
	for _, name := range names {
		if !validName(name) {
			return nil, invalidModel("invalid type name %q", name)
		}
		rels := make([]string, 0, len(types[name].Relations))
		for rel := range types[name].Relations {
			rels = append(rels, rel)
		}
		sort.Strings(rels)
		for _, rel := range rels {
			if !validName(rel) {
				return nil, invalidModel("invalid relation name %s#%s", name, rel)
			}
			rule := types[name].Relations[rel]
			if err := m.validateRule(name, rel, &rule); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

func (m *Model) validateRule(typ, rel string, r *Rule) error {
	at := typ + "#" + rel
	if r.kinds() != 1 {
		return invalidModel("%s: a rule has exactly one of assignable, computed, from, union, intersection, exclude", at)
	}
	switch {
	case r.Assignable != nil:
		if len(r.Assignable) == 0 {
			return invalidModel("%s: assignable needs at least one subject type", at)
		}
		for _, s := range r.Assignable {
			ref, err := parseSubjectType(s)
			if err != nil {
				return invalidModel("%s: %v", at, err)
			}
			if !m.HasType(ref.Type) {
				return invalidModel("%s: unknown type %q", at, ref.Type)
			}
			if ref.Relation != "" && !m.HasRelation(ref.Type, ref.Relation) {
				return invalidModel("%s: unknown relation %s#%s", at, ref.Type, ref.Relation)
			}
		}
	case r.Computed != "":
		if !m.HasRelation(typ, r.Computed) {
			return invalidModel("%s: unknown relation %q", at, r.Computed)
		}
	case r.From != nil:
		via, target := r.From.Via, r.From.Relation
		if !m.HasRelation(typ, via) {
			return invalidModel("%s: unknown relation %q in from.via", at, via)
		}
		// the via relation must hold plain objects: "admin from tenant" needs tenant: {assignable: [tenant]}
		vr := m.types[typ].Relations[via]
		if vr.Assignable == nil {
			return invalidModel("%s: from.via %q must be an assignable relation", at, via)
		}
		found := false
		for _, s := range vr.Assignable {
			ref, _ := parseSubjectType(s)
			if ref.Relation != "" || ref.Wildcard {
				return invalidModel("%s: from.via %q may only hold objects, not usersets or wildcards", at, via)
			}
			if m.HasRelation(ref.Type, target) {
				found = true
			}
		}
		if !found {
			return invalidModel("%s: no type in %q defines relation %q", at, via, target)
		}
	case r.Union != nil, r.Intersection != nil:
		children := r.Union
		if r.Intersection != nil {
			children = r.Intersection
		}
		if len(children) < 2 {
			return invalidModel("%s: union and intersection need at least two rules", at)
		}
		for i := range children {
			if err := m.validateRule(typ, rel, &children[i]); err != nil {
				return err
			}
		}
	case r.Exclude != nil:
		if err := m.validateRule(typ, rel, &r.Exclude.Base); err != nil {
			return err
		}
		return m.validateRule(typ, rel, &r.Exclude.Subtract)
	}
	return nil
}

// SubjectType is an entry of an assignable list: "user", "user:*", "team#member".
type SubjectType struct {
	Type     string
	Relation string
	Wildcard bool
}

func parseSubjectType(s string) (SubjectType, error) {
	switch {
	case strings.HasSuffix(s, ":*"):
		t := strings.TrimSuffix(s, ":*")
		if !validName(t) {
			return SubjectType{}, fmt.Errorf("invalid subject type %q", s)
		}
		return SubjectType{Type: t, Wildcard: true}, nil
	case strings.Contains(s, "#"):
		t, rel, _ := strings.Cut(s, "#")
		if !validName(t) || !validName(rel) {
			return SubjectType{}, fmt.Errorf("invalid subject type %q", s)
		}
		return SubjectType{Type: t, Relation: rel}, nil
	}
	if !validName(s) {
		return SubjectType{}, fmt.Errorf("invalid subject type %q", s)
	}
	return SubjectType{Type: s}, nil
}

func (m *Model) HasType(t string) bool { _, ok := m.types[t]; return ok }

func (m *Model) HasRelation(objType, rel string) bool {
	td, ok := m.types[objType]
	if !ok {
		return false
	}
	_, ok = td.Relations[rel]
	return ok
}

func (m *Model) rule(objType, rel string) *Rule {
	r := m.types[objType].Relations[rel]
	return &r
}

// directTypes: every subject type assignable to objType#rel, anywhere in its rule.
func (m *Model) directTypes(objType, rel string) []SubjectType {
	var out []SubjectType
	var walk func(r *Rule)
	walk = func(r *Rule) {
		for _, s := range r.Assignable {
			if ref, err := parseSubjectType(s); err == nil {
				out = append(out, ref)
			}
		}
		for i := range r.Union {
			walk(&r.Union[i])
		}
		for i := range r.Intersection {
			walk(&r.Intersection[i])
		}
		if r.Exclude != nil {
			walk(&r.Exclude.Base)
			walk(&r.Exclude.Subtract)
		}
	}
	walk(m.rule(objType, rel))
	return out
}

// ValidateTuple checks that t can be stored: the relation is assignable on the
// object type and accepts this kind of subject (type, userset, wildcard).
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
		if ref.Type != t.Subject.Type {
			continue
		}
		switch {
		case t.Subject.IsWildcard() && ref.Wildcard:
			return nil
		case !t.Subject.IsWildcard() && !ref.Wildcard && ref.Relation == t.Subject.Relation:
			return nil
		}
	}
	return apperr.Invalid("subject", fmt.Sprintf("%s#%s does not accept %s", t.Object.Type, t.Relation, t.Subject))
}

func invalidModel(format string, args ...any) error {
	return apperr.Invalid("model", fmt.Sprintf(format, args...))
}
