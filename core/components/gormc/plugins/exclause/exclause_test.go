package exclause

import (
	"slices"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// dryDB builds Postgres SQL without a server: the dialector connects lazily and
// DryRun never executes.
func dryDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=localhost"}), &gorm.Config{
		DryRun:               true,
		DisableAutomaticPing: true,
		// writes would open a transaction, which connects
		SkipDefaultTransaction: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Use(New()); err != nil {
		t.Fatal(err)
	}
	return db
}

type user struct {
	ID   uint
	Name string
}

func assertSQL(t *testing.T, stmt *gorm.Statement, wantSQL string, wantVars ...any) {
	t.Helper()
	if got := stmt.SQL.String(); got != wantSQL {
		t.Errorf("SQL\n got: %s\nwant: %s", got, wantSQL)
	}
	if len(wantVars) == 0 {
		wantVars = nil
	}
	if !slices.Equal(stmt.Vars, wantVars) {
		t.Errorf("vars = %v, want %v", stmt.Vars, wantVars)
	}
}

func TestInitializeClauseOrder(t *testing.T) {
	db := dryDB(t)
	tests := []struct {
		name string
		got  []string
		want []string
	}{
		{"query", db.Callback().Query().Clauses,
			[]string{"WITH", "SELECT", "FROM", "WHERE", "GROUP BY", "UNION", "INTERSECT", "EXCEPT", "ORDER BY", "LIMIT", "FOR"}},
		{"row", db.Callback().Row().Clauses,
			[]string{"WITH", "SELECT", "FROM", "WHERE", "GROUP BY", "UNION", "INTERSECT", "EXCEPT", "ORDER BY", "LIMIT", "FOR"}},
		{"create", db.Callback().Create().Clauses, []string{"WITH", "INSERT", "VALUES", "ON CONFLICT", "RETURNING"}},
		{"update", db.Callback().Update().Clauses, []string{"WITH", "UPDATE", "SET", "FROM", "WHERE", "RETURNING"}},
		{"delete", db.Callback().Delete().Clauses, []string{"WITH", "DELETE", "FROM", "WHERE", "RETURNING"}},
	}
	for _, tt := range tests {
		if !slices.Equal(tt.got, tt.want) {
			t.Errorf("%s clauses = %v, want %v", tt.name, tt.got, tt.want)
		}
	}
}

func TestInsertBeforeIsIdempotentAndKeepsCustomOrder(t *testing.T) {
	order := []string{"FOO", "SELECT", "FROM", "BAR", "ORDER BY"}
	once := insertBefore(order, queryClauses)
	want := []string{"FOO", "WITH", "SELECT", "FROM", "BAR", "UNION", "INTERSECT", "EXCEPT", "ORDER BY"}
	if !slices.Equal(once, want) {
		t.Fatalf("got %v, want %v", once, want)
	}
	if twice := insertBefore(once, queryClauses); !slices.Equal(twice, want) {
		t.Errorf("second insert changed order: %v", twice)
	}
	// no anchor: nothing added
	if got := insertBefore([]string{"SELECT"}, []placement{{"UNION", "ORDER BY"}}); !slices.Equal(got, []string{"SELECT"}) {
		t.Errorf("got %v", got)
	}
}

