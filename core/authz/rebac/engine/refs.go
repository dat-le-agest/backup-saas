// Package engine evaluates relationship-based authorization (Zanzibar
// style) over relation tuples: Check and ListObjects against an authorization
// model, plus validation of models and tuples. It does no I/O itself: tuples are
// read through TupleReader.
package engine

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"core/apperr"
)

// Object is "type:id".
type Object struct {
	Type string
	ID   string
}

func (o Object) String() string { return o.Type + ":" + o.ID }

// Subject is "type:id" (Relation empty), a userset "type:id#relation", or the
// wildcard "type:*" (only stored in tuples, meaning every subject of the type).
type Subject struct {
	Type     string
	ID       string
	Relation string
}

func (s Subject) String() string {
	if s.Relation == "" {
		return s.Type + ":" + s.ID
	}
	return s.Type + ":" + s.ID + "#" + s.Relation
}

func (s Subject) IsWildcard() bool { return s.ID == "*" }

// Object returns the object part of a userset subject (team:eng of team:eng#member).
func (s Subject) Object() Object { return Object{Type: s.Type, ID: s.ID} }

// Tuple is object#relation@subject, owned by TenantID.
type Tuple struct {
	Object   Object
	Relation string
	Subject  Subject
	TenantID string
}

func (t Tuple) String() string {
	return t.Object.String() + "#" + t.Relation + "@" + t.Subject.String()
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func validName(s string) bool { return namePattern.MatchString(s) }

// validID matches the database constraint: 1-256 chars, no whitespace or '#'.
// An id may contain ':' (only the first one separates type and id).
func validID(s string) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	for _, r := range s {
		if unicode.IsSpace(r) || r == '#' {
			return false
		}
	}
	return true
}

// ParseObject parses "type:id". field names the input in validation errors.
func ParseObject(field, s string) (Object, error) {
	typ, id, ok := strings.Cut(s, ":")
	if !ok || !validName(typ) || !validID(id) || id == "*" {
		return Object{}, apperr.Invalid(field, fmt.Sprintf("must be type:id, got %q", s))
	}
	return Object{Type: typ, ID: id}, nil
}

// ParseSubject parses "type:id", "type:id#relation" or "type:*".
func ParseSubject(field, s string) (Subject, error) {
	ref, rel, hasRel := strings.Cut(s, "#")
	typ, id, ok := strings.Cut(ref, ":")
	if !ok || !validName(typ) || !validID(id) || (hasRel && !validName(rel)) || (id == "*" && hasRel) {
		return Subject{}, apperr.Invalid(field, fmt.Sprintf("must be type:id, type:id#relation or type:*, got %q", s))
	}
	return Subject{Type: typ, ID: id, Relation: rel}, nil
}

// ValidateRelation checks the syntax of a relation name.
func ValidateRelation(field, rel string) error {
	if !validName(rel) {
		return apperr.Invalid(field, fmt.Sprintf("invalid relation %q", rel))
	}
	return nil
}
