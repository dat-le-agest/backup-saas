// Package internalauthc is how a service authenticates its callers: every RPC
// must carry an internal token (core/token, signed by the app's gateway) and
// the caller's identity is taken from it, never from plain headers.
//
//	--internal-auth / INTERNAL_AUTH
//	  enforce     (default) a valid internal token is required
//	  permissive  identity headers are accepted without a token (migrations only)
//	  off         identity headers are trusted (dev and tests; refused with app-env=prd)
//
//	public keys of the gateway, one of:
//	--internal-jwt-jwks-url / INTERNAL_JWT_JWKS_URL           the gateway's internal JWKS
//	--internal-jwt-public-keys-dir / INTERNAL_JWT_PUBLIC_KEYS_DIR  *.pem public keys
//
//	--internal-jwt-issuer   / INTERNAL_JWT_ISSUER    the gateway's (INTERNAL_JWT_ISSUER there too)
//	--internal-jwt-audience / INTERNAL_JWT_AUDIENCE  default: the service name
package internalauthc

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"time"

	"core/logger"
	"core/rpc"
	"core/sctx"
	"core/token"
)

const defaultIssuer = "saas-iam"

type Component interface {
	// Auth goes to rpc.HandlerOptions.
	Auth() rpc.InternalAuth
}

type component struct {
	id         string
	mode       string
	jwksURL    string
	publicKeys string
	issuer     string
	audience   string

	logger logger.Logger
	auth   rpc.InternalAuth
}

func New(id string) *component {
	return &component{id: id}
}

func (c *component) ID() string { return c.id }

func (c *component) InitFlags() {
	flag.StringVar(&c.mode, "internal-auth", rpc.InternalAuthEnforce,
		"how callers are authenticated: enforce (internal token required) | permissive (headers accepted without a token) | off (headers trusted, dev only) - Default enforce")
	flag.StringVar(&c.jwksURL, "internal-jwt-jwks-url", "",
		"JWKS of the gateway's internal tokens, e.g. http://notes-gateway:9081/.well-known/internal-jwks.json")
	flag.StringVar(&c.publicKeys, "internal-jwt-public-keys-dir", "",
		"directory of the gateway's internal token public keys (*.pem), instead of the JWKS URL")
	flag.StringVar(&c.issuer, "internal-jwt-issuer", defaultIssuer,
		"issuer of the internal tokens (the gateway's INTERNAL_JWT_ISSUER) - Default saas-iam")
	flag.StringVar(&c.audience, "internal-jwt-audience", "",
		"audience the internal tokens must be issued for - Default the service name")
}

func (c *component) Activate(sc sctx.ServiceContext) error {
	c.logger = sc.Logger(c.id)

	switch c.mode {
	case rpc.InternalAuthOff:
		if sc.EnvName() == sctx.PrdEnv {
			return fmt.Errorf("internal-auth=off is not allowed with app-env=%s", sc.EnvName())
		}
		c.auth = rpc.InternalAuth{Mode: rpc.InternalAuthOff}
		c.logger.Warn("internal auth is off: identity headers are trusted as sent")
		return nil
	case rpc.InternalAuthEnforce, rpc.InternalAuthPermissive:
	default:
		return fmt.Errorf("invalid internal-auth %q (enforce | permissive | off)", c.mode)
	}

	if c.audience == "" {
		c.audience = sc.GetName()
	}
	if c.audience == "" {
		return fmt.Errorf("internal-jwt-audience is required")
	}

	var (
		keys   token.KeySource
		source string
	)
	switch {
	case c.jwksURL != "" && c.publicKeys != "":
		return fmt.Errorf("set either internal-jwt-jwks-url or internal-jwt-public-keys-dir, not both")
	case c.jwksURL != "":
		u, err := url.Parse(c.jwksURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("invalid internal-jwt-jwks-url %q", c.jwksURL)
		}
		jwks := token.NewRemoteJWKS(c.jwksURL, nil)
		// the gateway may start later: keys are fetched on the first call then
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := jwks.Refresh(ctx); err != nil {
			c.logger.Warnf("internal jwks not reachable yet, retried on the first call: %v", err)
		}
		keys, source = jwks, "jwks="+c.jwksURL
	case c.publicKeys != "":
		dir, err := token.NewDirKeys(c.publicKeys)
		if err != nil {
			return err
		}
		keys, source = dir, "keys="+c.publicKeys
	default:
		return fmt.Errorf("internal-jwt-jwks-url or internal-jwt-public-keys-dir is required with internal-auth=%s", c.mode)
	}

	c.auth = rpc.InternalAuth{Mode: c.mode, Verifier: token.NewInternalVerifier(keys, c.issuer, c.audience)}
	c.logger.Infof("internal auth %s (%s, issuer=%s, audience=%s)", c.mode, source, c.issuer, c.audience)
	if c.mode == rpc.InternalAuthPermissive {
		c.logger.Warn("internal auth is permissive: calls without a token are accepted (rpc.internal_auth.fallback)")
	}
	return nil
}

func (c *component) Stop() error { return nil }

func (c *component) Auth() rpc.InternalAuth { return c.auth }