func TestWith(t *testing.T) {
	db := dryDB(t)
	users := func() *gorm.DB { return db.Table("users") }

	tests := []struct {
		name     string
		query    func() *gorm.DB
		wantSQL  string
		wantVars []any
	}{
		{
			name: "raw SQL with args",
			query: func() *gorm.DB {
				return db.Clauses(NewWith("cte", "SELECT * FROM users WHERE name = ?", "ann")).Table("cte").Find(&[]user{})
			},
			wantSQL:  `WITH "cte" AS (SELECT * FROM users WHERE name = $1) SELECT * FROM "cte"`,
			wantVars: []any{"ann"},
		},
		{
			name: "gorm subquery",
			query: func() *gorm.DB {
				return db.Clauses(NewWith("cte", users().Where("name = ?", "ann"))).Table("cte").Find(&[]user{})
			},
			wantSQL:  `WITH "cte" AS (SELECT * FROM "users" WHERE name = $1) SELECT * FROM "cte"`,
			wantVars: []any{"ann"},
		},
		{
			name: "clause.Expression",
			query: func() *gorm.DB {
				return db.Clauses(NewWith("cte", clause.Expr{SQL: "SELECT 1"})).Table("cte").Find(&[]user{})
			},
			wantSQL: `WITH "cte" AS (SELECT 1) SELECT * FROM "cte"`,
		},
		{
			name: "columns",
			query: func() *gorm.DB {
				return db.Clauses(With{CTEs: []CTE{{Name: "cte", Columns: []string{"id", "name"}, Subquery: Subquery{DB: users()}}}}).Table("cte").Find(&[]user{})
			},
			wantSQL: `WITH "cte" ("id","name") AS (SELECT * FROM "users") SELECT * FROM "cte"`,
		},
		{
			name: "unquoted",
			query: func() *gorm.DB {
				return db.Clauses(With{CTEs: []CTE{{Name: "cte", Columns: []string{"id"}, Subquery: Subquery{DB: users()}, Unquoted: true}}}).Table("cte").Find(&[]user{})
			},
			wantSQL: `WITH cte (id) AS (SELECT * FROM "users") SELECT * FROM "cte"`,
		},
		{
			name: "merged: CTEs in order, RECURSIVE if any asks",
			query: func() *gorm.DB {
				return db.
					Clauses(With{CTEs: []CTE{NewCTE("a", users().Where("id = ?", 1))}}).
					Clauses(With{Recursive: true, CTEs: []CTE{NewCTE("b", users().Where("id = ?", 2))}}).
					Table("a").Find(&[]user{})
			},
			wantSQL:  `WITH RECURSIVE "a" AS (SELECT * FROM "users" WHERE id = $1),"b" AS (SELECT * FROM "users" WHERE id = $2) SELECT * FROM "a"`,
			wantVars: []any{1, 2},
		},
		{
			name: "materialization hints",
			query: func() *gorm.DB {
				return db.Clauses(With{CTEs: []CTE{
					NewMaterializedCTE("a", users()),
					NewNotMaterializedCTE("b", users()),
					NewCTE("c", users()),
				}}).Table("a").Find(&[]user{})
			},
			wantSQL: `WITH "a" AS MATERIALIZED (SELECT * FROM "users"),"b" AS NOT MATERIALIZED (SELECT * FROM "users"),"c" AS (SELECT * FROM "users") SELECT * FROM "a"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertSQL(t, tt.query().Statement, tt.wantSQL, tt.wantVars...)
		})
	}
}

func TestWithOnWrites(t *testing.T) {
	db := dryDB(t)
	stale := NewWith("stale", db.Table("users").Select("id").Where("name = ?", "old"))

	t.Run("update", func(t *testing.T) {
		stmt := db.Clauses(stale).Model(&user{}).Where("id IN (SELECT id FROM stale)").Update("name", "new").Statement
		assertSQL(t, stmt, `WITH "stale" AS (SELECT id FROM "users" WHERE name = $1) UPDATE "users" SET "name"=$2 WHERE id IN (SELECT id FROM stale)`, "old", "new")
	})
	t.Run("delete", func(t *testing.T) {
		stmt := db.Clauses(stale).Where("id IN (SELECT id FROM stale)").Delete(&user{}).Statement
		assertSQL(t, stmt, `WITH "stale" AS (SELECT id FROM "users" WHERE name = $1) DELETE FROM "users" WHERE id IN (SELECT id FROM stale)`, "old")
	})
}

func TestSetOperations(t *testing.T) {
	db := dryDB(t)
	members := func() *gorm.DB { return db.Table("members") }
	admins := func() *gorm.DB { return db.Table("admins").Where("active = ?", true) }

	tests := []struct {
		name     string
		query    func() *gorm.DB
		wantSQL  string
		wantVars []any
	}{
		{
			name:    "union raw SQL",
			query:   func() *gorm.DB { return members().Clauses(NewUnion("SELECT * FROM admins")).Find(&[]user{}) },
			wantSQL: `SELECT * FROM "members" UNION SELECT * FROM admins`,
		},
		{
			name: "union all with subquery, then order and limit the whole result",
			query: func() *gorm.DB {
				return members().Clauses(NewUnion("ALL ?", admins())).Order("id").Limit(10).Find(&[]user{})
			},
			wantSQL:  `SELECT * FROM "members" UNION ALL SELECT * FROM "admins" WHERE active = $1 ORDER BY id LIMIT $2`,
			wantVars: []any{true, 10},
		},
		{
			name: "unions merge in order",
			query: func() *gorm.DB {
				return members().Clauses(NewUnion(db.Table("a"))).Clauses(NewUnion(db.Table("b"))).Find(&[]user{})
			},
			wantSQL: `SELECT * FROM "members" UNION SELECT * FROM "a" UNION SELECT * FROM "b"`,
		},
		{
			name:     "intersect",
			query:    func() *gorm.DB { return members().Clauses(NewIntersect(admins())).Find(&[]user{}) },
			wantSQL:  `SELECT * FROM "members" INTERSECT SELECT * FROM "admins" WHERE active = $1`,
			wantVars: []any{true},
		},
		{
			name:     "except all",
			query:    func() *gorm.DB { return members().Clauses(NewExcept("ALL ?", admins())).Find(&[]user{}) },
			wantSQL:  `SELECT * FROM "members" EXCEPT ALL SELECT * FROM "admins" WHERE active = $1`,
			wantVars: []any{true},
		},
		{
			name: "CTE feeding a union",
			query: func() *gorm.DB {
				return db.Clauses(NewWith("recent", members().Where("id > ?", 100))).
					Table("recent").Clauses(NewUnion(admins())).Find(&[]user{})
			},
			wantSQL:  `WITH "recent" AS (SELECT * FROM "members" WHERE id > $1) SELECT * FROM "recent" UNION SELECT * FROM "admins" WHERE active = $2`,
			wantVars: []any{100, true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertSQL(t, tt.query().Statement, tt.wantSQL, tt.wantVars...)
		})
	}
}

func TestMergeDoesNotShareBackingArray(t *testing.T) {
	db := dryDB(t)
	base := db.Table("members").Clauses(NewUnion(db.Table("a")))
	first := base.Session(&gorm.Session{}).Clauses(NewUnion(db.Table("b"))).Find(&[]user{}).Statement.SQL.String()
	second := base.Session(&gorm.Session{}).Clauses(NewUnion(db.Table("c"))).Find(&[]user{}).Statement.SQL.String()

	if want := `SELECT * FROM "members" UNION SELECT * FROM "a" UNION SELECT * FROM "b"`; first != want {
		t.Errorf("first = %s", first)
	}
	if want := `SELECT * FROM "members" UNION SELECT * FROM "a" UNION SELECT * FROM "c"`; second != want {
		t.Errorf("second = %s", second)
	}
}

func TestUnsupportedQueryTypePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected a panic")
		}
	}()
	NewUnion(42)
}
