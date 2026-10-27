package token

import (
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestInternalSignVerify(t *testing.T) {
	s, _ := newSigner(t)
	v := NewInternalVerifier(s.Keys(), "iam", "note")

	raw, err := s.SignInternal("note", Internal{App: "notes", UserID: "u1", TenantID: "t1", Role: "member"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	c, err := v.Verify(raw)
	if err != nil {
		t.Fatal(err)
	}
	if c.Identity() != (Internal{App: "notes", UserID: "u1", TenantID: "t1", Role: "member"}) || c.ID == "" {
		t.Fatalf("claims %+v", c)
	}
	if d := c.ExpiresAt.Sub(c.IssuedAt.Time); d != DefaultInternalTTL {
		t.Fatalf("default lifetime %s", d)
	}

	// anonymous call: no user, the token still proves the caller
	raw, _ = s.SignInternal("note", Internal{}, 0)
	if c, err := v.Verify(raw); err != nil || c.Identity() != (Internal{}) {
		t.Fatalf("anonymous: %+v %v", c, err)
	}

	for name, aud := range map[string]string{"empty": "", "wildcard": AnyAudience} {
		if _, err := s.SignInternal(aud, Internal{}, 0); err == nil {
			t.Errorf("%s audience: expected an error", name)
		}
	}
	if _, err := s.SignInternal("note", Internal{}, MaxInternalTTL+time.Second); err == nil {
		t.Error("lifetime above the maximum: expected an error")
	}
}

func TestInternalRejects(t *testing.T) {
	s, key := newSigner(t)
	v := NewInternalVerifier(s.Keys(), "iam", "note")
	id := Internal{UserID: "u1", TenantID: "t1", Role: "owner"}

	other, _ := newSigner(t)
	foreign, _ := other.SignInternal("note", id, 0)
	wrongAud, _ := s.SignInternal("billing", id, 0)
	wrongIss, _ := NewSigner(key, "evil", time.Minute).SignInternal("note", id, 0)
	expiredSigner := NewSigner(key, "iam", time.Minute)
	expiredSigner.now = func() time.Time { return time.Now().Add(-time.Hour) }
	expired, _ := expiredSigner.SignInternal("note", id, 0)

	// an access token, even signed with the same key, is not an internal token
	access, _, _ := s.Sign("u1", "note", "t1", "owner", 1)

	// a token signed with the right key but living too long (or without iat)
	long := func(claims InternalClaims) string {
		tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
		tok.Header["typ"] = InternalTokenType
		tok.Header["kid"] = KeyID(key.Public().(ed25519.PublicKey))
		raw, _ := tok.SignedString(key)
		return raw
	}
	now := time.Now()
	tooLong := long(InternalClaims{RegisteredClaims: jwt.RegisteredClaims{
		Issuer: "iam", Subject: "u1", Audience: jwt.ClaimStrings{"note"},
		IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
	}})
	noIat := long(InternalClaims{RegisteredClaims: jwt.RegisteredClaims{
		Issuer: "iam", Subject: "u1", Audience: jwt.ClaimStrings{"note"},
		ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
	}})
	twoAud := long(InternalClaims{RegisteredClaims: jwt.RegisteredClaims{
		Issuer: "iam", Subject: "u1", Audience: jwt.ClaimStrings{"note", "billing"},
		IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
	}})

	cases := map[string]string{
		"foreign key": foreign, "wrong audience": wrongAud, "wrong issuer": wrongIss,
		"expired": expired, "access token": access, "too long": tooLong, "no iat": noIat,
		"two audiences": twoAud, "garbage": "a.b.c", "empty": "",
	}
	for name, raw := range cases {
		if _, err := v.Verify(raw); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: expected ErrInvalid, got %v", name, err)
		}
	}

	// nor is an internal token an access token
	internal, _ := s.SignInternal("notes", id, 0)
	if _, err := s.Verifier("notes").Verify(internal); !errors.Is(err, ErrInvalid) {
		t.Errorf("internal token as access token: %v", err)
	}
}

func TestDirKeys(t *testing.T) {
	dir := t.TempDir()
	writePub := func(name string) (*Signer, string) {
		key, _ := GenerateKey()
		pub := key.Public().(ed25519.PublicKey)
		data, err := EncodePublicKeyPEM(pub)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
		return NewSigner(key, "iam", time.Minute), KeyID(pub)
	}

	if _, err := NewDirKeys(dir); err == nil {
		t.Fatal("an empty directory must fail")
	}
	a, _ := writePub("a.pem")
	keys, err := NewDirKeys(dir)
	if err != nil {
		t.Fatal(err)
	}
	keys.minRefresh = 0
	v := NewInternalVerifier(keys, "iam", "note")
	raw, _ := a.SignInternal("note", Internal{UserID: "u1"}, 0)
	if _, err := v.Verify(raw); err != nil {
		t.Fatal(err)
	}

	// a key added later is picked up on its first unknown kid
	b, bKid := writePub("b.pem")
	raw, _ = b.SignInternal("note", Internal{UserID: "u1"}, 0)
	if _, err := v.Verify(raw); err != nil {
		t.Fatalf("new key: %v", err)
	}

	// a broken file keeps the keys already loaded
	_ = os.WriteFile(filepath.Join(dir, "c.pem"), []byte("nope"), 0o600)
	if k, err := keys.Key(bKid); err != nil || k == nil {
		t.Fatalf("after a failed reload: %v", err)
	}

	// private keys are refused: services only get the public half
	priv, _ := GenerateKey()
	data, _ := EncodePrivateKeyPEM(priv)
	if _, err := ParsePublicKeyPEM(data); err == nil {
		t.Fatal("a private key PEM must be refused")
	}
}
