package config

const ServiceName = "template"

// Component IDs registered in sctx. Also used as logger prefixes.
const (
	ComponentOtel  = "otel"
	ComponentDB    = "db"
	ComponentAuthz = "authz"
	ComponentGin   = "gin"
	// how callers (the app's gateway) are authenticated: core/components/internalauthc
	ComponentInternalAuth = "internal-auth"
)
