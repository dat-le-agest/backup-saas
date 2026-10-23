// Package migrations embeds the tables of core/authz/rebac (models, tuples,
// changelog, revision), applied by rebac.New into the service's "authz" schema
// (core/migrate.UpInSchema): trigger functions are pinned to that schema.
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed app/*.sql
var files embed.FS

// FS holds the migrations of the authz schema.
var FS, _ = fs.Sub(files, "app")
