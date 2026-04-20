package customerportal

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/errs"
)

// memMagicStore mirrors PostgresMagicLinkStore for unit tests. The Consume
// path models the same predicate the SQL uses: used OR expired OR unknown
// all surface as ErrNotFound, so callers can't distinguish the failure
// modes and attackers can't probe the state machine via timing or error.
type memMagicStore struct {
	mu   sync.Mutex
	rows map[string]MagicLink // id → row
	idx  map[string]string    // token_hash → id
}

func newMemMagicStore() *memMagicStore {
	return &memMagicStore{rows: map[string]MagicLink{}, idx: map[string]string{}}
}

func (m *memMagicStore) Create(_ context.Context, tenantID, customerID, tokenHash string, expiresAt time.Time) (MagicLink, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := "vlx_cpml_mem_" + tokenHash[:8]
	row := MagicLink{
		ID: id, TenantID: tenantID, CustomerID: customerID,
		Livemode: true, ExpiresAt: expiresAt, CreatedAt: time.Now().UTC(),
	}
	m.rows[id] = row
	m.idx[tokenHash] = id
	return row, nil
}

func (m *memMagicStore) Consume(_ context.Context, tokenHash string) (MagicLink, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.idx[tokenHash]
	if !ok {
		return MagicLink{}, errs.ErrNotFound
	}
	row := m.rows[id]
	now := time.Now().UTC()
	if row.UsedAt != nil || !now.Before(row.ExpiresAt) {
		return MagicLink{}, errs.ErrNotFound
	}
	row.UsedAt = &now
	m.rows[id] = row
	return row, nil
}

// newMagicLinkServiceForTest wires both the magic-link and session stores
// together the same way the DI wiring does in cmd/velox. The session
// Service needs a real SessionStore because Consume mints one on success.
func newMagicLinkServiceForTest() (*MagicLinkService, *memMagicStore, *memStore) {
	magicStore := newMemMagicStore()
	sessStore := newMemStore()
	sessions := NewService(sessStore)
	return NewMagicLinkService(magicStore, sessions), magicStore, sessStore
}

// TestMagicLink_MintAndConsume walks the canonical lifecycle: mint a link,
// consume it once, get a usable portal session back. Validates the prefix,
// TTL window, and that the session tenant/customer propagate from the
// magic link row.
func TestMagicLink_MintAndConsume(t *testing.T) {
	svc, _, _ := newMagicLinkServiceForTest()
	ctx := context.Background()

	mint, err := svc.Mint(ctx, "tnt_x", "cus_y")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if !strings.HasPrefix(mint.RawToken, magicTokenPrefix) {
		t.Fatalf("token missing prefix: %q", mint.RawToken)
	}
	if mint.Link.TenantID != "tnt_x" || mint.Link.CustomerID != "cus_y" {
		t.Fatalf("link identity mismatch: %+v", mint.Link)
	}
	ttl := time.Until(mint.Link.ExpiresAt)
	if ttl < 10*time.Minute || ttl > 20*time.Minute {
		t.Fatalf("magic-link TTL out of expected 15min band: %s", ttl)
	}

	res, err := svc.Consume(ctx, mint.RawToken)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if res.Session.TenantID != "tnt_x" || res.Session.CustomerID != "cus_y" {
		t.Fatalf("session identity did not propagate: %+v", res.Session)
	}
	if !strings.HasPrefix(res.RawToken, tokenPrefix) {
		t.Fatalf("session token missing prefix: %q", res.RawToken)
	}
}

// TestMagicLink_Consume_AlreadyUsed pins the single-use invariant. A second
// Consume on the same raw token must surface the same ErrNotFound as an
// unknown token, so an attacker replaying a leaked email URL learns
// nothing about whether it was ever valid.
func TestMagicLink_Consume_AlreadyUsed(t *testing.T) {
	svc, _, _ := newMagicLinkServiceForTest()
	ctx := context.Background()

	mint, err := svc.Mint(ctx, "tnt_x", "cus_y")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if _, err := svc.Consume(ctx, mint.RawToken); err != nil {
		t.Fatalf("first Consume: %v", err)
	}
	if _, err := svc.Consume(ctx, mint.RawToken); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("second Consume: want ErrNotFound, got %v", err)
	}
}

// TestMagicLink_Consume_Expired locks down the expires_at predicate. An
// expired row behaves like a used row — same ErrNotFound — because the
// SQL predicate `used_at IS NULL AND expires_at > now()` collapses both
// failure modes into a single sql.ErrNoRows branch.
func TestMagicLink_Consume_Expired(t *testing.T) {
	svc, store, _ := newMagicLinkServiceForTest()
	ctx := context.Background()

	// Go straight to the store with a past expires_at — Mint normalises
	// TTL to 15 min so we can't bypass the default through the service.
	raw, hash, err := newMagicToken()
	if err != nil {
		t.Fatalf("newMagicToken: %v", err)
	}
	if _, err := store.Create(ctx, "tnt_x", "cus_y", hash, time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	if _, err := svc.Consume(ctx, raw); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("expired Consume: want ErrNotFound, got %v", err)
	}
}

// TestMagicLink_Consume_UnknownToken — a well-formed but never-minted
// token. Same ErrNotFound as used/expired, so the three buckets are
// indistinguishable from outside.
func TestMagicLink_Consume_UnknownToken(t *testing.T) {
	svc, _, _ := newMagicLinkServiceForTest()
	if _, err := svc.Consume(context.Background(), magicTokenPrefix+"deadbeef"); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("unknown token: want ErrNotFound, got %v", err)
	}
}

// TestMagicLink_Consume_EmptyToken guards the sha256("") footgun —
// hashing an empty string produces a real, deterministic digest that
// could match a seeded row in a Postgres-level enumeration attack.
// Service must short-circuit before hashing.
func TestMagicLink_Consume_EmptyToken(t *testing.T) {
	svc, _, _ := newMagicLinkServiceForTest()
	if _, err := svc.Consume(context.Background(), ""); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("empty token: want ErrNotFound, got %v", err)
	}
}

// TestMagicLink_Mint_RequiresTenantAndCustomer — both are mandatory.
// Handler will usually have populated these from a blind-index lookup,
// but the service must fail closed on empty strings rather than
// inserting a row with dangling foreign keys.
func TestMagicLink_Mint_RequiresTenantAndCustomer(t *testing.T) {
	svc, _, _ := newMagicLinkServiceForTest()
	ctx := context.Background()

	if _, err := svc.Mint(ctx, "", "cus_y"); err == nil {
		t.Fatalf("empty tenant: want error, got nil")
	}
	if _, err := svc.Mint(ctx, "tnt_x", ""); err == nil {
		t.Fatalf("empty customer: want error, got nil")
	}
}
