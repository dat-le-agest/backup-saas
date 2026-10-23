// Package migrations embeds the admin service's schema (goose SQL migrations),
// applied at startup by gormc according to --db-migrate.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
