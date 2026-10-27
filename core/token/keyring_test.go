package token

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// writeKey writes a new key to dir/name.pem and returns it.
func writeKey(t *testing.T, dir, name string) ed25519.PrivateKey {
	t.Helper()
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	data, err := EncodePrivateKeyPEM(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".pem"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return key
}

func kid(k ed25519.PrivateKey) string { return KeyID(k.Public().(ed25519.PublicKey)) }

func TestLoadKeyRingActiveKey(t *testing.T) {
	dir := t.TempDir()
	old := writeKey(t, dir, "2026-10-01")
	cur := writeKey(t, dir, "2026-10-27")
	// not keys: skipped
	_ = os.WriteFile(filepath.Join(dir, "README"), []byte("keys"), 0o600)
	_ = os.Mkdir(filepath.Join(dir, "..data"), 0o700)

	r, err := LoadKeyRing(dir)
	if err != nil {
		t.Fatal(err)
	}
	if r.ActiveKid() != kid(cur) {
		t.Fatalf("without an active file the last name signs: got %s", r.ActiveKid())
	}
	if got := r.Kids(); !slices.Equal(got, []string{kid(cur), kid(old)}) {
		t.Fatalf("kids %v", got)
	}

	for _, active := range []string{"2026-10-01", "2026-10-01.pem\n", kid(old)} {
		if err := os.WriteFile(filepath.Join(dir, ActiveFile), []byte(active), 0o600); err != nil {
			t.Fatal(err)
		}
		r, err := LoadKeyRing(dir)
		if err != nil {
			t.Fatalf("active %q: %v", active, err)
		}
		if r.ActiveKid() != kid(old) {
			t.Fatalf("active %q: got %s", active, r.ActiveKid())
		}
		if _, err := r.Key(kid(cur)); err != nil {
			t.Fatalf("the other key must still verify: %v", err)
		}
	}
}

func TestLoadKeyRingFollowsSymlinks(t *testing.T) {
	// layout of a Kubernetes secret volume: keys/x.pem -> ..data/x.pem
	dir := t.TempDir()
	data := filepath.Join(dir, "..data")
	if err := os.Mkdir(data, 0o700); err != nil {
		t.Fatal(err)
	}
	key := writeKey(t, data, "2026-10-27")
	if err := os.Symlink(filepath.Join("..data", "2026-10-27.pem"), filepath.Join(dir, "2026-10-27.pem")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	r, err := LoadKeyRing(dir)
	if err != nil {
		t.Fatal(err)
	}
	if r.ActiveKid() != kid(key) || len(r.Kids()) != 1 {
		t.Fatalf("got %v", r.Kids())
	}
}

func TestLoadKeyRingErrors(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string){
		"empty directory": func(*testing.T, string) {},
		"broken pem": func(t *testing.T, dir string) {
			_ = os.WriteFile(filepath.Join(dir, "a.pem"), []byte("not a key"), 0o600)
		},
		"unknown active key": func(t *testing.T, dir string) {
			writeKey(t, dir, "a")
			_ = os.WriteFile(filepath.Join(dir, ActiveFile), []byte("b"), 0o600)
		},
		"same key twice": func(t *testing.T, dir string) {
			writeKey(t, dir, "a")
			data, _ := os.ReadFile(filepath.Join(dir, "a.pem"))
			_ = os.WriteFile(filepath.Join(dir, "b.pem"), data, 0o600)
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			setup(t, dir)
			if _, err := LoadKeyRing(dir); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	if _, err := LoadKeyRing(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing directory: expected an error")
	}
}

// The rotation of the runbook: publish the new key, switch, retire the old one.
func TestSignerSwapRotation(t *testing.T) {
	oldKey, _ := GenerateKey()
	newKey, _ := GenerateKey()
	oldPub, newPub := oldKey.Public().(ed25519.PublicKey), newKey.Public().(ed25519.PublicKey)

	s := NewSigner(oldKey, "iam", 10*time.Minute)
	v := s.Verifier("notes")
	oldTok, _, _ := s.Sign("u1", "notes", "", "", 0)

	// 1. the new key is published, the old one still signs
	r1, err := NewKeyRing(oldKey, newPub)
	if err != nil {
		t.Fatal(err)
	}
	s.Swap(r1)
	if jwks := s.PublicKeys(); len(jwks.Keys) != 2 || jwks.Keys[0].Kid != KeyID(oldPub) {
		t.Fatalf("jwks %+v", jwks)
	}

	// 2. the new key signs; tokens of the old one still verify
	r2, _ := NewKeyRing(newKey, oldPub)
	s.Swap(r2)
	newTok, _, _ := s.Sign("u1", "notes", "", "", 0)
	if headerKid(t, newTok) != KeyID(newPub) {
		t.Fatal("new tokens must use the new key")
	}
	for name, raw := range map[string]string{"old": oldTok, "new": newTok} {
		if _, err := v.Verify(raw); err != nil {
			t.Fatalf("%s token during rotation: %v", name, err)
		}
	}

	// 3. the old key is retired: its tokens are rejected
	r3, _ := NewKeyRing(newKey)
	s.Swap(r3)
	if _, err := v.Verify(oldTok); !errors.Is(err, ErrInvalid) {
		t.Fatalf("old token after retirement: %v", err)
	}
	if _, err := v.Verify(newTok); err != nil {
		t.Fatal(err)
	}
}

func TestKeyRingEqual(t *testing.T) {
	a, _ := GenerateKey()
	b, _ := GenerateKey()
	bPub := b.Public().(ed25519.PublicKey)

	r1, _ := NewKeyRing(a, bPub)
	r2, _ := NewKeyRing(a, bPub)
	r3, _ := NewKeyRing(b, a.Public().(ed25519.PublicKey))
	if !r1.Equal(r2) || r1.Equal(r3) || r1.Equal(nil) {
		t.Fatal("Equal compares the keys and the active key")
	}
	if _, err := NewKeyRing(a, a.Public().(ed25519.PublicKey)); err == nil {
		t.Fatal("the active key listed again must fail")
	}
}

// headerKid returns the kid of a JWT's header.
func headerKid(t *testing.T, raw string) string {
	t.Helper()
	header, _, _ := strings.Cut(raw, ".")
	b, err := base64.RawURLEncoding.DecodeString(header)
	if err != nil {
		t.Fatal(err)
	}
	var h struct {
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(b, &h); err != nil {
		t.Fatal(err)
	}
	return h.Kid
}
