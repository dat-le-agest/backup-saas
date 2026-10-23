package config

const ServiceName = "admin"

// SessionAudience: the audience of admin session tokens (never an app's).
const SessionAudience = "admin"

// Component IDs registered in sctx. Also used as logger prefixes.
const (
	ComponentOtel = "otel"
	ComponentDB   = "db"
	ComponentJWT  = "jwt"
	ComponentGin  = "gin"
)
