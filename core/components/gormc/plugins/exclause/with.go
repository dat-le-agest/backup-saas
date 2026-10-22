package exclause

import "gorm.io/gorm/clause"

// Materialization is the planner hint of a CTE (Postgres 12+).
type Materialization int

const (
	// MaterializeDefault: no hint, the database decides.
	MaterializeDefault Materialization = iota
	// Materialized: AS MATERIALIZED, the CTE is computed once.
	Materialized
	// NotMaterialized: AS NOT MATERIALIZED, the CTE may be inlined into the query.
	NotMaterialized
)

// With is the WITH clause. Several With on one statement merge into one
// clause: CTEs in the order added, RECURSIVE if any of them asks for it.
//
//	// WITH RECURSIVE "tree" ("id","parent_id") AS (...) SELECT * FROM "tree"
//	db.Clauses(exclause.With{Recursive: true, CTEs: []exclause.CTE{
//		{Name: "tree", Columns: []string{"id", "parent_id"}, Subquery: exclause.Subquery{DB: base}},
//	}}).Table("tree").Scan(&rows)
type With struct {
	Recursive bool
	CTEs      []CTE
}

// CTE is one named subquery of a WITH clause.
type CTE struct {
	Name         string
	Columns      []string
	Subquery     clause.Expression
	Materialized Materialization
	// Unquoted writes Name and Columns as given (e.g. already quoted or qualified).
	Unquoted bool
}

// NewWith is a WITH clause of a single CTE. query is a *gorm.DB, raw SQL with
// args, or a clause.Expression.
func NewWith(name string, query any, args ...any) With {
	return With{CTEs: []CTE{NewCTE(name, query, args...)}}
}

func NewCTE(name string, query any, args ...any) CTE {
	return CTE{Name: name, Subquery: toExpr(query, args...)}
}

func NewMaterializedCTE(name string, query any, args ...any) CTE {
	cte := NewCTE(name, query, args...)
	cte.Materialized = Materialized
	return cte
}

func NewNotMaterializedCTE(name string, query any, args ...any) CTE {
	cte := NewCTE(name, query, args...)
	cte.Materialized = NotMaterialized
	return cte
}

func (With) Name() string { return "WITH" }

func (w With) Build(builder clause.Builder) {
	if w.Recursive {
		builder.WriteString("RECURSIVE ")
	}
	for i, cte := range w.CTEs {
		if i > 0 {
			builder.WriteByte(',')
		}
		cte.Build(builder)
	}
}

func (w With) MergeClause(c *clause.Clause) {
	if prev, ok := c.Expression.(With); ok {
		w.Recursive = w.Recursive || prev.Recursive
		w.CTEs = append(slicesClone(prev.CTEs), w.CTEs...)
	}
	c.Expression = w
}

func (cte CTE) Build(builder clause.Builder) {
	cte.writeName(builder, cte.Name)
	if len(cte.Columns) > 0 {
		builder.WriteString(" (")
		for i, column := range cte.Columns {
			if i > 0 {
				builder.WriteByte(',')
			}
			cte.writeName(builder, column)
		}
		builder.WriteByte(')')
	}

	builder.WriteString(" AS ")
	switch cte.Materialized {
	case Materialized:
		builder.WriteString("MATERIALIZED ")
	case NotMaterialized:
		builder.WriteString("NOT MATERIALIZED ")
	}

	builder.WriteByte('(')
	cte.Subquery.Build(builder)
	builder.WriteByte(')')
}

func (cte CTE) writeName(builder clause.Builder, name string) {
	if cte.Unquoted {
		builder.WriteString(name)
	} else {
		builder.WriteQuoted(name)
	}
}

// slicesClone copies s so merging never writes into the backing array of a
// clause another statement still holds.
func slicesClone[T any](s []T) []T {
	return append(make([]T, 0, len(s)), s...)
}
