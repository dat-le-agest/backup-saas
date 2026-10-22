package config

const ServiceName = "notes-gateway"

// Component IDs registered in sctx. Also used as logger prefixes.
const (
	ComponentOtel       = "otel"
	ComponentGin        = "gin"
	ComponentJWT        = "jwt"
	ComponentRedis      = "redis"
	ComponentNoteClient = "note-client"
	ComponentIamClient  = "iam-client"
)

// Flag prefixes for upstream services: <prefix>-url / <PREFIX>_URL.
const (
	UpstreamNote = "note-service"
	// iam: current membership versions when Redis doesn't have them
	UpstreamIam = "iam-service"
)
