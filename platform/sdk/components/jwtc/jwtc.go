// Package jwtc provides sctx components for access tokens (sdk/token):
//
//	iam:     jwtc.NewSigner("jwt")    flags --jwt-private-key-file, --jwt-issuer, --jwt-audience, --jwt-access-ttl
//	gateway: jwtc.NewVerifier("jwt")  flags --jwt-jwks-url, --jwt-issuer, --jwt-audience
package jwtc

import (
	"context"
	"core/logger"
	"core/sctx"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"sdk/token"
	"time"
)

const (
	defaultIssuer    = "saas-iam"
	defaultAudience  = "saas-api"
	defaultAccessTTL = 600
)

type SignerComponent interface {
	Signer() *token.Signer
	JWKS() token.JWKS
}

type VerifierComponent interface {
	Verifier() *token.Verifier
}

type signerComponent struct {
	id        string
	keyFile   string
	issuer    string
	audience  string
	accessTTL int

	logger logger.Logger
	signer *token.Signer
}

func NewSigner(id string) *signerComponent {
	return &signerComponent{id: id}
}

func (s *signerComponent) ID() string { return s.id }

func (s *signerComponent) InitFlags() {
	flag.StringVar(&s.keyFile, "jwt-private-key-file", "",
		"PEM file with the Ed25519 signing key (openssl genpkey -algorithm ed25519). Required outside dev")
	flag.StringVar(&s.issuer, "jwt-issuer", defaultIssuer, "iss claim - Default "+defaultIssuer)
	flag.StringVar(&s.audience, "jwt-audience", defaultAudience, "aud claim - Default "+defaultAudience)
	flag.IntVar(&s.accessTTL, "jwt-access-ttl", defaultAccessTTL, "access token lifetime in seconds - Default 600")
}

func (s *signerComponent) Activate(sc sctx.ServiceContext) error {
	s.logger = sc.Logger(s.id)
	if s.accessTTL <= 0 {
		return errors.New("jwt-access-ttl must be positive")
	}

	switch {
	case s.keyFile != "":
		key, err := os.ReadFile(s.keyFile)
		if err != nil {
			return fmt.Errorf("read jwt-private-key-file: %w", err)
		}
		k, err := token.ParsePrivateKeyPEM(key)
		if err != nil {
			return err
		}
		s.signer = token.NewSigner(k, s.issuer, s.audience, time.Duration(s.accessTTL)*time.Second)
	case sc.EnvName() == sctx.DevEnv:
		k, err := token.GenerateKey()
		if err != nil {
			return err
		}
		s.logger.Warn("no jwt-private-key-file: using an ephemeral key, tokens become invalid on restart (dev only)")
		s.signer = token.NewSigner(k, s.issuer, s.audience, time.Duration(s.accessTTL)*time.Second)
	default:
		return fmt.Errorf("jwt-private-key-file is required for app-env=%s", sc.EnvName())
	}

	s.logger.Infof("jwt signer ready (kid=%s, iss=%s, aud=%s, ttl=%ds)",
		s.signer.PublicKeys().Keys[0].Kid, s.issuer, s.audience, s.accessTTL)
	return nil
}

func (s *signerComponent) Stop() error { return nil }

func (s *signerComponent) Signer() *token.Signer { return s.signer }

func (s *signerComponent) JWKS() token.JWKS { return s.signer.PublicKeys() }

type verifierComponent struct {
	id       string
	jwksURL  string
	issuer   string
	audience string

	logger   logger.Logger
	verifier *token.Verifier
}

func NewVerifier(id string) *verifierComponent {
	return &verifierComponent{id: id}
}

func (v *verifierComponent) ID() string { return v.id }

func (v *verifierComponent) InitFlags() {
	flag.StringVar(&v.jwksURL, "jwt-jwks-url", "",
		"JWKS URL of the token issuer, e.g. http://iam:3000/.well-known/jwks.json")
	flag.StringVar(&v.issuer, "jwt-issuer", defaultIssuer, "expected iss claim - Default "+defaultIssuer)
	flag.StringVar(&v.audience, "jwt-audience", defaultAudience, "expected aud claim - Default "+defaultAudience)
}

func (v *verifierComponent) Activate(sc sctx.ServiceContext) error {
	v.logger = sc.Logger(v.id)

	u, err := url.Parse(v.jwksURL)
	if v.jwksURL == "" || err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("invalid jwt-jwks-url %q", v.jwksURL)
	}

	jwks := token.NewRemoteJWKS(v.jwksURL, nil)
	// the issuer may start after us: keys are fetched lazily on the first token anyway
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := jwks.Refresh(ctx); err != nil {
		v.logger.Warnf("jwks not reachable yet, will retry on demand: %v", err)
	}

	v.verifier = token.NewVerifier(jwks, v.issuer, v.audience)
	v.logger.Infof("jwt verifier ready (jwks=%s, iss=%s, aud=%s)", v.jwksURL, v.issuer, v.audience)
	return nil
}

func (v *verifierComponent) Stop() error { return nil }

func (v *verifierComponent) Verifier() *token.Verifier { return v.verifier }
