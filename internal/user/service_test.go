package user

import (
	"strings"
	"testing"
)

func TestValidatePassword(t *testing.T) {
	cases := []struct {
		name    string
		pw      string
		wantErr bool
	}{
		{"too short", "short", true},
		{"minimum", "12345678", false},
		{"normal", "correct horse battery staple", false},
		{"too long", strings.Repeat("x", 257), true},
		{"max length ok", strings.Repeat("x", 256), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePassword(tc.pw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validatePassword(%q) err=%v, want err=%v", tc.pw, err, tc.wantErr)
			}
		})
	}
}

func TestHashResetToken_Deterministic(t *testing.T) {
	a := HashResetToken("abc123")
	b := HashResetToken("abc123")
	if a != b {
		t.Fatal("hash must be deterministic for the same input")
	}
	if HashResetToken("abc124") == a {
		t.Fatal("hash must differ for different input")
	}
	// sha256 hex = 64 chars
	if len(a) != 64 {
		t.Fatalf("hash length = %d, want 64", len(a))
	}
}

func TestNewResetToken_RawIsHex(t *testing.T) {
	raw, hash, err := newResetToken()
	if err != nil {
		t.Fatalf("newResetToken: %v", err)
	}
	// 32 random bytes → 64 hex chars
	if len(raw) != 64 {
		t.Fatalf("raw len = %d, want 64", len(raw))
	}
	if HashResetToken(raw) != hash {
		t.Fatal("returned hash does not match HashResetToken(raw)")
	}
	// Two successive calls must produce distinct tokens.
	raw2, _, _ := newResetToken()
	if raw == raw2 {
		t.Fatal("two successive tokens collided — entropy broken")
	}
}
