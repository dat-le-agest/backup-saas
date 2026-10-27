package token

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// KeyRing is a signer's set of keys: the active key signs, every key verifies
// and is published in the JWKS. A rotation swaps the whole ring
// (Signer.Swap), so a KeyRing never changes once built.
type KeyRing struct {
	active    ed25519.PrivateKey
	activeKid string
	public    map[string]ed25519.PublicKey
	kids      []string // active first, then sorted
}

// NewKeyRing signs with active; verifyOnly keys stay valid for tokens they
// signed (the previous key during a rotation).
func NewKeyRing(active ed25519.PrivateKey, verifyOnly ...ed25519.PublicKey) (*KeyRing, error) {
	if len(active) != ed25519.PrivateKeySize {
		return nil, errors.New("token: invalid active key")
	}
	pub := active.Public().(ed25519.PublicKey)
	r := &KeyRing{
		active:    active,
		activeKid: KeyID(pub),
		public:    map[string]ed25519.PublicKey{KeyID(pub): pub},
	}
	for _, k := range verifyOnly {
		kid := KeyID(k)
		if _, dup := r.public[kid]; dup {
			return nil, fmt.Errorf("token: key %s listed twice", kid)
		}
		r.public[kid] = k
	}
	others := make([]string, 0, len(r.public)-1)
	for kid := range r.public {
		if kid != r.activeKid {
			others = append(others, kid)
		}
	}
	slices.Sort(others)
	r.kids = append([]string{r.activeKid}, others...)
	return r, nil
}

// ActiveFile names the key that signs in a key directory (LoadKeyRing).
const ActiveFile = "active"

// LoadKeyRing reads a key directory: every *.pem file (PKCS#8 Ed25519, see
// ParsePrivateKeyPEM) verifies, and the key named by the optional "active"
// file (a file name, with or without .pem, or a kid) signs. Without it, the
// file whose name sorts last signs, so naming keys by date is enough:
//
//	keys/
//	  2026-10-01.pem   previous key, verify only
//	  2026-10-27.pem   signs
//	  active           optional, e.g. "2026-10-01" to keep the previous key signing
//
// Hidden entries are skipped (a Kubernetes secret volume holds ..data/ and
// symlinks to it).
func LoadKeyRing(dir string) (*KeyRing, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("token: read key directory: %w", err)
	}

	type keyFile struct {
		name string // without .pem
		key  ed25519.PrivateKey
	}
	var files []keyFile
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".pem") {
			continue
		}
		path := filepath.Join(dir, name)
		// follows symlinks: secret volumes link every key into ..data/
		if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("token: read %s: %w", name, err)
		}
		key, err := ParsePrivateKeyPEM(data)
		if err != nil {
			return nil, fmt.Errorf("token: %s: %w", name, err)
		}
		files = append(files, keyFile{name: strings.TrimSuffix(name, ".pem"), key: key})
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("token: no *.pem key in %s", dir)
	}
	slices.SortFunc(files, func(a, b keyFile) int { return strings.Compare(a.name, b.name) })

	activeIdx := len(files) - 1
	raw, err := os.ReadFile(filepath.Join(dir, ActiveFile))
	switch {
	case err == nil:
		want := strings.TrimSuffix(strings.TrimSpace(string(raw)), ".pem")
		activeIdx = slices.IndexFunc(files, func(f keyFile) bool {
			return f.name == want || KeyID(f.key.Public().(ed25519.PublicKey)) == want
		})
		if activeIdx < 0 {
			return nil, fmt.Errorf("token: %s names %q, which is not a key in %s", ActiveFile, want, dir)
		}
	case !errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("token: read %s: %w", ActiveFile, err)
	}

	verifyOnly := make([]ed25519.PublicKey, 0, len(files)-1)
	for i, f := range files {
		if i != activeIdx {
			verifyOnly = append(verifyOnly, f.key.Public().(ed25519.PublicKey))
		}
	}
	r, err := NewKeyRing(files[activeIdx].key, verifyOnly...)
	if err != nil {
		return nil, fmt.Errorf("token: %s: %w", dir, err)
	}
	return r, nil
}

// ActiveKid is the kid of the key that signs.
func (r *KeyRing) ActiveKid() string { return r.activeKid }

// Kids lists every key, the active one first.
func (r *KeyRing) Kids() []string { return slices.Clone(r.kids) }

// Equal reports whether both rings hold the same keys with the same active key.
func (r *KeyRing) Equal(o *KeyRing) bool {
	return o != nil && r.activeKid == o.activeKid && slices.Equal(r.kids, o.kids)
}

// Key implements KeySource.
func (r *KeyRing) Key(kid string) (ed25519.PublicKey, error) {
	if k, ok := r.public[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("unknown kid %q", kid)
}

// JWKS publishes every key of the ring, the active one first.
func (r *KeyRing) JWKS() JWKS {
	keys := make([]ed25519.PublicKey, len(r.kids))
	for i, kid := range r.kids {
		keys[i] = r.public[kid]
	}
	return NewJWKS(keys...)
}
