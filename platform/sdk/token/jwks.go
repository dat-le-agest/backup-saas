package token

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// JWKS is the JSON Web Key Set served by iam at /.well-known/jwks.json.
type JWKS struct {
	Keys []JWK `json:"keys"`
}

// JWK of an Ed25519 public key (RFC 8037).
type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Use string `json:"use"`
}

// NewJWKS publishes the given keys. During a rotation, include the previous key
// so tokens it signed stay valid until they expire.
func NewJWKS(keys ...ed25519.PublicKey) JWKS {
	set := JWKS{Keys: make([]JWK, 0, len(keys))}
	for _, k := range keys {
		set.Keys = append(set.Keys, JWK{
			Kty: "OKP", Crv: "Ed25519", Alg: "EdDSA", Use: "sig",
			X:   base64.RawURLEncoding.EncodeToString(k),
			Kid: KeyID(k),
		})
	}
	return set
}

func (s JWKS) keyMap() (map[string]ed25519.PublicKey, error) {
	m := make(map[string]ed25519.PublicKey, len(s.Keys))
	for _, k := range s.Keys {
		if k.Kty != "OKP" || k.Crv != "Ed25519" {
			continue
		}
		x, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil || len(x) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("jwks: invalid key %q", k.Kid)
		}
		m[k.Kid] = ed25519.PublicKey(x)
	}
	return m, nil
}

// StaticKeys is a fixed key set (tests, or when keys are configured directly).
type StaticKeys map[string]ed25519.PublicKey

func NewStaticKeys(keys ...ed25519.PublicKey) StaticKeys {
	m := StaticKeys{}
	for _, k := range keys {
		m[KeyID(k)] = k
	}
	return m
}

func (s StaticKeys) Key(kid string) (ed25519.PublicKey, error) {
	if k, ok := s[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("unknown kid %q", kid)
}

// RemoteJWKS fetches keys from a JWKS URL and caches them. It refreshes
// periodically and when a token uses an unknown kid (key rotation), at most
// once per minRefresh so bogus kids can't flood the issuer.
type RemoteJWKS struct {
	url        string
	client     *http.Client
	maxAge     time.Duration
	minRefresh time.Duration

	mu        sync.Mutex
	keys      map[string]ed25519.PublicKey
	fetchedAt time.Time
	triedAt   time.Time
}

func NewRemoteJWKS(url string, client *http.Client) *RemoteJWKS {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &RemoteJWKS{url: url, client: client, maxAge: 10 * time.Minute, minRefresh: 30 * time.Second}
}

func (r *RemoteJWKS) Key(kid string) (ed25519.PublicKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	stale := time.Since(r.fetchedAt) > r.maxAge
	if k, ok := r.keys[kid]; ok && !stale {
		return k, nil
	}

	if time.Since(r.triedAt) >= r.minRefresh || r.keys == nil {
		r.triedAt = time.Now()
		if err := r.fetchLocked(context.Background()); err != nil && r.keys == nil {
			return nil, err
		}
	}

	// keep serving cached keys if a refresh failed: the issuer may be briefly down
	if k, ok := r.keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("unknown kid %q", kid)
}

// Refresh fetches the key set now (e.g. at startup).
func (r *RemoteJWKS) Refresh(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.triedAt = time.Now()
	return r.fetchLocked(ctx)
}

func (r *RemoteJWKS) fetchLocked(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url, nil)
	if err != nil {
		return err
	}
	res, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("jwks: fetch %s: %w", r.url, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks: fetch %s: status %d", r.url, res.StatusCode)
	}

	var set JWKS
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&set); err != nil {
		return fmt.Errorf("jwks: decode: %w", err)
	}
	keys, err := set.keyMap()
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return errors.New("jwks: no Ed25519 keys")
	}
	r.keys = keys
	r.fetchedAt = time.Now()
	return nil
}
