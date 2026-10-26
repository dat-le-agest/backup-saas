package jwtc

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"time"

	"core/logger"
	"core/sctx"
	"core/token"
)

const (
	defaultIssuer    = "saas-iam"
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
	accessTTL int

	logger logger.Logger
	signer *token.Signer
}

type verifierComponent struct {
	id       string
	jwksURL  string
	issuer   string
	audience string

	logger   logger.Logger
	verifier *token.Verifier
}

func NewSigner(id string) *signerComponent {
	return &signerComponent{
		id: id,
	}
}

func NewVerifier(id string) *verifierComponent {
	return &verifierComponent{
		id: id,
	}
}

func (s *signerComponent) ID() string {
	return s.id
}

func (s *signerComponent) InitFlags() {
	flag.StringVar(
		&s.keyFile,
		"jwt-private-key-file",
		"",
		"PEM file containing the Ed25519 signing key, required outside dev",
	)

	flag.StringVar(
		&s.issuer,
		"jwt-issuer",
		defaultIssuer,
		"expected issuer claim, default saas-iam",
	)

	flag.IntVar(
		&s.accessTTL,
		"jwt-access-ttl",
		defaultAccessTTL,
		"access token lifetime in seconds, default 600",
	)
}

func (s *signerComponent) Activate(sc sctx.ServiceContext) error {
	s.logger = sc.Logger(s.id)

	if s.accessTTL <= 0 {
		return errors.New("jwt-access-ttl must be positive")
	}

	var (
		key *token.Signer
		err error
	)

	switch {
	case s.keyFile != "":
		key, err = s.loadSignerFromFile()
	case sc.EnvName() == sctx.DevEnv:
		key, err = s.generateDevelopmentSigner()
	default:
		return fmt.Errorf(
			"jwt-private-key-file is required for app-env=%s",
			sc.EnvName(),
		)
	}

	if err != nil {
		return err
	}

	s.signer = key

	s.logger.Infof(
		"jwt signer ready with kid=%s, issuer=%s, ttl=%ds",
		s.signer.PublicKeys().Keys[0].Kid,
		s.issuer,
		s.accessTTL,
	)

	return nil
}

func (s *signerComponent) loadSignerFromFile() (*token.Signer, error) {
	keyPEM, err := os.ReadFile(s.keyFile)
	if err != nil {
		return nil, fmt.Errorf("read jwt private key file: %w", err)
	}

	key, err := token.ParsePrivateKeyPEM(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse jwt private key: %w", err)
	}

	return token.NewSigner(
		key,
		s.issuer,
		time.Duration(s.accessTTL)*time.Second,
	), nil
}

func (s *signerComponent) generateDevelopmentSigner() (*token.Signer, error) {
	key, err := token.GenerateKey()
	if err != nil {
		return nil, fmt.Errorf("generate development jwt key: %w", err)
	}

	s.logger.Warn(
		"jwt private key file is not configured; using an ephemeral development key",
	)

	return token.NewSigner(
		key,
		s.issuer,
		time.Duration(s.accessTTL)*time.Second,
	), nil
}

func (s *signerComponent) Stop() error {
	return nil
}

func (s *signerComponent) Signer() *token.Signer {
	return s.signer
}

func (s *signerComponent) JWKS() token.JWKS {
	if s.signer == nil {
		return token.JWKS{}
	}

	return s.signer.PublicKeys()
}

func (v *verifierComponent) ID() string {
	return v.id
}

func (v *verifierComponent) InitFlags() {
	flag.StringVar(
		&v.jwksURL,
		"jwt-jwks-url",
		"",
		"JWKS URL of the token issuer",
	)

	flag.StringVar(
		&v.issuer,
		"jwt-issuer",
		defaultIssuer,
		"expected issuer claim, default saas-iam",
	)

	flag.StringVar(
		&v.audience,
		"jwt-audience",
		"",
		"accepted audience claim: gateway app id or * for all apps, required",
	)
}

func (v *verifierComponent) Activate(sc sctx.ServiceContext) error {
	v.logger = sc.Logger(v.id)

	if v.audience == "" {
		return errors.New(
			"jwt-audience is required: specify the gateway app id or * for all apps",
		)
	}

	if err := v.validateJWKSURL(); err != nil {
		return err
	}

	jwks := token.NewRemoteJWKS(v.jwksURL, nil)

	// The issuer may start later, so keys can be fetched lazily on demand.
	ctx, cancel := context.WithTimeout(
		context.Background(),
		3*time.Second,
	)
	defer cancel()

	if err := jwks.Refresh(ctx); err != nil {
		v.logger.Warnf(
			"jwks endpoint is not reachable yet; keys will be retried on demand: %v",
			err,
		)
	}

	v.verifier = token.NewVerifier(
		jwks,
		v.issuer,
		v.audience,
	)

	v.logger.Infof(
		"jwt verifier ready with jwks=%s, issuer=%s, audience=%s",
		v.jwksURL,
		v.issuer,
		v.audience,
	)

	return nil
}

func (v *verifierComponent) validateJWKSURL() error {
	u, err := url.Parse(v.jwksURL)
	if err != nil {
		return fmt.Errorf("parse jwt jwks url: %w", err)
	}

	if u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("invalid jwt-jwks-url %q", v.jwksURL)
	}

	return nil
}

func (v *verifierComponent) Stop() error {
	return nil
}

func (v *verifierComponent) Verifier() *token.Verifier {
	return v.verifier
}
