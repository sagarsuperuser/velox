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

// memStore is a test stand-in for PostgresStore. It models only what the
// Service touches — Create + GetByTokenHash + Revoke — and intentionally
// mirrors the Postgres filter (revoked OR expired → ErrNotFound).
type memStore struct {
	mu   sync.Mutex
	rows map[string]Session
	// idx: token_hash → session id
	idx map[string]string
}

func newMemStore() *memStore {
	return &memStore{rows: map[string]Session{}, idx: map[string]string{}}
}

func (m *memStore) Create(_ context.Context, tenantID, customerID, tokenHash string, expiresAt time.Time) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := "vlx_cps_mem_" + tokenHash[:8]
	sess := Session{
		ID: id, TenantID: tenantID, CustomerID: customerID,
		Livemode: true, ExpiresAt: expiresAt, CreatedAt: time.Now().UTC(),
	}
	m.rows[id] = sess
	m.idx[tokenHash] = id
	return sess, nil
}

func (m *memStore) GetByTokenHash(_ context.Context, tokenHash string) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.idx[tokenHash]
	if !ok {
		return Session{}, errs.ErrNotFound
	}
	sess := m.rows[id]
	now := time.Now().UTC()
	if sess.RevokedAt != nil || !now.Before(sess.ExpiresAt) {
		return Session{}, errs.ErrNotFound
	}
	return sess, nil
}

func (m *memStore) Revoke(_ context.Context, _, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.rows[sessionID]
	if !ok {
		return nil
	}
	now := time.Now().UTC()
	sess.RevokedAt = &now
	m.rows[sessionID] = sess
	return nil
}

// TestRoundTrip walks the canonical lifecycle: mint → validate → revoke →
// validate-miss. Covers the sha256 hash path end to end through Service.
func TestRoundTrip(t *testing.T) {
	svc := NewService(newMemStore())
	ctx := context.Background()

	res, err := svc.Create(ctx, "tnt_x", "cus_y", 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.HasPrefix(res.RawToken, tokenPrefix) {
		t.Fatalf("token missing prefix: %q", res.RawToken)
	}
	if res.Session.CustomerID != "cus_y" {
		t.Fatalf("customer mismatch: %q", res.Session.CustomerID)
	}
	// TTL default should be ~1h.
	ttl := time.Until(res.Session.ExpiresAt)
	if ttl < 50*time.Minute || ttl > 70*time.Minute {
		t.Fatalf("default TTL out of expected band: %s", ttl)
	}

	got, err := svc.Validate(ctx, res.RawToken)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got.ID != res.Session.ID {
		t.Fatalf("validate returned wrong session: %s vs %s", got.ID, res.Session.ID)
	}

	if err := svc.Revoke(ctx, "tnt_x", res.Session.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := svc.Validate(ctx, res.RawToken); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("post-revoke validate: want ErrNotFound, got %v", err)
	}
}

// TestValidate_Expired makes sure the Service treats a row whose
// expires_at has passed the same way it treats a miss or a revoke.
// Service delegates the comparison to the Store (postgres does it in SQL;
// memStore mirrors it), so this pins the contract.
func TestValidate_Expired(t *testing.T) {
	store := newMemStore()
	svc := NewService(store)
	ctx := context.Background()

	// Create with a TTL in the past — the helper forces DefaultTTL on
	// ttl<=0 so we can't mint an already-expired session via Service.
	// Go straight to Store for the negative-TTL path.
	raw, hash, err := newToken()
	if err != nil {
		t.Fatalf("newToken: %v", err)
	}
	if _, err := store.Create(ctx, "tnt_x", "cus_y", hash, time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("store.Create: %v", err)
	}
	if _, err := svc.Validate(ctx, raw); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("expired validate: want ErrNotFound, got %v", err)
	}
}

// TestValidate_EmptyToken guards against a zero-length bearer slipping
// through to Store.GetByTokenHash — sha256("") is a real hash and could
// legitimately match a seeded row in a Postgres-level enumeration attack.
func TestValidate_EmptyToken(t *testing.T) {
	svc := NewService(newMemStore())
	if _, err := svc.Validate(context.Background(), ""); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("empty token: want ErrNotFound, got %v", err)
	}
}
