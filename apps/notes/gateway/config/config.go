package config

const ServiceName = "notes-gateway"

// App is the id of the notes app: the audience of its access tokens and the
// namespace of its cache keys.
const App = "notes"

// Component IDs registered in sctx. Also used as logger prefixes.
const (
	ComponentOtel        = "otel"
	ComponentDB          = "db"
	ComponentGin         = "gin"
	ComponentAdminGin    = "admin-gin"
	ComponentJWT         = "jwt"
	ComponentInternalJWT = "internal-jwt"
	ComponentMail        = "mail"
	ComponentRedis       = "redis"
	ComponentNoteClient  = "note-client"
)

// Flag prefixes for upstream services: <prefix>-url / <PREFIX>_URL.
const (
	UpstreamNote = "note-service"
)

// AdminFlagPrefix: the internal admin server's flags (--admin-gin-port / ADMIN_GIN_PORT).
const AdminFlagPrefix = "admin"

// InternalJWTFlagPrefix: the signer of internal tokens, the proof of the
// gateway's calls to its services (--internal-jwt-keys-dir / INTERNAL_JWT_KEYS_DIR).
// Its own keys, not the access tokens' ones.
const InternalJWTFlagPrefix = "internal-jwt"

// OutboxName lists the gateway's own outbox (account mail) next to its
// services' in the admin API (/admin/v1/outbox/gateway/*).
const OutboxName = "gateway"

// InternalJWKSPath serves the internal tokens' public keys on the internal
// admin server, for the services (their INTERNAL_JWT_JWKS_URL).
const InternalJWKSPath = "/.well-known/internal-jwks.json"
