// Package auth turns a credential into a caller identity. It owns key format, hashing
// and scope checks; it does not know about HTTP.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Key format: a prefix that identifies the environment, then the secret. The prefix is
// stored in the clear so a leaked key can be traced to its row; the secret never is.
const (
	PrefixLive = "zt_live_"
	PrefixTest = "zt_test_"

	// secretBytes is 256 bits of entropy (.claude/rules/security.md).
	secretBytes = 32
	// storedPrefixChars is how much of the encoded secret is kept alongside the prefix
	// so an operator can recognise a key in a list without holding the secret.
	storedPrefixChars = 8
)

// ErrMalformedKey means the presented credential is not a ViTTS API key at all.
var ErrMalformedKey = errors.New("malformed api key")

// Generated is a freshly minted key. Secret is returned to the caller exactly once and
// is never stored, logged, or recoverable afterwards.
type Generated struct {
	Secret string
	Hash   []byte
	Prefix string
}

// Generate mints a new key for the given environment prefix.
func Generate(prefix string) (Generated, error) {
	if prefix != PrefixLive && prefix != PrefixTest {
		return Generated{}, fmt.Errorf("%w: unknown prefix %q", ErrMalformedKey, prefix)
	}

	raw := make([]byte, secretBytes)
	if _, err := rand.Read(raw); err != nil {
		return Generated{}, fmt.Errorf("read entropy: %w", err)
	}

	secret := prefix + base64.RawURLEncoding.EncodeToString(raw)
	return Generated{
		Secret: secret,
		Hash:   Hash(secret),
		Prefix: StoredPrefix(secret),
	}, nil
}

// Hash is the value stored in api_keys.key_hash. Lookup is by this digest, so the plain
// secret never reaches the database or a query log.
func Hash(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}

// StoredPrefix is the human-recognisable fragment kept next to the hash, e.g.
// "zt_live_ab12cd34". It is not a credential and is safe to display.
func StoredPrefix(secret string) string {
	prefix, rest, ok := splitPrefix(secret)
	if !ok {
		return ""
	}
	if len(rest) > storedPrefixChars {
		rest = rest[:storedPrefixChars]
	}
	return prefix + rest
}

// ParseBearer extracts the secret from an Authorization header value.
//
// It validates shape only. A well-formed key that does not exist still fails later, and
// both paths must look the same to a caller probing for valid prefixes.
func ParseBearer(header string) (string, error) {
	const scheme = "bearer "

	if len(header) < len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return "", fmt.Errorf("%w: not a bearer credential", ErrMalformedKey)
	}
	secret := strings.TrimSpace(header[len(scheme):])
	if _, _, ok := splitPrefix(secret); !ok {
		return "", fmt.Errorf("%w: unknown key prefix", ErrMalformedKey)
	}
	return secret, nil
}

// EqualHash compares two digests in constant time, so a caller cannot learn a stored
// hash by timing repeated attempts.
func EqualHash(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}

// FingerprintForLogs is the only representation of a key that may appear in a log line:
// the first bytes of the digest, never the secret and never the full hash.
func FingerprintForLogs(hash []byte) string {
	const fingerprintBytes = 6
	if len(hash) < fingerprintBytes {
		return ""
	}
	return hex.EncodeToString(hash[:fingerprintBytes])
}

func splitPrefix(secret string) (prefix, rest string, ok bool) {
	for _, candidate := range []string{PrefixLive, PrefixTest} {
		if strings.HasPrefix(secret, candidate) {
			return candidate, secret[len(candidate):], len(secret) > len(candidate)
		}
	}
	return "", "", false
}
