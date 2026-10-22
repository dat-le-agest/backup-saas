package exclause

import "gorm.io/gorm/clause"

// Set operations combine the statement's SELECT with other queries. Each
// takes a *gorm.DB, raw SQL with args, or a clause.Expression; prefix "ALL ?"
// to keep duplicates:
//
//	db.Table("members").Clauses(exclause.NewUnion("ALL ?", db.Table("admins")))
//
// Several of the same kind on one statement merge, in the order added.

// Union is the UNION clause.
type Union struct {
	Statements []clause.Expression
}

func NewUnion(query any, args ...any) Union {
	return Union{Statements: []clause.Expression{toExpr(query, args...)}}
}

func (Union) Name() string { return "UNION" }

func (u Union) Build(builder clause.Builder) { buildSetOp(builder, " UNION ", u.Statements) }

func (u Union) MergeClause(c *clause.Clause) {
	if prev, ok := c.Expression.(Union); ok {
		u.Statements = append(slicesClone(prev.Statements), u.Statements...)
	}
	c.Expression = u
}

// Intersect is the INTERSECT clause.
type Intersect struct {
	Statements []clause.Expression
}

func NewIntersect(query any, args ...any) Intersect {
	return Intersect{Statements: []clause.Expression{toExpr(query, args...)}}
}

func (Intersect) Name() string { return "INTERSECT" }

func (i Intersect) Build(builder clause.Builder) { buildSetOp(builder, " INTERSECT ", i.Statements) }

func (i Intersect) MergeClause(c *clause.Clause) {
	if prev, ok := c.Expression.(Intersect); ok {
		i.Statements = append(slicesClone(prev.Statements), i.Statements...)
	}
	c.Expression = i
}

// Except is the EXCEPT clause.
type Except struct {
	Statements []clause.Expression
}

func NewExcept(query any, args ...any) Except {
	return Except{Statements: []clause.Expression{toExpr(query, args...)}}
}

func (Except) Name() string { return "EXCEPT" }

func (e Except) Build(builder clause.Builder) { buildSetOp(builder, " EXCEPT ", e.Statements) }

func (e Except) MergeClause(c *clause.Clause) {
	if prev, ok := c.Expression.(Except); ok {
		e.Statements = append(slicesClone(prev.Statements), e.Statements...)
	}
	c.Expression = e
}

// buildSetOp writes the statements joined by sep; gorm writes the first keyword.
func buildSetOp(builder clause.Builder, sep string, statements []clause.Expression) {
	for i, s := range statements {
		if i > 0 {
			builder.WriteString(sep)
		}
		s.Build(builder)
	}
}
