// Package keys issues and checks the API keys this service owns.
//
// Why keys live here and not in the auth service: the auth service authenticates people (sessions,
// roles inside organisations). A product API key is a different job: a long random secret shown
// once, stored only as a hash, with scopes, an expiry and usage metering. The check is exposed as an
// auth.Introspector, so any other source of keys can replace this one without touching the API.
package keys

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// Valid scopes. The database enforces the same list (migration 000008).
var Scopes = []string{"geocode", "normalize", "corrections:write"}

const (
	prefix      = "gk_"
	keyIDLen    = 8  // hex characters
	secretLen   = 64 // hex characters = 256 bits
	fullKeyLen  = len(prefix) + keyIDLen + 1 + secretLen
	secretBytes = secretLen / 2
)

// Generate returns a new key, its public id and the SHA-256 that is stored. The key itself is
// never stored: show it to the user once.
func Generate() (full, keyID string, hash [32]byte, err error) {
	id := make([]byte, keyIDLen/2)
	sec := make([]byte, secretBytes)
	if _, err = rand.Read(id); err != nil {
		return
	}
	if _, err = rand.Read(sec); err != nil {
		return
	}
	keyID = hex.EncodeToString(id)
	full = prefix + keyID + "_" + hex.EncodeToString(sec)
	hash = Hash(full)
	return
}

// Hash is what is stored for a key.
func Hash(full string) [32]byte { return sha256.Sum256([]byte(full)) }

// ErrMalformed means the text is not shaped like one of our keys.
var ErrMalformed = errors.New("malformed api key")

// Parse checks the shape of a key and returns its public id. Anything that is not exactly
// gk_<8 hex>_<64 hex> is rejected without touching the database.
func Parse(full string) (keyID string, err error) {
	if len(full) != fullKeyLen || !strings.HasPrefix(full, prefix) || full[len(prefix)+keyIDLen] != '_' {
		return "", ErrMalformed
	}
	id := full[len(prefix) : len(prefix)+keyIDLen]
	sec := full[len(prefix)+keyIDLen+1:]
	if !isLowerHex(id) || !isLowerHex(sec) {
		return "", ErrMalformed
	}
	return id, nil
}

func isLowerHex(s string) bool {
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// ValidScope reports whether s is a scope we issue.
func ValidScope(s string) bool {
	for _, x := range Scopes {
		if x == s {
			return true
		}
	}
	return false
}
