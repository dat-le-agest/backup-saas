package config

const ServiceName = "template"

// Component IDs registered in sctx. Also used as logger prefixes.
const (
	ComponentOtel        = "otel"
	ComponentDB          = "db"
	ComponentAuthzClient = "authz-client"
	ComponentAuthz       = "authz"
	ComponentGin         = "gin"
)

// Flag prefixes for upstream services: <prefix>-url / <PREFIX>_URL.
const (
	// optional: set AUTHZ_SERVICE_URL to authorize through the central authz
	// service (ReBAC); without it the service uses its RBAC policy
	UpstreamAuthz = "authz-service"
)
