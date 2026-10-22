// Package migrations embeds this service's schema (goose SQL migrations),
// applied at startup by gormc according to --db-migrate.
//
// New migration: make migration svc=<service> name=add_something
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
