package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

const (
	keyPrefixLen = 12 // hex chars used for DB prefix lookup
	keySecretLen = 32 // random bytes = 64 hex chars
)

type Service struct {
	store Store
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

// CreateKeyResult contains the key record + raw key (shown once).
type CreateKeyResult struct {
	Key    domain.APIKey `json:"key"`
	RawKey string        `json:"raw_key"`
}

type CreateKeyInput struct {
	Name      string     `json:"name"`
	KeyType   KeyType    `json:"key_type"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

func (s *Service) CreateKey(ctx context.Context, tenantID string, input CreateKeyInput) (CreateKeyResult, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return CreateKeyResult{}, errs.Required("name")
	}

	keyType := input.KeyType
	if keyType == "" {
		keyType = KeyTypeSecret
	}
	if keyType != KeyTypePlatform && keyType != KeyTypeSecret && keyType != KeyTypePublishable {
		return CreateKeyResult{}, errs.Invalid("key_type", "must be one of: platform, secret, publishable")
	}

	// Mode inherits from caller ctx, matching Stripe: a test-mode key creates
	// new test-mode keys, a live-mode key creates live. There is no cross-mode
	// key creation path — caller must switch authenticators to switch mode.
	livemode := Livemode(ctx)

	// Generate raw key: prefix + random hex
	secret := make([]byte, keySecretLen)
	if _, err := rand.Read(secret); err != nil {
		return CreateKeyResult{}, fmt.Errorf("generate key: %w", err)
	}
	secretHex := hex.EncodeToString(secret)
	prefix := KeyPrefix(keyType, livemode)
	rawKey := prefix + secretHex

	// DB prefix: full mode-aware prefix + first N hex chars (indexed lookup)
	dbPrefix := prefix + secretHex[:keyPrefixLen]

	// Generate a 16-byte random salt for this key
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return CreateKeyResult{}, fmt.Errorf("generate salt: %w", err)
	}
	saltHex := hex.EncodeToString(salt)

	// Hash: SHA-256(salt + rawKey)
	hash := sha256.Sum256(append(salt, []byte(rawKey)...))
	hashHex := hex.EncodeToString(hash[:])

	id := postgres.NewID("vlx_key")

	key, err := s.store.Create(ctx, domain.APIKey{
		ID:        id,
		KeyPrefix: dbPrefix,
		KeyHash:   hashHex,
		KeySalt:   saltHex,
		KeyType:   string(keyType),
		Livemode:  livemode,
		Name:      name,
		TenantID:  tenantID,
		ExpiresAt: input.ExpiresAt,
	})
	if err != nil {
		return CreateKeyResult{}, err
	}

	return CreateKeyResult{Key: key, RawKey: rawKey}, nil
}

// ValidateKey looks up a key by prefix, verifies hash, checks expiry.
//
// Accepts three prefix forms, in priority order:
//  1. "vlx_{type}_live_..." (new, post-FEAT-8)
//  2. "vlx_{type}_test_..." (new, post-FEAT-8)
//  3. "vlx_{type}_..."       (legacy, pre-FEAT-8 — treated as live)
//
// Backward compat for (3) keeps existing production keys working through
// the cutover. Newly issued keys always use form (1) or (2).
func (s *Service) ValidateKey(ctx context.Context, rawKey string) (domain.APIKey, error) {
	rawKey = strings.TrimSpace(rawKey)

	var (
		keyType  KeyType
		fullPrefix string
	)
	for _, kt := range []KeyType{KeyTypePlatform, KeyTypeSecret, KeyTypePublishable} {
		typeP := kt.TypePrefix()
		if !strings.HasPrefix(rawKey, typeP) {
			continue
		}
		after := rawKey[len(typeP):]
		switch {
		case strings.HasPrefix(after, "live_"):
			keyType = kt
			fullPrefix = typeP + "live_"
		case strings.HasPrefix(after, "test_"):
			keyType = kt
			fullPrefix = typeP + "test_"
		default:
			// Legacy format (pre-FEAT-8): no mode infix — treat as live.
			keyType = kt
			fullPrefix = typeP
		}
		break
	}
	if fullPrefix == "" {
		return domain.APIKey{}, fmt.Errorf("invalid key format")
	}
	_ = keyType // retained for future use (permission routing pre-lookup)

	secretPart := strings.TrimPrefix(rawKey, fullPrefix)
	if len(secretPart) < keyPrefixLen {
		return domain.APIKey{}, fmt.Errorf("invalid key format")
	}

	dbPrefix := fullPrefix + secretPart[:keyPrefixLen]

	key, err := s.store.GetByPrefix(ctx, dbPrefix)
	if err != nil {
		return domain.APIKey{}, fmt.Errorf("invalid api key")
	}

	// Verify hash using the stored salt
	salt, err := hex.DecodeString(key.KeySalt)
	if err != nil {
		return domain.APIKey{}, fmt.Errorf("invalid api key")
	}
	hash := sha256.Sum256(append(salt, []byte(rawKey)...))
	hashHex := hex.EncodeToString(hash[:])
	if subtle.ConstantTimeCompare([]byte(hashHex), []byte(key.KeyHash)) != 1 {
		return domain.APIKey{}, fmt.Errorf("invalid api key")
	}

	// Check expiration
	if key.ExpiresAt != nil && time.Now().UTC().After(*key.ExpiresAt) {
		return domain.APIKey{}, fmt.Errorf("api key expired")
	}

	// Touch last used (async, fire and forget)
	go func() { _ = s.store.TouchLastUsed(context.Background(), key.ID, time.Now().UTC()) }()

	return key, nil
}

func (s *Service) RevokeKey(ctx context.Context, tenantID, id string) (domain.APIKey, error) {
	return s.store.Revoke(ctx, tenantID, id)
}

// MaxRotationGraceSeconds caps how long a caller can keep the old key alive
// during rotation. Seven days matches the longest grace window Stripe offers
// in its dashboard "roll key" flow — long enough for a scheduled deploy
// cadence, short enough that a forgotten rotation still self-heals.
const MaxRotationGraceSeconds = 7 * 24 * 60 * 60

// RotateKeyInput parameterises rotation. ExpiresInSeconds=0 revokes the old
// key immediately on rotation; a positive value keeps it valid for that many
// seconds past now, giving deployed clients time to swap credentials without
// a hard cutover.
type RotateKeyInput struct {
	ExpiresInSeconds int64 `json:"expires_in_seconds,omitempty"`
}

// RotateKeyResult returns both sides of a rotation. RawKey is the new key's
// plaintext — shown once, never persisted or returned on any subsequent
// fetch. OldKey's ExpiresAt (grace window) or RevokedAt (immediate) reflects
// which path the caller took.
type RotateKeyResult struct {
	OldKey domain.APIKey `json:"old_key"`
	NewKey domain.APIKey `json:"new_key"`
	RawKey string        `json:"raw_key"`
}

// RotateKey issues a replacement key for `id`, preserving its name, type,
// livemode, and any configured expires_at. The old key is either revoked
// immediately (grace=0) or scheduled to expire after input.ExpiresInSeconds
// seconds; callers trading zero-downtime deploys for slightly wider blast
// radius pick a non-zero grace.
//
// Self-rotation guard (handler-level, not service-level): the caller must not
// rotate the key that authenticated the request — same rationale as Revoke.
// The handler checks this before invoking the service; the service itself
// has no access to the requesting key's identity.
func (s *Service) RotateKey(ctx context.Context, tenantID, id string, input RotateKeyInput) (RotateKeyResult, error) {
	if input.ExpiresInSeconds < 0 {
		return RotateKeyResult{}, errs.Invalid("expires_in_seconds", "must be >= 0")
	}
	if input.ExpiresInSeconds > MaxRotationGraceSeconds {
		return RotateKeyResult{}, errs.Invalid("expires_in_seconds",
			fmt.Sprintf("must be <= %d (7 days)", MaxRotationGraceSeconds))
	}

	old, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return RotateKeyResult{}, err
	}
	if old.RevokedAt != nil {
		return RotateKeyResult{}, errs.InvalidState("cannot rotate a revoked key")
	}

	// Mint the replacement through the same CreateKey path so livemode
	// propagation, hashing, and prefix generation stay in one place. The new
	// key inherits name+type+expires_at from the old; a rotation is a
	// credential swap, not a reconfiguration.
	createInput := CreateKeyInput{
		Name:      old.Name,
		KeyType:   KeyType(old.KeyType),
		ExpiresAt: old.ExpiresAt,
	}
	// The new key must land on the same livemode as the old — Livemode(ctx)
	// depends on the request ctx, which the caller controls. We force the
	// ctx to match the old key's mode so a live key rotated from a test-mode
	// session (unlikely but possible through platform keys) still produces a
	// live replacement.
	newCtx := ctx
	if Livemode(ctx) != old.Livemode {
		newCtx = WithLivemode(ctx, old.Livemode)
	}
	created, err := s.CreateKey(newCtx, tenantID, createInput)
	if err != nil {
		return RotateKeyResult{}, fmt.Errorf("mint replacement key: %w", err)
	}

	var oldAfter domain.APIKey
	if input.ExpiresInSeconds == 0 {
		// Immediate revocation: old key stops authenticating on the next
		// ValidateKey call. Any request already in flight under the old key
		// completes; subsequent ones fail.
		oldAfter, err = s.store.Revoke(ctx, tenantID, id)
	} else {
		graceUntil := time.Now().UTC().Add(time.Duration(input.ExpiresInSeconds) * time.Second)
		oldAfter, err = s.store.ScheduleExpiry(ctx, tenantID, id, graceUntil)
	}
	if err != nil {
		// The new key is already minted. Leaving it live (and failing rotation)
		// means callers can retry the rotation using the new key — the old
		// stays valid until its retry succeeds. A garbage-collection job can
		// eventually reap the unused new key if the caller never retries.
		return RotateKeyResult{}, fmt.Errorf("retire old key (replacement %s was created and must be cleaned up if retry fails): %w", created.Key.ID, err)
	}

	return RotateKeyResult{
		OldKey: oldAfter,
		NewKey: created.Key,
		RawKey: created.RawKey,
	}, nil
}

func (s *Service) ListKeys(ctx context.Context, filter ListFilter) ([]domain.APIKey, error) {
	return s.store.List(ctx, filter)
}
