package openapi

import "embed"

// FS embeds all OpenAPI specifications.
//
//go:embed apps/*
var FS embed.FS
