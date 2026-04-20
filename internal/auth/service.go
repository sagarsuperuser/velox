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

func (s *Service) ListKeys(ctx context.Context, filter ListFilter) ([]domain.APIKey, error) {
	return s.store.List(ctx, filter)
}
