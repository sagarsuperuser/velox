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

func (m *memStore) Get(_ context.Context, tenantID, id string) (domain.APIKey, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	k, ok := m.keys[id]
	if !ok || k.TenantID != tenantID {
		return domain.APIKey{}, errs.ErrNotFound
	}
	return k, nil
}

func (m *memStore) ScheduleExpiry(_ context.Context, tenantID, id string, expiresAt time.Time) (domain.APIKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.keys[id]
	if !ok || k.TenantID != tenantID || k.RevokedAt != nil {
		return domain.APIKey{}, errs.ErrNotFound
	}
	k.ExpiresAt = &expiresAt
	m.keys[id] = k
	return k, nil
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

// Revoke mirrors PostgresStore.Revoke's safeguard: refuse if revoking
// a *currently-active* secret/platform key would leave the tenant
// with zero such keys. An already-expired key is allowed to be
// revoked even when no other active secret/platform keys exist —
// it's already dead, revoking is just cleanup.
func (m *memStore) Revoke(_ context.Context, tenantID, id string) (domain.APIKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	target, ok := m.keys[id]
	if !ok || target.TenantID != tenantID || target.RevokedAt != nil {
		return domain.APIKey{}, errs.ErrNotFound
	}
	now := time.Now().UTC()
	targetActive := target.ExpiresAt == nil || target.ExpiresAt.After(now)
	if targetActive && (target.KeyType == "secret" || target.KeyType == "platform") {
		remaining := 0
		for _, k := range m.keys {
			if k.TenantID != tenantID || k.ID == id {
				continue
			}
			if k.RevokedAt != nil {
				continue
			}
			if k.ExpiresAt != nil && k.ExpiresAt.Before(now) {
				continue
			}
			if k.KeyType == "secret" || k.KeyType == "platform" {
				remaining++
			}
		}
		if remaining == 0 {
			return domain.APIKey{}, errs.InvalidState(
				"cannot revoke the last active secret/platform key — create another first")
		}
	}
	target.RevokedAt = &now
	m.keys[id] = target
	return target, nil
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

	if !strings.HasPrefix(result.RawKey, "vlx_secret_live_") {
		t.Errorf("raw key should start with vlx_secret_live_, got prefix: %q", result.RawKey[:16])
	}
	if len(result.RawKey) != len("vlx_secret_live_")+64 {
		t.Errorf("raw key length: got %d, want %d", len(result.RawKey), len("vlx_secret_live_")+64)
	}
	if !result.Key.Livemode {
		t.Error("key should be live by default")
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
	if !strings.HasPrefix(result.RawKey, "vlx_pub_live_") {
		t.Errorf("raw key should start with vlx_pub_live_")
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
	if !strings.HasPrefix(result.RawKey, "vlx_platform_live_") {
		t.Errorf("raw key should start with vlx_platform_live_")
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
	fake := "vlx_secret_live_" + strings.Repeat("ff", 32)
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

	// Create a spare so the last-key safeguard doesn't refuse the revoke.
	if _, err := svc.CreateKey(ctx, "t1", CreateKeyInput{Name: "Spare", KeyType: KeyTypeSecret}); err != nil {
		t.Fatalf("create spare: %v", err)
	}
	result, _ := svc.CreateKey(ctx, "t1", CreateKeyInput{Name: "Revokable", KeyType: KeyTypeSecret})
	if _, err := svc.RevokeKey(ctx, "t1", result.Key.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	_, err := svc.ValidateKey(ctx, result.RawKey)
	if err == nil {
		t.Fatal("expected error for revoked key")
	}
}

func TestCreateKey_TestModeFromCtx(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := WithLivemode(context.Background(), false)

	result, err := svc.CreateKey(ctx, "t1", CreateKeyInput{Name: "Sandbox", KeyType: KeyTypeSecret})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.HasPrefix(result.RawKey, "vlx_secret_test_") {
		t.Errorf("test-mode key should start with vlx_secret_test_, got %q", result.RawKey[:20])
	}
	if result.Key.Livemode {
		t.Error("key should be test-mode")
	}

	// ValidateKey on the same raw key recovers livemode=false.
	key, err := svc.ValidateKey(context.Background(), result.RawKey)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if key.Livemode {
		t.Error("validated key should be test-mode")
	}
}

func TestValidateKey_LegacyFormat(t *testing.T) {
	// Pre-FEAT-8 keys have no mode infix (vlx_secret_<hex>). They must still
	// validate, and must be classified as live.
	store := newMemStore()
	svc := NewService(store)

	// Seed a legacy-format key directly.
	rawKey := "vlx_secret_" + strings.Repeat("ab", 32)
	dbPrefix := "vlx_secret_" + strings.Repeat("ab", 6) // first 12 hex chars
	salt := []byte("legacytestsalt16")
	saltHex := hex.EncodeToString(salt)
	hash := sha256.Sum256(append(salt, []byte(rawKey)...))
	store.keys["legacy1"] = domain.APIKey{
		ID:        "legacy1",
		KeyPrefix: dbPrefix,
		KeyHash:   hex.EncodeToString(hash[:]),
		KeySalt:   saltHex,
		KeyType:   string(KeyTypeSecret),
		Livemode:  true,
		TenantID:  "t1",
	}

	key, err := svc.ValidateKey(context.Background(), rawKey)
	if err != nil {
		t.Fatalf("legacy key validate: %v", err)
	}
	if !key.Livemode {
		t.Error("legacy key should be classified as live")
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

// TestRotateKey_ImmediateRevoke covers the default rotation flow: the old key
// stops validating as soon as rotate returns, the new key validates, and the
// new key inherits the old's type/name/livemode/expires_at.
func TestRotateKey_ImmediateRevoke(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()

	expiry := time.Now().UTC().Add(90 * 24 * time.Hour)
	original, err := svc.CreateKey(ctx, "t1", CreateKeyInput{
		Name:      "Prod deploy",
		KeyType:   KeyTypeSecret,
		ExpiresAt: &expiry,
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	rotated, err := svc.RotateKey(ctx, "t1", original.Key.ID, RotateKeyInput{})
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}

	if rotated.NewKey.ID == original.Key.ID {
		t.Fatal("new key must have a distinct ID")
	}
	if rotated.NewKey.Name != original.Key.Name {
		t.Errorf("new key name: got %q, want %q", rotated.NewKey.Name, original.Key.Name)
	}
	if rotated.NewKey.KeyType != original.Key.KeyType {
		t.Errorf("new key type: got %q, want %q", rotated.NewKey.KeyType, original.Key.KeyType)
	}
	if rotated.NewKey.Livemode != original.Key.Livemode {
		t.Errorf("new key livemode: got %v, want %v", rotated.NewKey.Livemode, original.Key.Livemode)
	}
	if rotated.NewKey.ExpiresAt == nil || !rotated.NewKey.ExpiresAt.Equal(expiry) {
		t.Errorf("new key expires_at: got %v, want %v", rotated.NewKey.ExpiresAt, expiry)
	}
	if rotated.OldKey.RevokedAt == nil {
		t.Fatal("old key should be revoked after immediate rotation")
	}
	if rotated.RawKey == "" {
		t.Fatal("raw key must be returned once on rotation")
	}
	if rotated.RawKey == original.RawKey {
		t.Fatal("new raw key must differ from old raw key")
	}

	if _, err := svc.ValidateKey(ctx, original.RawKey); err == nil {
		t.Fatal("old key should fail validation after rotation")
	}
	valid, err := svc.ValidateKey(ctx, rotated.RawKey)
	if err != nil {
		t.Fatalf("new key validation: %v", err)
	}
	if valid.ID != rotated.NewKey.ID {
		t.Errorf("new key ID mismatch: got %q, want %q", valid.ID, rotated.NewKey.ID)
	}
}

// TestRotateKey_WithGracePeriod covers the zero-downtime rotation flow: the
// old key stays valid for the grace window, the new key is immediately valid,
// and both can authenticate concurrently within the window.
func TestRotateKey_WithGracePeriod(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()

	original, _ := svc.CreateKey(ctx, "t1", CreateKeyInput{Name: "App", KeyType: KeyTypeSecret})

	rotated, err := svc.RotateKey(ctx, "t1", original.Key.ID, RotateKeyInput{ExpiresInSeconds: 3600})
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}

	if rotated.OldKey.RevokedAt != nil {
		t.Fatal("grace-period rotation must not set revoked_at on the old key")
	}
	if rotated.OldKey.ExpiresAt == nil {
		t.Fatal("grace-period rotation must set expires_at on the old key")
	}
	until := time.Until(*rotated.OldKey.ExpiresAt)
	if until < 50*time.Minute || until > 70*time.Minute {
		t.Errorf("old key expires_at should be ~1 hour out, got %v", until)
	}

	// Both keys authenticate during the grace window.
	if _, err := svc.ValidateKey(ctx, original.RawKey); err != nil {
		t.Errorf("old key should still validate in grace window: %v", err)
	}
	if _, err := svc.ValidateKey(ctx, rotated.RawKey); err != nil {
		t.Errorf("new key should validate immediately: %v", err)
	}
}

func TestRotateKey_RejectsNegativeGrace(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()

	result, _ := svc.CreateKey(ctx, "t1", CreateKeyInput{Name: "K", KeyType: KeyTypeSecret})

	_, err := svc.RotateKey(ctx, "t1", result.Key.ID, RotateKeyInput{ExpiresInSeconds: -1})
	if err == nil {
		t.Fatal("expected validation error for negative grace")
	}
}

func TestRotateKey_RejectsExcessiveGrace(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()

	result, _ := svc.CreateKey(ctx, "t1", CreateKeyInput{Name: "K", KeyType: KeyTypeSecret})

	_, err := svc.RotateKey(ctx, "t1", result.Key.ID, RotateKeyInput{ExpiresInSeconds: MaxRotationGraceSeconds + 1})
	if err == nil {
		t.Fatal("expected validation error for grace > 7 days")
	}
}

func TestRotateKey_RejectsAlreadyRevoked(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()

	// Spare so the last-key safeguard allows the revoke below.
	if _, err := svc.CreateKey(ctx, "t1", CreateKeyInput{Name: "Spare", KeyType: KeyTypeSecret}); err != nil {
		t.Fatalf("create spare: %v", err)
	}
	result, _ := svc.CreateKey(ctx, "t1", CreateKeyInput{Name: "K", KeyType: KeyTypeSecret})
	if _, err := svc.RevokeKey(ctx, "t1", result.Key.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	_, err := svc.RotateKey(ctx, "t1", result.Key.ID, RotateKeyInput{})
	if err == nil {
		t.Fatal("expected error rotating an already-revoked key")
	}
}

func TestRotateKey_WrongTenant(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()

	result, _ := svc.CreateKey(ctx, "t1", CreateKeyInput{Name: "K", KeyType: KeyTypeSecret})

	_, err := svc.RotateKey(ctx, "wrong_tenant", result.Key.ID, RotateKeyInput{})
	if err == nil {
		t.Fatal("expected error rotating from wrong tenant")
	}
}

// TestRotateKey_PreservesTestmode guards against a subtle regression: if the
// caller's ctx is live-mode but the rotated key is test-mode, the mint path
// must still produce a test-mode replacement. Losing the mode here would
// issue a live key silently, which is the worst class of mode leak — a
// dashboard-issued "rotate" button should never mint a cross-mode key.
func TestRotateKey_PreservesTestmode(t *testing.T) {
	svc := NewService(newMemStore())

	testCtx := WithLivemode(context.Background(), false)
	original, _ := svc.CreateKey(testCtx, "t1", CreateKeyInput{Name: "Sandbox", KeyType: KeyTypeSecret})
	if original.Key.Livemode {
		t.Fatal("seed should be test-mode")
	}

	// Caller context is LIVE here — the mismatch is intentional.
	liveCtx := context.Background()
	rotated, err := svc.RotateKey(liveCtx, "t1", original.Key.ID, RotateKeyInput{})
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if rotated.NewKey.Livemode {
		t.Fatal("rotated key must stay test-mode regardless of caller ctx mode")
	}
	if !strings.HasPrefix(rotated.RawKey, "vlx_secret_test_") {
		t.Errorf("test-mode raw key prefix expected, got %q...", rotated.RawKey[:20])
	}
}

// recordingSessionRevoker captures fan-out calls so tests can assert
// that auth.Service correctly invokes session-revocation after a key
// revoke and during rotate-with-grace=0.
type recordingSessionRevoker struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (r *recordingSessionRevoker) RevokeAllForKey(_ context.Context, keyID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, keyID)
	return r.err
}

func (r *recordingSessionRevoker) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// keySpec describes one key to seed into the test tenant. expired=true
// produces a key with expires_at one hour in the past — the only knob
// the safeguard cares about beyond the type.
type keySpec struct {
	keyType KeyType
	expired bool
}

// seed creates the keys described by specs in tenant ten_1 and returns
// their IDs in the same order. Test setup helper to keep the table-
// driven cases readable.
func seed(t *testing.T, svc *Service, specs ...keySpec) []string {
	t.Helper()
	ids := make([]string, len(specs))
	for i, spec := range specs {
		input := CreateKeyInput{
			Name:    string(spec.keyType) + "-" + string(rune('a'+i)),
			KeyType: spec.keyType,
		}
		if spec.expired {
			past := time.Now().UTC().Add(-1 * time.Hour)
			input.ExpiresAt = &past
		}
		res, err := svc.CreateKey(context.Background(), "ten_1", input)
		if err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
		ids[i] = res.Key.ID
	}
	return ids
}

// TestRevokeKey_Safeguard table-tests the last-active-secret-or-platform
// guard in auth.Service.RevokeKey. The store-layer safeguard refuses
// any revoke that would leave the tenant with zero active secret or
// platform keys; publishable keys don't count (they can't manage other
// keys); already-expired keys aren't "active" so revoking them never
// reduces the count.
//
// FLOW K4 in MANUAL_TEST.md is the operator-facing contract; this is
// the unit-level enforcement.
func TestRevokeKey_Safeguard(t *testing.T) {
	cases := []struct {
		name        string
		seed        []keySpec
		revokeIdx   int    // index into seed of the key to revoke
		wantBlocked bool   // expect Service.RevokeKey to return an error
		wantMsg     string // substring required in the error (when blocked)
	}{
		{
			name:        "blocked: only secret",
			seed:        []keySpec{{keyType: KeyTypeSecret}},
			revokeIdx:   0,
			wantBlocked: true,
			wantMsg:     "last active secret/platform key",
		},
		{
			name:        "blocked: only platform",
			seed:        []keySpec{{keyType: KeyTypePlatform}},
			revokeIdx:   0,
			wantBlocked: true,
			wantMsg:     "last active secret/platform key",
		},
		{
			name: "blocked: secret + publishable, revoke secret (publishable doesn't count)",
			seed: []keySpec{
				{keyType: KeyTypeSecret},
				{keyType: KeyTypePublishable},
			},
			revokeIdx:   0,
			wantBlocked: true,
			wantMsg:     "last active secret/platform key",
		},
		{
			name: "allow: secret + publishable, revoke publishable",
			seed: []keySpec{
				{keyType: KeyTypeSecret},
				{keyType: KeyTypePublishable},
			},
			revokeIdx: 1,
		},
		{
			name: "allow: two secrets, revoke either",
			seed: []keySpec{
				{keyType: KeyTypeSecret},
				{keyType: KeyTypeSecret},
			},
			revokeIdx: 0,
		},
		{
			name: "allow: only key is expired secret (no targetActive → safeguard skipped)",
			seed: []keySpec{
				{keyType: KeyTypeSecret, expired: true},
			},
			revokeIdx: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewService(newMemStore())
			ids := seed(t, svc, tc.seed...)

			_, err := svc.RevokeKey(context.Background(), "ten_1", ids[tc.revokeIdx])
			if tc.wantBlocked {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tc.wantMsg != "" && !strings.Contains(err.Error(), tc.wantMsg) {
					t.Errorf("error should contain %q, got: %v", tc.wantMsg, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected success, got: %v", err)
			}
		})
	}
}

// TestRevokeKey_FanOut covers the cookie-session fan-out: a successful
// RevokeKey must call sessionRevoker.RevokeAllForKey exactly once with
// the revoked key id; a refused RevokeKey (safeguard fired) must not
// fire fan-out at all (auth state didn't change, sessions should remain
// valid).
func TestRevokeKey_FanOut(t *testing.T) {
	cases := []struct {
		name      string
		seed      []keySpec
		revokeIdx int
		wantCalls int
	}{
		{
			name: "fans out on successful revoke",
			seed: []keySpec{
				{keyType: KeyTypeSecret},
				{keyType: KeyTypeSecret},
			},
			revokeIdx: 0,
			wantCalls: 1,
		},
		{
			name:      "does not fan out when safeguard refuses",
			seed:      []keySpec{{keyType: KeyTypeSecret}},
			revokeIdx: 0,
			wantCalls: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewService(newMemStore())
			rev := &recordingSessionRevoker{}
			svc.SetSessionRevoker(rev)
			ids := seed(t, svc, tc.seed...)

			_, _ = svc.RevokeKey(context.Background(), "ten_1", ids[tc.revokeIdx])

			if got := rev.callCount(); got != tc.wantCalls {
				t.Errorf("fan-out call count: got %d, want %d", got, tc.wantCalls)
			}
			if tc.wantCalls > 0 && rev.calls[0] != ids[tc.revokeIdx] {
				t.Errorf("fan-out called with %q, want %q", rev.calls[0], ids[tc.revokeIdx])
			}
		})
	}
}

func TestRotateKey_FanOutOnImmediateCutoverOnly(t *testing.T) {
	// Grace=0 should fan out; grace>0 should not (old key still
	// valid through the grace window, sessions tied to it stay live).
	for _, tc := range []struct {
		name     string
		grace    int64
		wantCall bool
	}{
		{"grace=0 fans out", 0, true},
		{"grace=300 does not fan out", 300, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemStore()
			svc := NewService(store)
			rev := &recordingSessionRevoker{}
			svc.SetSessionRevoker(rev)

			// Two secrets so the immediate-cutover path passes the
			// safeguard when calling Store.Revoke under the hood.
			created, err := svc.CreateKey(context.Background(), "ten_1",
				CreateKeyInput{Name: "rotating", KeyType: KeyTypeSecret})
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if _, err := svc.CreateKey(context.Background(), "ten_1",
				CreateKeyInput{Name: "spare", KeyType: KeyTypeSecret}); err != nil {
				t.Fatalf("create spare: %v", err)
			}

			if _, err := svc.RotateKey(context.Background(), "ten_1", created.Key.ID,
				RotateKeyInput{ExpiresInSeconds: tc.grace}); err != nil {
				t.Fatalf("rotate: %v", err)
			}

			got := rev.callCount()
			if tc.wantCall && got != 1 {
				t.Errorf("expected fan-out, got %d calls", got)
			}
			if !tc.wantCall && got != 0 {
				t.Errorf("expected no fan-out, got %d calls", got)
			}
		})
	}
}
