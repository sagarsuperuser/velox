package session

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// NewID mints a cryptographically-random session identifier and returns
// both the raw form (for the cookie) and the sha256 hash (for DB storage).
// A DB snapshot therefore never yields bearer-capable tokens.
//
// 32 bytes → 64-char hex → ~256 bits of entropy, well past the birthday
// bound for any realistic session volume.
func NewID() (raw, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("session: read rand: %w", err)
	}
	raw = hex.EncodeToString(buf)
	hash = HashID(raw)
	return raw, hash, nil
}

// HashID hashes a raw session ID the same way NewID stores it. Exported so
// middleware can hash the cookie value for lookup.
func HashID(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}
