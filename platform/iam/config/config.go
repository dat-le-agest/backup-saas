package config

const ServiceName = "iam"

// Component IDs registered in sctx. Also used as logger prefixes.
const (
	ComponentOtel        = "otel"
	ComponentDB          = "db"
	ComponentRedis       = "redis"
	ComponentJWT         = "jwt"
	ComponentAuthzClient = "authz-client"
	ComponentGin         = "gin"
)

// Flag prefixes for upstream services: <prefix>-url / <PREFIX>_URL.
const (
	// optional: memberships are synced to the central authz service when set
	UpstreamAuthz = "authz-service"
)
