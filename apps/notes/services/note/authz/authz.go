// Package authz embeds the service's authorization model: OpenFGA module
// files (*.fga), composed with the base module of core/authz/rebac and loaded
// by the embedded engine at startup.
package authz

import "embed"

//go:embed *.fga
var FS embed.FS
