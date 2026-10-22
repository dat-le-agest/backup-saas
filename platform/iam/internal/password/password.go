// Package password hashes and verifies passwords with argon2id.
//
// Hashes are stored as PHC strings ($argon2id$v=19$m=...,t=...,p=...$salt$hash)
// so parameters can be raised later: Verify reports when a stored hash uses
// weaker parameters than the current ones and should be re-hashed.
package password

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Params of argon2id. Defaults follow the OWASP recommendation (19 MiB, t=2, p=1).
type Params struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
	SaltLen     uint32
	KeyLen      uint32
}

var DefaultParams = Params{MemoryKiB: 19 * 1024, Iterations: 2, Parallelism: 1, SaltLen: 16, KeyLen: 32}

var ErrMalformedHash = errors.New("password: malformed hash")

// Hasher limits concurrent hashing: each one allocates MemoryKiB, so a burst of
// logins can't exhaust memory.
type Hasher struct {
	params Params
	slots  chan struct{}
	dummy  string
}

// NewHasher: maxConcurrent <= 0 means runtime.NumCPU().
func NewHasher(p Params, maxConcurrent int) *Hasher {
	if maxConcurrent <= 0 {
		maxConcurrent = runtime.NumCPU()
	}
	h := &Hasher{params: p, slots: make(chan struct{}, maxConcurrent)}
	h.dummy, _ = h.Hash(context.Background(), "dummy password for timing equalization")
	return h
}

func (h *Hasher) acquire(ctx context.Context) error {
	select {
	case h.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Hasher) release() { <-h.slots }

func (h *Hasher) Hash(ctx context.Context, password string) (string, error) {
	salt := make([]byte, h.params.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, h.params.Iterations, h.params.MemoryKiB, h.params.Parallelism, h.params.KeyLen)
	h.release()

	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, h.params.MemoryKiB, h.params.Iterations, h.params.Parallelism,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// Verify compares password with a stored hash in constant time. needsRehash is
// true when the hash was made with different parameters than the current ones.
func (h *Hasher) Verify(ctx context.Context, password, encoded string) (ok, needsRehash bool, err error) {
	p, salt, want, err := decode(encoded)
	if err != nil {
		return false, false, err
	}
	if err := h.acquire(ctx); err != nil {
		return false, false, err
	}
	got := argon2.IDKey([]byte(password), salt, p.Iterations, p.MemoryKiB, p.Parallelism, uint32(len(want)))
	h.release()

	if subtle.ConstantTimeCompare(got, want) != 1 {
		return false, false, nil
	}
	needsRehash = p.MemoryKiB != h.params.MemoryKiB || p.Iterations != h.params.Iterations ||
		p.Parallelism != h.params.Parallelism || uint32(len(want)) != h.params.KeyLen
	return true, needsRehash, nil
}

// VerifyDummy spends the same time as a real verification. Call it when the
// account doesn't exist so response times don't reveal which emails are registered.
func (h *Hasher) VerifyDummy(ctx context.Context, password string) {
	_, _, _ = h.Verify(ctx, password, h.dummy)
}

func decode(encoded string) (Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash
	if len(parts) != 6 || parts[1] != "argon2id" {
		return Params{}, nil, nil, ErrMalformedHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return Params{}, nil, nil, ErrMalformedHash
	}
	var p Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.MemoryKiB, &p.Iterations, &p.Parallelism); err != nil {
		return Params{}, nil, nil, ErrMalformedHash
	}
	// refuse absurd parameters from a tampered row (memory DoS)
	if p.MemoryKiB == 0 || p.MemoryKiB > 1<<20 || p.Iterations == 0 || p.Iterations > 16 || p.Parallelism == 0 {
		return Params{}, nil, nil, ErrMalformedHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return Params{}, nil, nil, ErrMalformedHash
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) < 16 {
		return Params{}, nil, nil, ErrMalformedHash
	}
	return p, salt, key, nil
}
