package exclause

import (
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Subquery embeds a gorm query, with its bound values, in a clause.
type Subquery struct {
	DB *gorm.DB
}

func (s Subquery) Build(builder clause.Builder) {
	builder.AddVar(builder, s.DB)
}

// toExpr accepts what the New* helpers take: a *gorm.DB, raw SQL with its
// args ("SELECT ... WHERE id = ?", 1 or "ALL ?", db.Table(...)), or any
// clause.Expression. Anything else is a programming error.
func toExpr(query any, args ...any) clause.Expression {
	switch v := query.(type) {
	case *gorm.DB:
		return Subquery{DB: v}
	case string:
		return clause.Expr{SQL: v, Vars: args}
	case clause.Expression:
		return v
	}
	panic(fmt.Sprintf("exclause: unsupported query type %T (want *gorm.DB, string or clause.Expression)", query))
}
