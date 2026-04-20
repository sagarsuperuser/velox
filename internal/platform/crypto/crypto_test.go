package crypto

import (
	"encoding/hex"
	"strings"
	"testing"
)

func testKey(t *testing.T) string {
	t.Helper()
	// Fixed 32-byte key for deterministic test setup
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	return hex.EncodeToString(key)
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	enc, err := NewEncryptor(testKey(t))
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}

	cases := []string{
		"hello@example.com",
		"John Doe",
		"+1-555-0100",
		"US12345678",
		"a",                        // single char
		"unicode: 日本語テスト 🎉",        // multi-byte
		strings.Repeat("x", 10000), // large value
	}

	for _, plaintext := range cases {
		encrypted, err := enc.Encrypt(plaintext)
		if err != nil {
			t.Fatalf("Encrypt(%q): %v", plaintext, err)
		}
		if !strings.HasPrefix(encrypted, "enc:") {
			t.Errorf("encrypted value should have enc: prefix, got %q", encrypted)
		}
		if encrypted == plaintext {
			t.Errorf("encrypted value should differ from plaintext")
		}

		decrypted, err := enc.Decrypt(encrypted)
		if err != nil {
			t.Fatalf("Decrypt: %v", err)
		}
		if decrypted != plaintext {
			t.Errorf("round-trip failed: got %q, want %q", decrypted, plaintext)
		}
	}
}

func TestDecryptPlaintext(t *testing.T) {
	enc, err := NewEncryptor(testKey(t))
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}

	// Plaintext without "enc:" prefix should pass through unchanged
	plain := "not-encrypted@example.com"
	result, err := enc.Decrypt(plain)
	if err != nil {
		t.Fatalf("Decrypt plaintext: %v", err)
	}
	if result != plain {
		t.Errorf("got %q, want %q", result, plain)
	}
}

func TestNoopEncryptor(t *testing.T) {
	noop := NewNoop()

	if noop.IsEnabled() {
		t.Error("noop should not be enabled")
	}

	val := "sensitive@example.com"
	encrypted, err := noop.Encrypt(val)
	if err != nil {
		t.Fatalf("noop Encrypt: %v", err)
	}
	if encrypted != val {
		t.Errorf("noop Encrypt should pass through: got %q, want %q", encrypted, val)
	}

	decrypted, err := noop.Decrypt(val)
	if err != nil {
		t.Fatalf("noop Decrypt: %v", err)
	}
	if decrypted != val {
		t.Errorf("noop Decrypt should pass through: got %q, want %q", decrypted, val)
	}
}

func TestInvalidKey(t *testing.T) {
	// Too short
	if _, err := NewEncryptor("abcd"); err == nil {
		t.Error("expected error for short key")
	}

	// Not hex
	if _, err := NewEncryptor(strings.Repeat("zz", 32)); err == nil {
		t.Error("expected error for non-hex key")
	}

	// Wrong length (48 hex chars = 24 bytes, not 32)
	if _, err := NewEncryptor(strings.Repeat("ab", 24)); err == nil {
		t.Error("expected error for 24-byte key")
	}

	// Correct length
	if _, err := NewEncryptor(strings.Repeat("ab", 32)); err != nil {
		t.Errorf("valid key should succeed: %v", err)
	}
}

func TestEmptyString(t *testing.T) {
	enc, err := NewEncryptor(testKey(t))
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}

	encrypted, err := enc.Encrypt("")
	if err != nil {
		t.Fatalf("Encrypt empty: %v", err)
	}
	if encrypted != "" {
		t.Errorf("encrypting empty string should return empty, got %q", encrypted)
	}

	decrypted, err := enc.Decrypt("")
	if err != nil {
		t.Fatalf("Decrypt empty: %v", err)
	}
	if decrypted != "" {
		t.Errorf("decrypting empty string should return empty, got %q", decrypted)
	}
}

func TestUniqueNonces(t *testing.T) {
	enc, err := NewEncryptor(testKey(t))
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}

	plaintext := "same-value@example.com"
	seen := make(map[string]bool)

	for i := 0; i < 100; i++ {
		encrypted, err := enc.Encrypt(plaintext)
		if err != nil {
			t.Fatalf("Encrypt: %v", err)
		}
		if seen[encrypted] {
			t.Fatalf("duplicate ciphertext on iteration %d — nonces are not unique", i)
		}
		seen[encrypted] = true
	}
}

func TestNoopDecryptEncryptedValue(t *testing.T) {
	// If a noop encryptor encounters an "enc:" value, it should error
	// because it cannot decrypt without a key.
	noop := NewNoop()
	_, err := noop.Decrypt("enc:somedata")
	if err == nil {
		t.Error("expected error when noop tries to decrypt encrypted value")
	}
}

// TestBlinderDeterministicAndKeyed locks down the two properties the
// magic-link lookup relies on: identical inputs produce identical blind
// indexes (so the lookup works at all), and different keys produce
// different blind indexes (so the HMAC key is the only oracle).
func TestBlinderDeterministicAndKeyed(t *testing.T) {
	b1, err := NewBlinder(testKey(t))
	if err != nil {
		t.Fatalf("NewBlinder: %v", err)
	}

	// Deterministic: same input → same output.
	a := b1.Blind("hello@example.com")
	if a == "" {
		t.Fatalf("expected non-empty blind index")
	}
	if b1.Blind("hello@example.com") != a {
		t.Fatalf("blind index is not deterministic")
	}

	// Different input → different output (collision resistance of HMAC).
	if b1.Blind("other@example.com") == a {
		t.Fatalf("different inputs produced identical blind index")
	}

	// Different key → different output even for the same input. This is
	// what prevents an attacker who only sees email_bidx values from
	// reversing them without the key.
	altKey := make([]byte, 32)
	for i := range altKey {
		altKey[i] = byte(255 - i)
	}
	b2, err := NewBlinder(hex.EncodeToString(altKey))
	if err != nil {
		t.Fatalf("NewBlinder alt: %v", err)
	}
	if b2.Blind("hello@example.com") == a {
		t.Fatalf("different keys produced identical blind index")
	}
}

// TestBlinderNoopAndEmptyInputs — an unconfigured blinder or an empty input
// both safely return "". The customer store relies on this: a NULL/empty
// email_bidx is never queryable, so misconfigured environments silently
// fail closed instead of leaking rows through a blank-string match.
func TestBlinderNoopAndEmptyInputs(t *testing.T) {
	if NewNoopBlinder().Blind("anything") != "" {
		t.Errorf("noop blinder must return empty string")
	}
	if NewNoopBlinder().IsEnabled() {
		t.Errorf("noop blinder must report !IsEnabled")
	}

	b, err := NewBlinder(testKey(t))
	if err != nil {
		t.Fatalf("NewBlinder: %v", err)
	}
	if got := b.Blind(""); got != "" {
		t.Errorf("empty input must yield empty index, got %q", got)
	}
}
