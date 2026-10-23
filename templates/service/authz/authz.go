// Package authz embeds the service's authorization model (authz.yml),
// composed with the base model of core/authz/rebac. It decides in both modes:
// RBAC (relationships implied by the request) and ReBAC (stored ones too).
package authz

import "embed"

//go:embed *.yml
var FS embed.FS
