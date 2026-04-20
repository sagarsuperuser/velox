package customerportal

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// MagicLinkTTL is the lifetime of a freshly minted magic link. 15 min is
// short enough that a leaked email URL becomes useless quickly, long
// enough for a customer to receive the email and click. Mirrors the
// industry default (Slack, Linear, Stripe Express all use 15 min).
const MagicLinkTTL = 15 * time.Minute

// magicTokenPrefix — visible in logs, distinguishes magic links from the
// reusable session token (vlx_cps_) and API keys (vlx_...).
const magicTokenPrefix = "vlx_cpml_"

// MagicLink represents a persisted single-use magic link. The raw token
// is only returned once at Create and never stored alongside the row.
type MagicLink struct {
	ID         string
	TenantID   string
	Livemode   bool
	CustomerID string
	ExpiresAt  time.Time
	UsedAt     *time.Time
	CreatedAt  time.Time
}

// MagicLinkStore is the narrow persistence contract the service depends on.
// Split from the session Store because the consumption path is very
// different — single-use markers vs. revocation — and merging them would
// force every method to carry a "kind" enum.
type MagicLinkStore interface {
	// Create inserts a magic link row under the given tenant's RLS context.
	// The caller holds the raw token and includes it in the email body; we
	// persist only the hash.
	Create(ctx context.Context, tenantID, customerID, tokenHash string, expiresAt time.Time) (MagicLink, error)

	// Consume atomically looks up a magic link by token hash (TxBypass,
	// because the caller is unauthenticated at this point), verifies it's
	// unused and unexpired, and marks it used_at = now(). Returns the full
	// row so the caller can mint a portal session against the same
	// (tenant, customer). Returns errs.ErrNotFound when the token is
	// unknown, expired, or already used — callers map all three to a
	// single 401 so an attacker can't learn which bucket they hit.
	Consume(ctx context.Context, tokenHash string) (MagicLink, error)
}

// MagicLinkService is the programmatic surface for minting and redeeming
// magic links. Kept separate from Service (portal sessions) so the two
// token types can be depended on independently by handlers.
type MagicLinkService struct {
	store    MagicLinkStore
	sessions *Service // used to mint a session after Consume
}

func NewMagicLinkService(store MagicLinkStore, sessions *Service) *MagicLinkService {
	return &MagicLinkService{store: store, sessions: sessions}
}

// MintResult bundles the persisted magic link row and the raw token that
// must go into the email body. RawToken is returned once.
type MintResult struct {
	Link     MagicLink
	RawToken string
}

// Mint creates a new magic link for (tenantID, customerID). Caller is the
// unauthenticated request handler — the customer lookup ran cross-tenant
// just before this, via the blind index, so tenantID is whatever the
// lookup produced.
func (s *MagicLinkService) Mint(ctx context.Context, tenantID, customerID string) (MintResult, error) {
	if tenantID == "" {
		return MintResult{}, errs.Required("tenant_id")
	}
	if customerID == "" {
		return MintResult{}, errs.Required("customer_id")
	}
	raw, hash, err := newMagicToken()
	if err != nil {
		return MintResult{}, fmt.Errorf("generate magic token: %w", err)
	}
	link, err := s.store.Create(ctx, tenantID, customerID, hash, time.Now().UTC().Add(MagicLinkTTL))
	if err != nil {
		return MintResult{}, err
	}
	return MintResult{Link: link, RawToken: raw}, nil
}

// ConsumeResult is what the consume endpoint returns: a newly minted
// portal session the browser can use for subsequent /v1/me/* calls.
type ConsumeResult struct {
	Session  Session
	RawToken string
}

