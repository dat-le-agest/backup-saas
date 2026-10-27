package jwtc

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"sync"
	"time"

	"core/logger"
	"core/sctx"
	"core/token"
)

const (
	defaultIssuer     = "saas-iam"
	defaultAccessTTL  = 600
	defaultKeysReload = 60
	defaultFlagPrefix = "jwt"
)

type Option func(*options)

type options struct {
	prefix string
}

// WithFlagPrefix names the component's flags <prefix>-* (default "jwt":
// --jwt-keys-dir / JWT_KEYS_DIR), so a service can hold several signers, e.g.
// one for its users' access tokens and one for internal calls.
func WithFlagPrefix(prefix string) Option {
	return func(o *options) {
		o.prefix = prefix
	}
}

func newOptions(opts []Option) options {
	o := options{prefix: defaultFlagPrefix}
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

func (o options) flagName(name string) string {
	return o.prefix + "-" + name
}

type SignerComponent interface {
	Signer() *token.Signer
	JWKS() token.JWKS
}

type VerifierComponent interface {
	Verifier() *token.Verifier
}

type signerComponent struct {
	id string
	options
	keyFile    string
	keysDir    string
	keysReload int
	issuer     string
	accessTTL  int

	logger logger.Logger
	signer *token.Signer
	stop   chan struct{}
	done   sync.WaitGroup
}

type verifierComponent struct {
	id string
	options
	jwksURL  string
	issuer   string
	audience string

	logger   logger.Logger
	verifier *token.Verifier
}

// NewSigner signs tokens with the keys of a key directory (--jwt-keys-dir,
// reloaded every --jwt-keys-reload seconds: key rotation without restart, see
// token.LoadKeyRing) or of a single key file (--jwt-private-key-file). In dev
// without either, an ephemeral key is generated.
func NewSigner(id string, opts ...Option) *signerComponent {
	return &signerComponent{
		id:      id,
		options: newOptions(opts),
	}
}

func NewVerifier(id string, opts ...Option) *verifierComponent {
	return &verifierComponent{
		id:      id,
		options: newOptions(opts),
	}
}

func (s *signerComponent) ID() string {
	return s.id
}

func (s *signerComponent) InitFlags() {
	flag.StringVar(
		&s.keyFile,
		s.flagName("private-key-file"),
		"",
		"PEM file containing the Ed25519 signing key (or use keys-dir), one of them required outside dev",
	)

	flag.StringVar(
		&s.keysDir,
		s.flagName("keys-dir"),
		"",
		"directory of Ed25519 *.pem keys: every key verifies, the one named by its 'active' file (or sorting last) signs",
	)

	flag.IntVar(
		&s.keysReload,
		s.flagName("keys-reload"),
		defaultKeysReload,
		"seconds between reloads of keys-dir, 0 = never - Default 60",
	)

	flag.StringVar(
		&s.issuer,
		s.flagName("issuer"),
		defaultIssuer,
		"expected issuer claim, default saas-iam",
	)

	flag.IntVar(
		&s.accessTTL,
		s.flagName("access-ttl"),
		defaultAccessTTL,
		"access token lifetime in seconds, default 600",
	)
}

func (s *signerComponent) Activate(sc sctx.ServiceContext) error {
	s.logger = sc.Logger(s.id)

	if s.accessTTL <= 0 {
		return fmt.Errorf("%s must be positive", s.flagName("access-ttl"))
	}

	if s.keysReload < 0 {
		return fmt.Errorf("%s must not be negative", s.flagName("keys-reload"))
	}

	var (
		ring *token.KeyRing
		err  error
	)

	switch {
	case s.keyFile != "" && s.keysDir != "":
		return fmt.Errorf(
			"set either %s or %s, not both",
			s.flagName("private-key-file"),
			s.flagName("keys-dir"),
		)
	case s.keysDir != "":
		ring, err = token.LoadKeyRing(s.keysDir)
	case s.keyFile != "":
		ring, err = s.loadKeyFile()
	case sc.EnvName() == sctx.DevEnv:
		ring, err = s.generateDevelopmentKey()
	default:
		return fmt.Errorf(
			"%s or %s is required for app-env=%s",
			s.flagName("keys-dir"),
			s.flagName("private-key-file"),
			sc.EnvName(),
		)
	}

	if err != nil {
		return err
	}

	s.signer = token.NewRingSigner(
		ring,
		s.issuer,
		time.Duration(s.accessTTL)*time.Second,
	)

	s.logger.Infof(
		"jwt signer ready with kid=%s (keys %v), issuer=%s, ttl=%ds",
		ring.ActiveKid(),
		ring.Kids(),
		s.issuer,
		s.accessTTL,
	)

	if s.keysDir != "" && s.keysReload > 0 {
		s.watchKeys(time.Duration(s.keysReload) * time.Second)
	}

	return nil
}

func (s *signerComponent) loadKeyFile() (*token.KeyRing, error) {
	keyPEM, err := os.ReadFile(s.keyFile)
	if err != nil {
		return nil, fmt.Errorf("read jwt private key file: %w", err)
	}

	key, err := token.ParsePrivateKeyPEM(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse jwt private key: %w", err)
	}

	return token.NewKeyRing(key)
}

func (s *signerComponent) generateDevelopmentKey() (*token.KeyRing, error) {
	key, err := token.GenerateKey()
	if err != nil {
		return nil, fmt.Errorf("generate development jwt key: %w", err)
	}

	s.logger.Warn(
		"jwt keys are not configured; using an ephemeral development key",
	)

	return token.NewKeyRing(key)
}

// watchKeys reloads keys-dir every interval until Stop. Kubernetes updates a
// mounted secret by swapping a symlink, so polling sees it where file
// notifications may not. A directory that fails to load keeps the current keys.
func (s *signerComponent) watchKeys(interval time.Duration) {
	s.stop = make(chan struct{})
	s.done.Add(1)

	go func() {
		defer s.done.Done()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-s.stop:
				return
			case <-ticker.C:
				s.reloadKeys()
			}
		}
	}()
}

