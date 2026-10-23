// Package migrations embeds the authn schema (goose SQL migrations), applied
// by the app that embeds core/authn (gormc.WithMigrations).
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
