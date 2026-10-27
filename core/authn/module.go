package authn

import (
	"errors"
	"time"

	"core/authn/httpapi"
	"core/authn/password"
	"core/authn/repository"
	"core/authn/service"
	"core/authz/rebac"
	"core/logger"
	"core/mail"
	"core/token"
	"core/uow"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// Config of an app's authentication. The database must have the authn
// migrations applied (migrations.FS).
type Config struct {
	// App id: the audience of the tokens (an app's gateway accepts only its own)
	// and the namespace of its cache keys, e.g. "notes".
	App    string
	DB     *gorm.DB
	Signer *token.Signer // jwtc.NewSigner component
	// Redis caches membership versions; nil reads the database on every
	// tenant-scoped request.
	Redis *redis.Client
	// Hasher of passwords; nil uses password.DefaultParams.
	Hasher     service.PasswordHasher
	RefreshTTL time.Duration // default service.DefaultRefreshTTL
	// DevLogin enables POST /v1/auth/dev-login (tokens without credentials). Dev only.
	DevLogin bool
	// AdminKey protects the admin API (shared with the admin service); empty disables it.
	AdminKey string
	// Mailer sends account mail (email verification, password reset,
	// invitations): a mail.Queue on an outbox in DB, so mail is stored with the
	// change it is about and delivered by the app's relay. nil: no account mail.
	Mailer mail.Sender
	Logger logger.Logger
}

// Module is an app's authentication, wired.
type Module struct {
	Auth    *service.AuthService
	Tokens  *service.TokenService
	Tenants *service.TenantService
	Members *service.MemberService
	Admin   *service.AdminService

	// Authenticator verifies the app's access tokens and membership versions
	// (for httpx.Auth).
	Authenticator *JWTAuthenticator
	// HTTP: public endpoints and the admin API.
	HTTP      *httpapi.Handler
	AdminHTTP *httpapi.AdminHandler

	// Mailer of the account mail (Config.Mailer), nil without one.
	Mailer mail.Sender
}

// cache entries must outlive the access tokens they protect
const versionTTL = time.Hour

// New wires an app's authentication.
func New(cfg Config) (*Module, error) {
	switch {
	case cfg.App == "":
		return nil, errors.New("authn: App is required")
	case cfg.DB == nil:
		return nil, errors.New("authn: DB is required")
	case cfg.Signer == nil:
		return nil, errors.New("authn: Signer is required")
	case cfg.Logger == nil:
		return nil, errors.New("authn: Logger is required")
	}
	if cfg.Hasher == nil {
		cfg.Hasher = password.NewHasher(password.DefaultParams, 0)
	}

	u := uow.New(cfg.DB)
	store := repository.NewStore(u)
	cache := repository.NewVersionCache(cfg.Redis, cfg.App, versionTTL)

	tokens := service.NewTokenService(store, u, cfg.Signer, service.TokenConfig{
		App: cfg.App, RefreshTTL: cfg.RefreshTTL, DevLogin: cfg.DevLogin,
	}, cfg.Logger)
	auth := service.NewAuthService(store, u, cfg.Hasher, tokens, cfg.Logger)
	tenants := service.NewTenantService(store, u, cache)
	// who manages members: the base model's tenant relations (can_*_member)
	base, err := rebac.BaseModel()
	if err != nil {
		return nil, err
	}
	members := service.NewMemberService(store, u, rebac.NewRBAC(base), cache, cfg.Logger)
	admin := service.NewAdminService(store, u, members, cfg.Hasher, cache, cfg.Logger)

	return &Module{
		Auth: auth, Tokens: tokens, Tenants: tenants, Members: members, Admin: admin,
		Authenticator: NewJWTAuthenticator(
			cfg.Signer.Verifier(cfg.App),
			NewVersionLookup(cfg.Redis, tokens, versionTTL, cfg.Logger),
		),
		HTTP:      httpapi.NewHandler(auth, tokens, tenants, members),
		AdminHTTP: httpapi.NewAdminHandler(admin, cfg.App, cfg.AdminKey),
		Mailer:    cfg.Mailer,
	}, nil
}
