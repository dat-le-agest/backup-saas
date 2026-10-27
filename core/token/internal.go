package token

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Internal tokens prove that a call to a service comes from a trusted caller
// (an app's gateway) and carry the identity that caller authenticated. They
// are signed with their own keys (not the access tokens' ones), live for
// seconds, and are issued for one service (the audience). Services take the
// caller's identity from them only, never from plain headers.

const (
	// InternalTokenType is the "typ" header of internal tokens: an access
	// token is never accepted as an internal one, nor the other way round.
	InternalTokenType = "internal+jwt"
	// DefaultInternalTTL: one call, plus retries and clock skew.
	DefaultInternalTTL = time.Minute
	// MaxInternalTTL bounds the lifetime a verifier accepts, whatever the signer set.
	MaxInternalTTL = 5 * time.Minute
)

// Internal is the identity an internal token carries. A call without a user
// (e.g. anonymous) carries an empty one: the token still proves the caller.
type Internal struct {
	App, UserID, TenantID, Role string
}

// InternalClaims of an internal token. The subject is the user id (may be empty).
type InternalClaims struct {
	App      string `json:"app,omitempty"`
	TenantID string `json:"tid,omitempty"`
	Role     string `json:"role,omitempty"`
	jwt.RegisteredClaims
}

func (c *InternalClaims) Identity() Internal {
	return Internal{App: c.App, UserID: c.Subject, TenantID: c.TenantID, Role: c.Role}
}

// SignInternal issues an internal token for a call to the audience service.
// ttl <= 0 uses DefaultInternalTTL.
func (s *Signer) SignInternal(audience string, id Internal, ttl time.Duration) (string, error) {
	if audience == "" || audience == AnyAudience {
		return "", errors.New("token: invalid internal audience")
	}
	if ttl <= 0 {
		ttl = DefaultInternalTTL
	}
	if ttl > MaxInternalTTL {
		return "", fmt.Errorf("token: internal token lifetime above %s", MaxInternalTTL)
	}
	jti := make([]byte, 12)
	_, _ = rand.Read(jti)

	now := s.now()
	claims := InternalClaims{
		App:      id.App,
		TenantID: id.TenantID,
		Role:     id.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        hex.EncodeToString(jti),
			Issuer:    s.issuer,
			Subject:   id.UserID,
			Audience:  jwt.ClaimStrings{audience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}

	ring := s.ring.Load()
	t := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	t.Header["typ"] = InternalTokenType
	t.Header["kid"] = ring.activeKid
	signed, err := t.SignedString(ring.active)
	if err != nil {
		return "", fmt.Errorf("token: sign internal: %w", err)
	}
	return signed, nil
}

// Keys resolves kids against the signer's current ring (for verifiers in the
// same process, e.g. tests).
func (s *Signer) Keys() KeySource { return signerKeys{s} }

// InternalVerifier verifies internal tokens issued for one service.
type InternalVerifier struct {
	keys   KeySource
	parser *jwt.Parser
}

// NewInternalVerifier accepts internal tokens of issuer for audience (the
// service's name).
func NewInternalVerifier(keys KeySource, issuer, audience string) *InternalVerifier {
	return &InternalVerifier{
		keys: keys,
		parser: jwt.NewParser(
			jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}),
			jwt.WithIssuer(issuer),
			jwt.WithAudience(audience),
			jwt.WithExpirationRequired(),
			jwt.WithIssuedAt(),
			jwt.WithLeeway(30*time.Second),
		),
	}
}

// Verify checks signature, type, audience and lifetime. All failures wrap ErrInvalid.
func (v *InternalVerifier) Verify(raw string) (*InternalClaims, error) {
	var claims InternalClaims
	_, err := v.parser.ParseWithClaims(raw, &claims, func(t *jwt.Token) (any, error) {
		if typ, _ := t.Header["typ"].(string); typ != InternalTokenType {
			return nil, errors.New("not an internal token")
		}
		kid, _ := t.Header["kid"].(string)
		if kid == "" {
			return nil, errors.New("missing kid")
		}
		return v.keys.Key(kid)
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if claims.IssuedAt == nil || claims.ExpiresAt.Sub(claims.IssuedAt.Time) > MaxInternalTTL {
		return nil, fmt.Errorf("%w: internal token lifetime above %s", ErrInvalid, MaxInternalTTL)
	}
	if len(claims.Audience) != 1 {
		return nil, fmt.Errorf("%w: an internal token must have exactly one audience", ErrInvalid)
	}
	return &claims, nil
}
