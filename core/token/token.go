// Package token issues and verifies the kit's access tokens (JWT, EdDSA/Ed25519).
//
// The iam service signs tokens with a private key and publishes the public keys
// as JWKS; gateways verify tokens against that JWKS. Access tokens are short
// lived and carry the membership version (mv) so role changes can be detected
// before expiry (see MembershipVersionKey).
//
// Every token is issued for one app: its audience (aud) is the app id. An app
// gateway accepts only its app's tokens; the platform gateway accepts any app
// (AnyAudience) and passes the app on.
package token

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"sync/atomic"
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

// AppID is the app the token was issued for (its single audience).
func (c *Claims) AppID() string {
	if len(c.Audience) != 1 {
		return ""
	}
	return c.Audience[0]
}

// AnyAudience makes a Verifier accept tokens of every app (the platform
// gateway); the app is then read from Claims.AppID.
const AnyAudience = "*"

// MembershipVersionKey is the cache key (Redis) holding the current version of
// a membership of a tenant of appID. iam writes it on every change; gateways
// compare it with mv.
func MembershipVersionKey(appID, tenantID, userID string) string {
	return "iam:mv:" + appID + ":" + tenantID + ":" + userID
}

// RemovedMembershipVersion is stored for memberships that no longer exist.
// It never matches a token (versions start at 1).
const RemovedMembershipVersion int64 = -1

// KeyID derives a stable kid from a public key.
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return base64.RawURLEncoding.EncodeToString(sum[:12])
}

// Signer signs with the active key of its KeyRing. Swap replaces the ring
// (key rotation) while tokens are being signed and verified.
type Signer struct {
	ring   atomic.Pointer[KeyRing]
	issuer string
	ttl    time.Duration
	now    func() time.Time
}

// NewSigner signs with a single key.
func NewSigner(key ed25519.PrivateKey, issuer string, ttl time.Duration) *Signer {
	ring, err := NewKeyRing(key)
	if err != nil {
		panic(err) // a single key can't be listed twice: only an invalid key gets here
	}
	return NewRingSigner(ring, issuer, ttl)
}

// NewRingSigner signs with the active key of ring and verifies with all of its keys.
func NewRingSigner(ring *KeyRing, issuer string, ttl time.Duration) *Signer {
	s := &Signer{issuer: issuer, ttl: ttl, now: time.Now}
	s.ring.Store(ring)
	return s
}

// Swap makes ring the signer's keys: tokens are signed with its active key from
// now on, and only its keys verify (Verifier, PublicKeys).
func (s *Signer) Swap(ring *KeyRing) { s.ring.Store(ring) }

// KeyRing returns the current keys.
func (s *Signer) KeyRing() *KeyRing { return s.ring.Load() }

// Sign issues an access token for userID in appID (the audience). Pass empty
// tenantID for a tenant-less token.
func (s *Signer) Sign(userID, appID, tenantID, role string, membershipVersion int64) (string, time.Time, error) {
	if userID == "" {
		return "", time.Time{}, errors.New("token: empty user id")
	}
	if appID == "" || appID == AnyAudience {
		return "", time.Time{}, errors.New("token: invalid app id")
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
			Audience:  jwt.ClaimStrings{appID},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
	}

	ring := s.ring.Load()
	t := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	t.Header["kid"] = ring.activeKid
	signed, err := t.SignedString(ring.active)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("token: sign: %w", err)
	}
	return signed, exp, nil
}

// Verifier verifies tokens signed by s for audience (an app id), e.g. in the
// gateway that issues them. It follows Swap: every key of the current ring verifies.
func (s *Signer) Verifier(audience string) *Verifier {
	return NewVerifier(signerKeys{s}, s.issuer, audience)
}

// PublicKeys returns the JWKS document for this signer: every key of its ring.
func (s *Signer) PublicKeys() JWKS {
	return s.ring.Load().JWKS()
}

// signerKeys resolves kids against the signer's current ring.
type signerKeys struct{ s *Signer }

func (k signerKeys) Key(kid string) (ed25519.PublicKey, error) { return k.s.ring.Load().Key(kid) }

// KeySource resolves a kid to a public key (static set or remote JWKS).
type KeySource interface {
	Key(kid string) (ed25519.PublicKey, error)
}

var ErrInvalid = errors.New("token: invalid")

type Verifier struct {
	keys   KeySource
	parser *jwt.Parser
}

// NewVerifier accepts tokens of issuer for the app audience, or of every app
// when audience is AnyAudience.
func NewVerifier(keys KeySource, issuer, audience string) *Verifier {
	opts := []jwt.ParserOption{
		// only EdDSA: rejects "none" and HS256-with-public-key confusion
		jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}),
		jwt.WithIssuer(issuer),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(30 * time.Second),
	}
	if audience != AnyAudience {
		opts = append(opts, jwt.WithAudience(audience))
	}
	return &Verifier{keys: keys, parser: jwt.NewParser(opts...)}
}

// Verify checks signature and claims. All failures wrap ErrInvalid.
func (v *Verifier) Verify(raw string) (*Claims, error) {
	var claims Claims
	_, err := v.parser.ParseWithClaims(raw, &claims, func(t *jwt.Token) (any, error) {
		if typ, _ := t.Header["typ"].(string); typ == InternalTokenType {
			return nil, errors.New("an internal token is not an access token")
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
	if claims.Subject == "" {
		return nil, fmt.Errorf("%w: missing subject", ErrInvalid)
	}
	if app := claims.AppID(); app == "" || app == AnyAudience {
		return nil, fmt.Errorf("%w: a token must have exactly one app audience", ErrInvalid)
	}
	if claims.TenantID != "" && (claims.Role == "" || claims.MembershipVersion < 1) {
		return nil, fmt.Errorf("%w: tenant token without role or membership version", ErrInvalid)
	}
	return &claims, nil
}
