package token

import (
	"crypto/ed25519"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DirKeys is a KeySource reading public keys (*.pem, PKIX "PUBLIC KEY") from
// a directory, e.g. a mounted secret holding the public half of a signer's
// key directory. Like RemoteJWKS it rereads the directory when its keys are
// older than maxAge or a token uses an unknown kid (at most once per
// minRefresh), and keeps the keys it has when a reread fails.
type DirKeys struct {
	dir        string
	maxAge     time.Duration
	minRefresh time.Duration

	mu       sync.Mutex
	keys     map[string]ed25519.PublicKey
	loadedAt time.Time
	triedAt  time.Time
}

// NewDirKeys reads dir once: an unreadable directory or one without keys is an error.
func NewDirKeys(dir string) (*DirKeys, error) {
	d := &DirKeys{dir: dir, maxAge: time.Minute, minRefresh: 30 * time.Second}
	if err := d.load(); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *DirKeys) Key(kid string) (ed25519.PublicKey, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if k, ok := d.keys[kid]; ok && time.Since(d.loadedAt) <= d.maxAge {
		return k, nil
	}
	if time.Since(d.triedAt) >= d.minRefresh {
		_ = d.load() // keep the current keys on failure
	}
	if k, ok := d.keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("unknown kid %q", kid)
}

// load reads the directory; callers other than NewDirKeys hold mu.
func (d *DirKeys) load() error {
	d.triedAt = time.Now()
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return fmt.Errorf("token: read public key directory: %w", err)
	}
	keys := map[string]ed25519.PublicKey{}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".pem") {
			continue
		}
		path := filepath.Join(d.dir, name)
		if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("token: read %s: %w", name, err)
		}
		key, err := ParsePublicKeyPEM(data)
		if err != nil {
			return fmt.Errorf("token: %s: %w", name, err)
		}
		keys[KeyID(key)] = key
	}
	if len(keys) == 0 {
		return fmt.Errorf("token: no *.pem public key in %s", d.dir)
	}
	d.keys, d.loadedAt = keys, time.Now()
	return nil
}
