package user

import (
	"strings"
	"testing"
)

func TestHashPassword_RoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=") {
		t.Fatalf("hash prefix wrong: %q", hash)
	}

	ok, err := VerifyPassword("correct horse battery staple", hash)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !ok {
		t.Fatal("verify should succeed for correct password")
	}
}

func TestHashPassword_DifferentSaltsSameInput(t *testing.T) {
	a, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("hash a: %v", err)
	}
	b, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("hash b: %v", err)
	}
	if a == b {
		t.Fatal("two hashes of the same password must differ (salt randomness)")
	}
}

func TestVerifyPassword_WrongPassword(t *testing.T) {
	hash, err := HashPassword("correct")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	ok, err := VerifyPassword("wrong", hash)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if ok {
		t.Fatal("verify should fail for wrong password")
	}
}

func TestVerifyPassword_MalformedHash(t *testing.T) {
	cases := map[string]string{
		"empty":              "",
		"not PHC":            "plaintext",
		"wrong algo":         "$bcrypt$v=19$m=1,t=1,p=1$aa$bb",
		"truncated sections": "$argon2id$v=19$m=65536,t=3,p=4$onlysalt",
		"non-int version":    "$argon2id$v=xx$m=65536,t=3,p=4$aa$bb",
		"non-int params":     "$argon2id$v=19$m=bad,t=3,p=4$aa$bb",
		"bad salt b64":       "$argon2id$v=19$m=65536,t=3,p=4$!!!notb64$bb",
		"bad hash b64":       "$argon2id$v=19$m=65536,t=3,p=4$aaaa$!!!notb64",
	}
	for name, stored := range cases {
		t.Run(name, func(t *testing.T) {
			ok, err := VerifyPassword("anything", stored)
			if ok {
				t.Fatal("malformed hash must not validate")
			}
			if err == nil {
				t.Fatal("malformed hash must return error")
			}
		})
	}
}

func TestHashPassword_EmptyRejected(t *testing.T) {
	if _, err := HashPassword(""); err == nil {
		t.Fatal("empty password must error")
	}
}