// Consume validates a raw magic token, marks it used, and mints a portal
// session against the same (tenant, customer). One method per the full
// redeem-and-promote flow so handlers can't accidentally skip the
// single-use marker on the way to minting a session.
func (s *MagicLinkService) Consume(ctx context.Context, rawToken string) (ConsumeResult, error) {
	if rawToken == "" {
		return ConsumeResult{}, errs.ErrNotFound
	}
	link, err := s.store.Consume(ctx, hashMagicToken(rawToken))
	if err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			return ConsumeResult{}, errs.ErrNotFound
		}
		return ConsumeResult{}, err
	}

	// Propagate the magic link's livemode onto the session ctx so the
	// session row inherits the same mode — otherwise a test-mode magic
	// link could mint a live-mode session (or vice versa) and the two
	// would live on opposite sides of the RLS fence.
	sessCtx := postgres.WithLivemode(ctx, link.Livemode)
	mint, err := s.sessions.Create(sessCtx, link.TenantID, link.CustomerID, DefaultTTL)
	if err != nil {
		return ConsumeResult{}, fmt.Errorf("mint session: %w", err)
	}
	return ConsumeResult{Session: mint.Session, RawToken: mint.RawToken}, nil
}

// PostgresMagicLinkStore is the real implementation backed by the
// customer_portal_magic_links table.
type PostgresMagicLinkStore struct {
	db *postgres.DB
}

func NewPostgresMagicLinkStore(db *postgres.DB) *PostgresMagicLinkStore {
	return &PostgresMagicLinkStore{db: db}
}

func (s *PostgresMagicLinkStore) Create(ctx context.Context, tenantID, customerID, tokenHash string, expiresAt time.Time) (MagicLink, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return MagicLink{}, fmt.Errorf("begin tx: %w", err)
	}
	defer postgres.Rollback(tx)

	var link MagicLink
	err = tx.QueryRowContext(ctx, `
		INSERT INTO customer_portal_magic_links (tenant_id, customer_id, token_hash, expires_at)
		VALUES ($1, $2, $3, $4)
		RETURNING id, tenant_id, livemode, customer_id, expires_at, used_at, created_at
	`, tenantID, customerID, tokenHash, expiresAt).Scan(
		&link.ID, &link.TenantID, &link.Livemode, &link.CustomerID,
		&link.ExpiresAt, &link.UsedAt, &link.CreatedAt,
	)
	if err != nil {
		return MagicLink{}, fmt.Errorf("insert magic link: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return MagicLink{}, fmt.Errorf("commit: %w", err)
	}
	return link, nil
}

// Consume does the atomic read-and-mark in one statement. UPDATE ... RETURNING
// with an inline used_at IS NULL + expires_at > now() predicate guarantees
// the no-op case (already-used or expired tokens) surfaces as sql.ErrNoRows
// — which we translate to errs.ErrNotFound so the handler produces a
// uniform 401 across all failure modes.
func (s *PostgresMagicLinkStore) Consume(ctx context.Context, tokenHash string) (MagicLink, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return MagicLink{}, fmt.Errorf("begin tx: %w", err)
	}
	defer postgres.Rollback(tx)

	var link MagicLink
	err = tx.QueryRowContext(ctx, `
		UPDATE customer_portal_magic_links
		SET used_at = now()
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
		RETURNING id, tenant_id, livemode, customer_id, expires_at, used_at, created_at
	`, tokenHash).Scan(
		&link.ID, &link.TenantID, &link.Livemode, &link.CustomerID,
		&link.ExpiresAt, &link.UsedAt, &link.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return MagicLink{}, errs.ErrNotFound
		}
		return MagicLink{}, fmt.Errorf("consume magic link: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return MagicLink{}, fmt.Errorf("commit: %w", err)
	}
	return link, nil
}

// newMagicToken mints a fresh 256-bit token and its sha256 hash. Same
// shape as payment/token.go and customerportal/session.go — kept a
// separate helper because the prefix is different.
func newMagicToken() (raw, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	raw = magicTokenPrefix + hex.EncodeToString(buf)
	sum := sha256.Sum256([]byte(raw))
	hash = hex.EncodeToString(sum[:])
	return raw, hash, nil
}

func hashMagicToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// Compile-time check that the postgres store satisfies the narrow
// interface the service depends on.
var _ MagicLinkStore = (*PostgresMagicLinkStore)(nil)
