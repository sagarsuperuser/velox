package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

type memStore struct {
	mu   sync.RWMutex
	keys map[string]domain.APIKey
}

func newMemStore() *memStore {
	return &memStore{keys: make(map[string]domain.APIKey)}
}

func (m *memStore) Create(_ context.Context, key domain.APIKey) (domain.APIKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key.CreatedAt = time.Now().UTC()
	m.keys[key.ID] = key
	return key, nil
}

func (m *memStore) GetByPrefix(_ context.Context, prefix string) (domain.APIKey, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, k := range m.keys {
		if k.KeyPrefix == prefix && k.RevokedAt == nil {
			return k, nil
		}
	}
	return domain.APIKey{}, errs.ErrNotFound
}

func (m *memStore) Revoke(_ context.Context, tenantID, id string) (domain.APIKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.keys[id]
	if !ok || k.TenantID != tenantID {
		return domain.APIKey{}, errs.ErrNotFound
	}
	now := time.Now().UTC()
	k.RevokedAt = &now
	m.keys[id] = k
	return k, nil
}

func (m *memStore) List(_ context.Context, filter ListFilter) ([]domain.APIKey, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []domain.APIKey
	for _, k := range m.keys {
		if k.TenantID == filter.TenantID {
			result = append(result, k)
		}
	}
	return result, nil
}

func (m *memStore) TouchLastUsed(_ context.Context, id string, usedAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.keys[id]
	if !ok {
		return errs.ErrNotFound
	}
	k.LastUsedAt = &usedAt
	m.keys[id] = k
	return nil
}

