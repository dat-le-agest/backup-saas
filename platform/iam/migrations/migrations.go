// Package migrations embeds the iam schema (goose SQL migrations).
//
//	FS     shared tables (apps, users, login throttles, outbox), default schema;
//	       applied at startup by gormc according to --db-migrate
//	AppFS  tables of one app (tenants, memberships, refresh tokens), applied to
//	       each app's schema at startup (core/migrate.UpInSchema)
//
// New migration: make migration svc=iam name=add_something (shared), or add a
// numbered file to app/ (every app's schema gets it on the next start).
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed *.sql
var FS embed.FS

//go:embed app/*.sql
var appFiles embed.FS

// AppFS holds the migrations of one app's schema.
var AppFS, _ = fs.Sub(appFiles, "app")