func (s *signerComponent) reloadKeys() {
	ring, err := token.LoadKeyRing(s.keysDir)
	if err != nil {
		s.logger.Errorf("reload jwt keys (keeping the current ones): %v", err)
		return
	}

	if ring.Equal(s.signer.KeyRing()) {
		return
	}

	s.signer.Swap(ring)
	s.logger.Infof(
		"jwt keys reloaded: active=%s keys=%v",
		ring.ActiveKid(),
		ring.Kids(),
	)
}

func (s *signerComponent) Stop() error {
	if s.stop != nil {
		close(s.stop)
		s.done.Wait()
		s.stop = nil
	}

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
		v.flagName("jwks-url"),
		"",
		"JWKS URL of the token issuer",
	)

	flag.StringVar(
		&v.issuer,
		v.flagName("issuer"),
		defaultIssuer,
		"expected issuer claim, default saas-iam",
	)

	flag.StringVar(
		&v.audience,
		v.flagName("audience"),
		"",
		"accepted audience claim: gateway app id or * for all apps, required",
	)
}

func (v *verifierComponent) Activate(sc sctx.ServiceContext) error {
	v.logger = sc.Logger(v.id)

	if v.audience == "" {
		return fmt.Errorf(
			"%s is required: specify the gateway app id or * for all apps",
			v.flagName("audience"),
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
		return fmt.Errorf("parse %s: %w", v.flagName("jwks-url"), err)
	}

	if u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("invalid %s %q", v.flagName("jwks-url"), v.jwksURL)
	}

	return nil
}

func (v *verifierComponent) Stop() error {
	return nil
}

func (v *verifierComponent) Verifier() *token.Verifier {
	return v.verifier
}
