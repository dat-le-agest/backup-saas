package config

const ServiceName = "gateway"

// Component IDs registered in sctx. Also used as logger prefixes.
const (
	ComponentOtel      = "otel"
	ComponentGin       = "gin"
	ComponentJWT       = "jwt"
	ComponentRedis     = "redis"
	ComponentIamClient = "iam-client"
)

// Flag prefixes for upstream services: <prefix>-url / <PREFIX>_URL.
const (
	UpstreamIam = "iam-service"
)
