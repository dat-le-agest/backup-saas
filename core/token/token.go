// Package token issues and verifies the kit's access tokens (JWT, EdDSA/Ed25519).
//
// The iam service signs tokens with a private key and publishes the public keys
// as JWKS; the gateway verifies tokens against that JWKS. Access tokens are short
// lived and carry the membership version (mv) so role changes can be detected
// before expiry (see MembershipVersionKey).
package token

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims of an access token.
//
// TenantID/Role/MembershipVersion are empty when the user has not selected a
// tenant yet (right after login): such a token can only call tenant-less APIs.
type Claims struct {
	TenantID          string `json:"tid,omitempty"`
	Role              string `json:"role,omitempty"`
	MembershipVersion int64  `json:"mv,omitempty"`
	jwt.RegisteredClaims
}

func (c *Claims) UserID() string { return c.Subject }

// MembershipVersionKey is the cache key (Redis) holding the current version of
// a membership. iam writes it on every change; the gateway compares it with mv.
func MembershipVersionKey(tenantID, userID string) string {
	return "iam:mv:" + tenantID + ":" + userID
}

// RemovedMembershipVersion is stored for memberships that no longer exist.
// It never matches a token (versions start at 1).
const RemovedMembershipVersion int64 = -1

// KeyID derives a stable kid from a public key.
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return base64.RawURLEncoding.EncodeToString(sum[:12])
}

type Signer struct {
	key      ed25519.PrivateKey
	kid      string
	issuer   string
	audience string
	ttl      time.Duration
	now      func() time.Time
}

func NewSigner(key ed25519.PrivateKey, issuer, audience string, ttl time.Duration) *Signer {
	return &Signer{
		key:      key,
		kid:      KeyID(key.Public().(ed25519.PublicKey)),
		issuer:   issuer,
		audience: audience,
		ttl:      ttl,
		now:      time.Now,
	}
}

// Sign issues an access token for userID. Pass empty tenantID for a tenant-less token.
func (s *Signer) Sign(userID, tenantID, role string, membershipVersion int64) (string, time.Time, error) {
	if userID == "" {
		return "", time.Time{}, errors.New("token: empty user id")
	}
	now := s.now()
	exp := now.Add(s.ttl)

	claims := Claims{
		TenantID:          tenantID,
		Role:              role,
		MembershipVersion: membershipVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.issuer,
			Subject:   userID,
			Audience:  jwt.ClaimStrings{s.audience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
	}

	t := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	t.Header["kid"] = s.kid
	signed, err := t.SignedString(s.key)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("token: sign: %w", err)
	}
	return signed, exp, nil
}

// PublicKeys returns the JWKS document for this signer.
func (s *Signer) PublicKeys() JWKS {
	return NewJWKS(s.key.Public().(ed25519.PublicKey))
}

// KeySource resolves a kid to a public key (static set or remote JWKS).
type KeySource interface {
	Key(kid string) (ed25519.PublicKey, error)
}

var ErrInvalid = errors.New("token: invalid")

type Verifier struct {
	keys   KeySource
	parser *jwt.Parser
}

func NewVerifier(keys KeySource, issuer, audience string) *Verifier {
	return &Verifier{
		keys: keys,
		parser: jwt.NewParser(
			// only EdDSA: rejects "none" and HS256-with-public-key confusion
			jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}),
			jwt.WithIssuer(issuer),
			jwt.WithAudience(audience),
			jwt.WithExpirationRequired(),
			jwt.WithIssuedAt(),
			jwt.WithLeeway(30*time.Second),
		),
	}
}

// Verify checks signature and claims. All failures wrap ErrInvalid.
func (v *Verifier) Verify(raw string) (*Claims, error) {
	var claims Claims
	_, err := v.parser.ParseWithClaims(raw, &claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		if kid == "" {
			return nil, errors.New("missing kid")
		}
		return v.keys.Key(kid)
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if claims.Subject == "" {
		return nil, fmt.Errorf("%w: missing subject", ErrInvalid)
	}
	if claims.TenantID != "" && (claims.Role == "" || claims.MembershipVersion < 1) {
		return nil, fmt.Errorf("%w: tenant token without role or membership version", ErrInvalid)
	}
	return &claims, nil
}
