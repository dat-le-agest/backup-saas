package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"strings"

	"core/apperr"
)

var userIDPattern = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)

func validateUserID(id string) error {
	if !userIDPattern.MatchString(id) {
		return apperr.Invalid("user_id", "must be 1-64 chars of A-Za-z0-9._@-")
	}
	return nil
}

func validateTenantName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 255 {
		return "", apperr.Invalid("name", "must be 1-255 characters")
	}
	return name, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newTenantID() string { return "tnt_" + randomHex(8) }

// newRefreshToken returns the opaque token for the client and the hash to store.
// newRefreshToken returns "<app>.<random>": the prefix tells a refresh or a
// logout which app's schema holds the token. Only the hash is stored.
func newRefreshToken(appID string) (raw, hash string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	raw = appID + "." + base64.RawURLEncoding.EncodeToString(b)
	return raw, hashRefreshToken(raw)
}

// refreshTokenApp is the app prefix of a raw refresh token, "" if malformed.
func refreshTokenApp(raw string) string {
	app, rest, ok := strings.Cut(raw, ".")
	if !ok || rest == "" || !appIDPattern.MatchString(app) {
		return ""
	}
	return app
}

func hashRefreshToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
