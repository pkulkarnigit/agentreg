// Package auth handles password hashing and API token generation/hashing.
// It is independent of internal/api so it can be called from anywhere
// (server, CLI, a future admin tool) without pulling in HTTP.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"

	"golang.org/x/crypto/bcrypt"
)

var usernameRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$`)

// ValidUsername enforces the charset the registry uses for scopes:
// lowercase alphanumeric and hyphens, 1-40 chars, since a username becomes
// the `{scope}` path segment and the `@scope/name` display form.
func ValidUsername(u string) bool {
	return usernameRE.MatchString(u)
}

// HashPassword bcrypt-hashes a plaintext password for storage.
func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// CheckPassword reports whether password matches the stored bcrypt hash.
func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// NewRandomToken generates a high-entropy random token (32 bytes, hex
// encoded). Used as the base for API tokens, email-verification tokens, and
// password-reset tokens alike — only its sha256 hash (HashToken) should
// ever be persisted.
func NewRandomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// NewAPIToken generates a random API token, prefixed so it's visually
// distinguishable from other token kinds (e.g. in logs). The plaintext is
// returned once (to hand to the caller); only its sha256 hash should ever
// be persisted.
func NewAPIToken() (plaintext string, err error) {
	tok, err := NewRandomToken()
	if err != nil {
		return "", err
	}
	return "apreg_" + tok, nil
}

// HashToken returns the sha256 hash (hex) of a token's plaintext, for
// storage and lookup. Tokens are high-entropy random values, not
// user-chosen secrets, so a fast hash (vs. bcrypt) is appropriate here.
func HashToken(plaintext string) string {
	h := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(h[:])
}
