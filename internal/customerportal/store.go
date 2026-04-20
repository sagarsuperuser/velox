package customerportal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// Store is the persistence contract. The real impl is PostgresStore; tests
// swap it for an in-memory fake via Service.
type Store interface {
	Create(ctx context.Context, tenantID, customerID, tokenHash string, expiresAt time.Time) (Session, error)
	GetByTokenHash(ctx context.Context, tokenHash string) (Session, error)
	Revoke(ctx context.Context, tenantID, sessionID string) error
}

type PostgresStore struct {
	db *postgres.DB
}

func NewPostgresStore(db *postgres.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

// Create inserts a new portal session under the tenant's RLS context.
// The caller holds the raw token and hands it to the customer; we only
// store the hash.
func (s *PostgresStore) Create(ctx context.Context, tenantID, customerID, tokenHash string, expiresAt time.Time) (Session, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return Session{}, fmt.Errorf("begin tx: %w", err)
	}
	defer postgres.Rollback(tx)

	var sess Session
	err = tx.QueryRowContext(ctx, `
		INSERT INTO customer_portal_sessions (tenant_id, customer_id, token_hash, expires_at)
		VALUES ($1, $2, $3, $4)
		RETURNING id, tenant_id, livemode, customer_id, expires_at, revoked_at, created_at
	`, tenantID, customerID, tokenHash, expiresAt).Scan(
		&sess.ID, &sess.TenantID, &sess.Livemode, &sess.CustomerID,
		&sess.ExpiresAt, &sess.RevokedAt, &sess.CreatedAt,
	)
	if err != nil {
		return Session{}, fmt.Errorf("insert session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Session{}, fmt.Errorf("commit: %w", err)
	}
	return sess, nil
}

// GetByTokenHash resolves a presented token back to a session record.
// Runs under TxBypass because the caller is unauthenticated until the
// lookup succeeds — the token hash is the only handle we have to learn
// which tenant+customer this request belongs to. A 256-bit token with a
// constant-time hash compare in SQL gives us the same enumeration
// resistance as payment_update_tokens.Validate.
//
// Returns errs.ErrNotFound if no row matches, the session is revoked, or
// it's past expiry — callers should map this to a 401.
func (s *PostgresStore) GetByTokenHash(ctx context.Context, tokenHash string) (Session, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return Session{}, fmt.Errorf("begin tx: %w", err)
	}
	defer postgres.Rollback(tx)

	var sess Session
	err = tx.QueryRowContext(ctx, `
		SELECT id, tenant_id, livemode, customer_id, expires_at, revoked_at, created_at
		FROM customer_portal_sessions
		WHERE token_hash = $1
		  AND revoked_at IS NULL
		  AND expires_at > now()
	`, tokenHash).Scan(
		&sess.ID, &sess.TenantID, &sess.Livemode, &sess.CustomerID,
		&sess.ExpiresAt, &sess.RevokedAt, &sess.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, errs.ErrNotFound
		}
		return Session{}, fmt.Errorf("select session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Session{}, fmt.Errorf("commit: %w", err)
	}
	return sess, nil
}

// Revoke marks a session as revoked; subsequent GetByTokenHash lookups
// will miss and Middleware will 401. Idempotent — revoking an already
// revoked session is a no-op that still returns nil.
func (s *PostgresStore) Revoke(ctx context.Context, tenantID, sessionID string) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer postgres.Rollback(tx)

	if _, err := tx.ExecContext(ctx,
		`UPDATE customer_portal_sessions SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`,
		sessionID,
	); err != nil {
		return fmt.Errorf("update: %w", err)
	}
	return tx.Commit()
}
