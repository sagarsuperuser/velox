package user

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters chosen against OWASP 2024 guidance for general server
// use. 64 MiB / 3 iterations / 4 parallel takes ~30ms on a modern core —
// slow enough to bite a brute-forcer, fast enough to not queue up logins.
// The values live in the hash string itself, so tuning them later does not
// invalidate existing users.
const (
	argonMemoryKiB  = 64 * 1024
	argonIterations = 3
	argonParallel   = 4
	argonSaltLen    = 16
	argonKeyLen     = 32
)

var (
	errInvalidHashFormat = errors.New("invalid password hash format")
	errUnsupportedAlgo   = errors.New("unsupported password hash algorithm")
)

// HashPassword returns a self-describing PHC-format string
// ($argon2id$v=19$m=...,t=...,p=...$<salt>$<hash>) suitable for storage in
// users.password_hash.
func HashPassword(plaintext string) (string, error) {
	if plaintext == "" {
		return "", errors.New("password must not be empty")
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("read salt: %w", err)
	}
	key := argon2.IDKey([]byte(plaintext), salt, argonIterations, argonMemoryKiB, argonParallel, argonKeyLen)
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		argonMemoryKiB, argonIterations, argonParallel,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword runs constant-time comparison of the plaintext against a
// stored PHC hash. A mismatch or a malformed hash both return false — the
// caller should not branch on the specific failure reason.
func VerifyPassword(plaintext, stored string) (bool, error) {
	parts := strings.Split(stored, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errUnsupportedAlgo
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, errInvalidHashFormat
	}
	var memKiB, iters uint32
	var parallel uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memKiB, &iters, &parallel); err != nil {
		return false, errInvalidHashFormat
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, errInvalidHashFormat
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, errInvalidHashFormat
	}
	got := argon2.IDKey([]byte(plaintext), salt, iters, memKiB, parallel, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
