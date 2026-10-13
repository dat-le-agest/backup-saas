package token

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func newSigner(t *testing.T) (*Signer, ed25519.PrivateKey) {
	t.Helper()
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return NewSigner(key, "iam", "api", 10*time.Minute), key
}

func TestSignVerify(t *testing.T) {
	s, key := newSigner(t)
	v := NewVerifier(NewStaticKeys(key.Public().(ed25519.PublicKey)), "iam", "api")

	raw, exp, err := s.Sign("u1", "t1", "admin", 3)
	if err != nil {
		t.Fatal(err)
	}
	if time.Until(exp) < 9*time.Minute {
		t.Fatalf("exp %v", exp)
	}
	c, err := v.Verify(raw)
	if err != nil {
		t.Fatal(err)
	}
	if c.UserID() != "u1" || c.TenantID != "t1" || c.Role != "admin" || c.MembershipVersion != 3 {
		t.Fatalf("claims %+v", c)
	}

	// tenant-less token right after login
	raw, _, _ = s.Sign("u1", "", "", 0)
	if c, err := v.Verify(raw); err != nil || c.TenantID != "" {
		t.Fatalf("tenant-less: %+v %v", c, err)
	}
}

func TestRejects(t *testing.T) {
	s, key := newSigner(t)
	pub := key.Public().(ed25519.PublicKey)
	v := NewVerifier(NewStaticKeys(pub), "iam", "api")
	valid, _, _ := s.Sign("u1", "t1", "admin", 1)

	other, _ := newSigner(t)
	foreign, _, _ := other.Sign("u1", "t1", "owner", 1)

	expiredSigner := NewSigner(key, "iam", "api", -time.Hour)
	expired, _, _ := expiredSigner.Sign("u1", "t1", "admin", 1)

	wrongAud, _, _ := NewSigner(key, "iam", "other-api", time.Minute).Sign("u1", "", "", 0)
	wrongIss, _, _ := NewSigner(key, "evil", "api", time.Minute).Sign("u1", "", "", 0)

	// payload tampering: promote to owner, keep signature
	parts := strings.Split(valid, ".")
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(strings.Replace(string(payload), `"admin"`, `"owner"`, 1))) + "." + parts[2]

	// alg confusion: HS256 signed with the public key bytes as the HMAC secret
	hs := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{RegisteredClaims: jwt.RegisteredClaims{
		Issuer: "iam", Subject: "u1", Audience: jwt.ClaimStrings{"api"},
		IssuedAt: jwt.NewNumericDate(time.Now()), ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}})
	hs.Header["kid"] = KeyID(pub)
	hsToken, _ := hs.SignedString([]byte(pub))

	// alg none
	none := jwt.NewWithClaims(jwt.SigningMethodNone, Claims{RegisteredClaims: jwt.RegisteredClaims{
		Issuer: "iam", Subject: "u1", Audience: jwt.ClaimStrings{"api"},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}})
	none.Header["kid"] = KeyID(pub)
	noneToken, _ := none.SignedString(jwt.UnsafeAllowNoneSignatureType)

	// tenant token missing mv
	noMV, _, _ := s.Sign("u1", "t1", "admin", 0)

	cases := map[string]string{
		"foreign key": foreign, "expired": expired, "wrong audience": wrongAud,
		"wrong issuer": wrongIss, "tampered": tampered, "hs256 confusion": hsToken,
		"alg none": noneToken, "tenant without mv": noMV, "garbage": "a.b.c", "empty": "",
	}
	for name, raw := range cases {
		if _, err := v.Verify(raw); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: expected ErrInvalid, got %v", name, err)
		}
	}
}

func TestRemoteJWKSRotation(t *testing.T) {
	oldSigner, oldKey := newSigner(t)
	newSigner_, newKey := newSigner(t)

	var published atomic.Value
	published.Store(NewJWKS(oldKey.Public().(ed25519.PublicKey)))
	var fetches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		_ = json.NewEncoder(w).Encode(published.Load())
	}))
	defer srv.Close()

	jwks := NewRemoteJWKS(srv.URL, nil)
	jwks.minRefresh = 0
	v := NewVerifier(jwks, "iam", "api")

	oldTok, _, _ := oldSigner.Sign("u1", "", "", 0)
	if _, err := v.Verify(oldTok); err != nil {
		t.Fatal(err)
	}

	// rotation: publish new + old; a token with the new kid triggers a refetch
	published.Store(NewJWKS(newKey.Public().(ed25519.PublicKey), oldKey.Public().(ed25519.PublicKey)))
	newTok, _, _ := newSigner_.Sign("u1", "", "", 0)
	if _, err := v.Verify(newTok); err != nil {
		t.Fatalf("new key after rotation: %v", err)
	}
	if _, err := v.Verify(oldTok); err != nil {
		t.Fatalf("old key still published: %v", err)
	}
	if n := fetches.Load(); n != 2 {
		t.Fatalf("expected 2 fetches, got %d", n)
	}
}

func TestRemoteJWKSRateLimitsUnknownKid(t *testing.T) {
	_, key := newSigner(t)
	var fetches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		_ = json.NewEncoder(w).Encode(NewJWKS(key.Public().(ed25519.PublicKey)))
	}))
	defer srv.Close()

	jwks := NewRemoteJWKS(srv.URL, nil)
	for i := 0; i < 50; i++ {
		_, _ = jwks.Key("bogus")
	}
	if n := fetches.Load(); n != 1 {
		t.Fatalf("unknown kids caused %d fetches", n)
	}
}

func TestPEMRoundTrip(t *testing.T) {
	key, _ := GenerateKey()
	pemBytes, err := EncodePrivateKeyPEM(key)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParsePrivateKeyPEM(pemBytes)
	if err != nil || !back.Equal(key) {
		t.Fatalf("roundtrip: %v", err)
	}
	if _, err := ParsePrivateKeyPEM([]byte("nope")); err == nil {
		t.Fatal("expected error")
	}
}