func TestCreateKey_Secret(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()

	result, err := svc.CreateKey(ctx, "tenant1", CreateKeyInput{
		Name:    "Backend Key",
		KeyType: KeyTypeSecret,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.HasPrefix(result.RawKey, "vlx_secret_") {
		t.Errorf("raw key should start with vlx_secret_, got prefix: %q", result.RawKey[:15])
	}
	if len(result.RawKey) != len("vlx_secret_")+64 {
		t.Errorf("raw key length: got %d, want %d", len(result.RawKey), len("vlx_secret_")+64)
	}
	if result.Key.KeyType != "secret" {
		t.Errorf("key_type: got %q, want secret", result.Key.KeyType)
	}
	if result.Key.TenantID != "tenant1" {
		t.Errorf("tenant_id: got %q", result.Key.TenantID)
	}

	// Verify salted hash: SHA-256(salt + rawKey)
	if result.Key.KeySalt == "" {
		t.Fatal("key salt should not be empty")
	}
	salt, err := hex.DecodeString(result.Key.KeySalt)
	if err != nil {
		t.Fatalf("decode salt: %v", err)
	}
	hash := sha256.Sum256(append(salt, []byte(result.RawKey)...))
	if result.Key.KeyHash != hex.EncodeToString(hash[:]) {
		t.Error("salted hash mismatch")
	}
}

func TestCreateKey_Publishable(t *testing.T) {
	svc := NewService(newMemStore())

	result, err := svc.CreateKey(context.Background(), "tenant1", CreateKeyInput{
		Name:    "Frontend Key",
		KeyType: KeyTypePublishable,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(result.RawKey, "vlx_pub_") {
		t.Errorf("raw key should start with vlx_pub_")
	}
	if result.Key.KeyType != "publishable" {
		t.Errorf("key_type: got %q, want publishable", result.Key.KeyType)
	}
}

func TestCreateKey_Platform(t *testing.T) {
	svc := NewService(newMemStore())

	result, err := svc.CreateKey(context.Background(), "tenant1", CreateKeyInput{
		Name:    "Platform Key",
		KeyType: KeyTypePlatform,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(result.RawKey, "vlx_platform_") {
		t.Errorf("raw key should start with vlx_platform_")
	}
	if result.Key.KeyType != "platform" {
		t.Errorf("key_type: got %q, want platform", result.Key.KeyType)
	}
}

func TestCreateKey_DefaultsToSecret(t *testing.T) {
	svc := NewService(newMemStore())

	result, _ := svc.CreateKey(context.Background(), "t1", CreateKeyInput{Name: "Default"})
	if result.Key.KeyType != "secret" {
		t.Errorf("default key_type: got %q, want secret", result.Key.KeyType)
	}
}

func TestCreateKey_InvalidType(t *testing.T) {
	svc := NewService(newMemStore())

	_, err := svc.CreateKey(context.Background(), "t1", CreateKeyInput{
		Name:    "Bad",
		KeyType: "admin",
	})
	if err == nil {
		t.Fatal("expected error for invalid key type")
	}
}

func TestCreateKey_MissingName(t *testing.T) {
	svc := NewService(newMemStore())
	_, err := svc.CreateKey(context.Background(), "t1", CreateKeyInput{})
	if err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestValidateKey_AllTypes(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()

	types := []KeyType{KeyTypeSecret, KeyTypePublishable, KeyTypePlatform}
	for _, kt := range types {
		t.Run(string(kt), func(t *testing.T) {
			result, _ := svc.CreateKey(ctx, "tenant1", CreateKeyInput{
				Name:    "Test " + string(kt),
				KeyType: kt,
			})

			key, err := svc.ValidateKey(ctx, result.RawKey)
			if err != nil {
				t.Fatalf("validate %s key: %v", kt, err)
			}
			if key.TenantID != "tenant1" {
				t.Errorf("tenant_id: got %q", key.TenantID)
			}
			if key.KeyType != string(kt) {
				t.Errorf("key_type: got %q, want %q", key.KeyType, kt)
			}
		})
	}
}

func TestValidateKey_InvalidFormat(t *testing.T) {
	svc := NewService(newMemStore())

	cases := []string{
		"",
		"sk_live_invalid",
		"vlx_secret_tooshort",
		"vlx_unknown_" + strings.Repeat("ab", 32),
	}
	for _, raw := range cases {
		_, err := svc.ValidateKey(context.Background(), raw)
		if err == nil {
			t.Errorf("expected error for key %q", raw)
		}
	}
}

func TestValidateKey_WrongSecret(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()

	result, _ := svc.CreateKey(ctx, "t1", CreateKeyInput{Name: "Real", KeyType: KeyTypeSecret})

	// Same prefix length but different secret
	fake := "vlx_secret_" + strings.Repeat("ff", 32)
	if fake == result.RawKey {
		t.Skip("astronomically unlikely collision")
	}

	_, err := svc.ValidateKey(ctx, fake)
	if err == nil {
		t.Fatal("expected error for wrong secret")
	}
}

func TestValidateKey_Expired(t *testing.T) {
	svc := NewService(newMemStore())
	past := time.Now().UTC().Add(-24 * time.Hour)

	result, _ := svc.CreateKey(context.Background(), "t1", CreateKeyInput{
		Name:      "Expired",
		KeyType:   KeyTypeSecret,
		ExpiresAt: &past,
	})

	_, err := svc.ValidateKey(context.Background(), result.RawKey)
	if err == nil {
		t.Fatal("expected error for expired key")
	}
}

func TestValidateKey_Revoked(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()

	result, _ := svc.CreateKey(ctx, "t1", CreateKeyInput{Name: "Revokable", KeyType: KeyTypeSecret})
	_, _ = svc.RevokeKey(ctx, "t1", result.Key.ID)

	_, err := svc.ValidateKey(ctx, result.RawKey)
	if err == nil {
		t.Fatal("expected error for revoked key")
	}
}

func TestRevokeKey_WrongTenant(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()

	result, _ := svc.CreateKey(ctx, "t1", CreateKeyInput{Name: "Test", KeyType: KeyTypeSecret})
	_, err := svc.RevokeKey(ctx, "wrong_tenant", result.Key.ID)
	if err == nil {
		t.Fatal("expected error revoking from wrong tenant")
	}
}
