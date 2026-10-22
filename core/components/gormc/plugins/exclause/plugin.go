// Package exclause adds SQL clauses gorm doesn't build on its own:
// WITH (CTE, RECURSIVE, MATERIALIZED), UNION, INTERSECT and EXCEPT.
//
//	db.Use(exclause.New()) // gormc.Open already does
//
//	// WITH "cte" AS (SELECT * FROM "users" WHERE name = $1) SELECT * FROM "cte"
//	db.Clauses(exclause.NewWith("cte", db.Table("users").Where("name = ?", "ann"))).Table("cte").Scan(&users)
//
//	// SELECT * FROM "members" UNION ALL SELECT * FROM "admins"
//	db.Table("members").Clauses(exclause.NewUnion("ALL ?", db.Table("admins"))).Scan(&users)
//
// Ported from github.com/WinterYukky/gorm-extra-clause-plugin (MIT, © 2021 WinterYukky).
package exclause

import (
	"slices"

	"gorm.io/gorm"
)

// Plugin registers the clauses in gorm's build order. Without it gorm ignores
// them: a statement only builds the clause names its callback lists.
type Plugin struct{}

func New() *Plugin { return &Plugin{} }

func (*Plugin) Name() string { return "exclause" }

func (*Plugin) Initialize(db *gorm.DB) error {
	cb := db.Callback()
	// a CTE may precede any statement (Postgres: SELECT, INSERT, UPDATE, DELETE)
	cb.Query().Clauses = insertBefore(cb.Query().Clauses, queryClauses)
	cb.Row().Clauses = insertBefore(cb.Row().Clauses, queryClauses)
	cb.Create().Clauses = insertBefore(cb.Create().Clauses, []placement{{"WITH", "INSERT"}})
	cb.Update().Clauses = insertBefore(cb.Update().Clauses, []placement{{"WITH", "UPDATE"}})
	cb.Delete().Clauses = insertBefore(cb.Delete().Clauses, []placement{{"WITH", "DELETE"}})
	return nil
}

// placement: clause name goes right before the clause named before.
type placement struct{ name, before string }

// set operations apply to the whole SELECT, so ORDER BY / LIMIT come after them
var queryClauses = []placement{
	{"WITH", "SELECT"},
	{"UNION", "ORDER BY"},
	{"INTERSECT", "ORDER BY"},
	{"EXCEPT", "ORDER BY"},
}

// insertBefore returns order with each placement added before its anchor.
// Names already in order are left where they are (Initialize twice is a no-op);
// placements whose anchor is missing are skipped.
func insertBefore(order []string, ps []placement) []string {
	out := make([]string, 0, len(order)+len(ps))
	for _, name := range order {
		for _, p := range ps {
			if p.before == name && !slices.Contains(order, p.name) {
				out = append(out, p.name)
			}
		}
		out = append(out, name)
	}
	return out
}
