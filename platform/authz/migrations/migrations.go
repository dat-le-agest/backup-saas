// Package migrations embeds the authz store (goose SQL migrations). Every app
// has its own store: AppFS is applied to each app's schema at startup
// (core/migrate.UpInSchema). There are no shared tables.
//
// New migration: add a numbered file to app/ (every app's schema gets it on
// the next start).
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed app/*.sql
var appFiles embed.FS

// AppFS holds the migrations of one app's store.
var AppFS, _ = fs.Sub(appFiles, "app")
