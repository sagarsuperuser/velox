package session

import (
	"encoding/hex"
	"testing"
)

func TestNewID_Format(t *testing.T) {
	raw, hash, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	// 32 random bytes → 64 hex chars
	if len(raw) != 64 {
		t.Fatalf("raw len = %d, want 64", len(raw))
	}
	if _, err := hex.DecodeString(raw); err != nil {
		t.Fatalf("raw is not hex: %v", err)
	}
	// sha256 hex = 64 chars
	if len(hash) != 64 {
		t.Fatalf("hash len = %d, want 64", len(hash))
	}
	if HashID(raw) != hash {
		t.Fatal("returned hash does not match HashID(raw)")
	}
}

func TestNewID_DistinctOutputs(t *testing.T) {
	seen := make(map[string]struct{}, 32)
	for i := range 32 {
		raw, _, err := NewID()
		if err != nil {
			t.Fatalf("NewID: %v", err)
		}
		if _, dup := seen[raw]; dup {
			t.Fatalf("collision at iter %d — entropy broken", i)
		}
		seen[raw] = struct{}{}
	}
}

func TestHashID_Deterministic(t *testing.T) {
	a := HashID("abc")
	b := HashID("abc")
	if a != b {
		t.Fatal("HashID must be deterministic")
	}
	if HashID("abcd") == a {
		t.Fatal("HashID must differ for different inputs")
	}
}
