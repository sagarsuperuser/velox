package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

const encPrefix = "enc:"

// Encryptor provides AES-256-GCM encryption for PII fields.
// A nil-key encryptor (created via NewNoop) passes values through unchanged.
type Encryptor struct {
	key []byte // nil = noop
}

// NewEncryptor creates an encryptor from a 64-character hex-encoded key (32 bytes).
func NewEncryptor(hexKey string) (*Encryptor, error) {
	if len(hexKey) != 64 {
		return nil, fmt.Errorf("encryption key must be exactly 64 hex characters (32 bytes), got %d", len(hexKey))
	}
	key, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("encryption key is not valid hex: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("decoded key must be 32 bytes, got %d", len(key))
	}
	return &Encryptor{key: key}, nil
}

// NewNoop returns an encryptor that passes values through without encryption.
// Used when VELOX_ENCRYPTION_KEY is not set (dev / migration).
func NewNoop() *Encryptor {
	return &Encryptor{key: nil}
}

// IsEnabled returns true when encryption is active.
func (e *Encryptor) IsEnabled() bool {
	return e != nil && e.key != nil
}

// Encrypt encrypts plaintext using AES-256-GCM and returns "enc:<base64(nonce+ciphertext)>".
// Empty strings are returned as-is (no point encrypting empty values).
// When encryption is disabled (noop), returns plaintext unchanged.
func (e *Encryptor) Encrypt(plaintext string) (string, error) {
	if !e.IsEnabled() {
		return plaintext, nil
	}
	if plaintext == "" {
		return "", nil
	}

	block, err := aes.NewCipher(e.key)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	encoded := base64.StdEncoding.EncodeToString(ciphertext)
	return encPrefix + encoded, nil
}

// Decrypt decrypts a value. If the value has an "enc:" prefix it is decrypted;
// otherwise it is returned as-is (plaintext passthrough for migration compatibility).
func (e *Encryptor) Decrypt(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if !strings.HasPrefix(value, encPrefix) {
		// Not encrypted — return as-is (backward compat during migration)
		return value, nil
	}
	if !e.IsEnabled() {
		// Encrypted data but no key — cannot decrypt
		return "", fmt.Errorf("encrypted value found but no encryption key configured")
	}

	encoded := strings.TrimPrefix(value, encPrefix)
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode base64: %w", err)
	}

	block, err := aes.NewCipher(e.key)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}

	return string(plaintext), nil
}
