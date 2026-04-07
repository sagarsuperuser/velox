package postgres

import (
	"strings"
	"testing"
)

func TestNewID(t *testing.T) {
	id := NewID("vlx_cus")
	if !strings.HasPrefix(id, "vlx_cus_") {
		t.Errorf("ID should start with vlx_cus_, got %q", id)
	}
	if len(id) < 20 {
		t.Errorf("ID too short: %q", id)
	}

	// IDs should be unique
	id2 := NewID("vlx_cus")
	if id == id2 {
		t.Error("IDs should be unique")
	}
}

func TestNewID_DifferentPrefixes(t *testing.T) {
	prefixes := []string{"vlx_ten", "vlx_cus", "vlx_inv", "vlx_sub", "vlx_key"}
	for _, p := range prefixes {
		id := NewID(p)
		if !strings.HasPrefix(id, p+"_") {
			t.Errorf("prefix %q: got %q", p, id)
		}
	}
}

func TestNullableString(t *testing.T) {
	if NullableString("hello") != "hello" {
		t.Error("non-empty string should return as-is")
	}
	if NullableString("") != nil {
		t.Error("empty string should return nil")
	}
	if NullableString("   ") != nil {
		t.Error("whitespace string should return nil")
	}
}

func TestIsUniqueViolation(t *testing.T) {
	if IsUniqueViolation(nil) {
		t.Error("nil should not be unique violation")
	}
}
