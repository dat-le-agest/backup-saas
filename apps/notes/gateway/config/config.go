package config

const ServiceName = "notes-gateway"

// App is the id of the notes app: the audience of its access tokens and the
// namespace of its cache keys.
const App = "notes"

// Component IDs registered in sctx. Also used as logger prefixes.
const (
	ComponentOtel       = "otel"
	ComponentDB         = "db"
	ComponentGin        = "gin"
	ComponentAdminGin   = "admin-gin"
	ComponentJWT        = "jwt"
	ComponentRedis      = "redis"
	ComponentNoteClient = "note-client"
)

// Flag prefixes for upstream services: <prefix>-url / <PREFIX>_URL.
const (
	UpstreamNote = "note-service"
)

// AdminFlagPrefix: the internal admin server's flags (--admin-gin-port / ADMIN_GIN_PORT).
const AdminFlagPrefix = "admin"
