package auth

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

type PostgresStore struct {
	db *postgres.DB
}

func NewPostgresStore(db *postgres.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

const keyCols = `id, key_prefix, key_type, livemode, name, tenant_id, created_at, expires_at, revoked_at, last_used_at`

func scanKey(row interface{ Scan(...any) error }) (domain.APIKey, error) {
	var k domain.APIKey
	err := row.Scan(&k.ID, &k.KeyPrefix, &k.KeyType, &k.Livemode, &k.Name, &k.TenantID,
		&k.CreatedAt, &k.ExpiresAt, &k.RevokedAt, &k.LastUsedAt)
	return k, err
}

func (s *PostgresStore) Create(ctx context.Context, key domain.APIKey) (domain.APIKey, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, key.TenantID)
	if err != nil {
		return domain.APIKey{}, err
	}
	defer postgres.Rollback(tx)

	now := time.Now().UTC()
	k, err := scanKey(tx.QueryRowContext(ctx, `
		INSERT INTO api_keys (id, key_prefix, key_hash, key_salt, key_type, livemode, name, tenant_id, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING `+keyCols,
		key.ID, key.KeyPrefix, key.KeyHash, key.KeySalt, key.KeyType, key.Livemode, key.Name, key.TenantID, now,
		postgres.NullableTime(key.ExpiresAt),
	))
	if err != nil {
		return domain.APIKey{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.APIKey{}, err
	}
	return k, nil
}

func (s *PostgresStore) Get(ctx context.Context, tenantID, id string) (domain.APIKey, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.APIKey{}, err
	}
	defer postgres.Rollback(tx)

	k, err := scanKey(tx.QueryRowContext(ctx,
		`SELECT `+keyCols+` FROM api_keys WHERE id = $1`, id))
	if err == sql.ErrNoRows {
		return domain.APIKey{}, errs.ErrNotFound
	}
	return k, err
}

func (s *PostgresStore) GetByPrefix(ctx context.Context, prefix string) (domain.APIKey, error) {
	// API key lookup bypasses RLS — we need to find the key to determine the tenant.
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return domain.APIKey{}, err
	}
	defer postgres.Rollback(tx)

	var key domain.APIKey
	err = tx.QueryRowContext(ctx, `
		SELECT `+keyCols+`, key_hash, key_salt
		FROM api_keys
		WHERE key_prefix = $1 AND revoked_at IS NULL
	`, prefix).Scan(&key.ID, &key.KeyPrefix, &key.KeyType, &key.Livemode, &key.Name, &key.TenantID,
		&key.CreatedAt, &key.ExpiresAt, &key.RevokedAt, &key.LastUsedAt, &key.KeyHash, &key.KeySalt)

	if err == sql.ErrNoRows {
		return domain.APIKey{}, errs.ErrNotFound
	}
	return key, err
}

// Revoke flips revoked_at on the target key, but refuses if doing so
// would leave the tenant with zero active secret-or-platform keys —
// the lockout case where the operator can no longer sign in or
// create new keys. Publishable keys don't count toward the safeguard
// since they can't manage other keys.
//
// Atomic: takes FOR UPDATE on the tenant's candidate-blocker keys
// before counting, so concurrent revokes serialize and can't both
// pass the safeguard and end at zero. Different tenants don't block
// each other (the lock filter is tenant-scoped).
//
// Idempotent: revoking an already-revoked or expired key returns
// ErrNotFound (the UPDATE matches zero rows). Caller decides how to
// surface that.
func (s *PostgresStore) Revoke(ctx context.Context, tenantID, id string) (domain.APIKey, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.APIKey{}, err
	}
	defer postgres.Rollback(tx)

	// Lock the candidate-blocker set so concurrent revokes serialize.
	// We lock the broader set (all active secret/platform keys for the
	// tenant) rather than just the target row so any other revoke on
	// the same tenant blocks here, even if it targets a different row.
	if _, err := tx.ExecContext(ctx, `
		SELECT 1 FROM api_keys
		WHERE tenant_id = $1
		  AND key_type IN ('secret', 'platform')
		  AND revoked_at IS NULL
		FOR UPDATE`, tenantID); err != nil {
		return domain.APIKey{}, fmt.Errorf("lock keys: %w", err)
	}

	// Count what would remain after the revoke — secret/platform keys
	// other than the target, still active and unexpired.
	var remaining int
	err = tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM api_keys
		WHERE tenant_id = $1
		  AND id != $2
		  AND key_type IN ('secret', 'platform')
		  AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at > NOW())`,
		tenantID, id).Scan(&remaining)
	if err != nil {
		return domain.APIKey{}, fmt.Errorf("count remaining keys: %w", err)
	}

	// Look up the target's key_type and expires_at so the safeguard
	// only fires when revoking a CURRENTLY-ACTIVE secret/platform key
	// would orphan the tenant. Revoking a publishable key never
	// orphans (publishables can't manage keys), and revoking an
	// already-expired key doesn't worsen the state — the key was
	// already failing auth, so the active-key count doesn't decrease.
	// Allowing the latter is necessary for the "clean up stale
	// expired rows" operator workflow.
	var (
		targetKeyType   string
		targetExpiresAt sql.NullTime
	)
	err = tx.QueryRowContext(ctx,
		`SELECT key_type, expires_at FROM api_keys WHERE id = $1 AND revoked_at IS NULL`,
		id).Scan(&targetKeyType, &targetExpiresAt)
	if err == sql.ErrNoRows {
		return domain.APIKey{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.APIKey{}, fmt.Errorf("read target key: %w", err)
	}

	targetActive := !targetExpiresAt.Valid || targetExpiresAt.Time.After(time.Now().UTC())
	if targetActive && (targetKeyType == "secret" || targetKeyType == "platform") && remaining == 0 {
		return domain.APIKey{}, errs.InvalidState(
			"cannot revoke the last active secret/platform key — create another first")
	}

	now := time.Now().UTC()
	k, err := scanKey(tx.QueryRowContext(ctx, `
		UPDATE api_keys SET revoked_at = $1
		WHERE id = $2 AND revoked_at IS NULL
		RETURNING `+keyCols, now, id))
	if err == sql.ErrNoRows {
		return domain.APIKey{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.APIKey{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.APIKey{}, err
	}
	return k, nil
}

func (s *PostgresStore) ScheduleExpiry(ctx context.Context, tenantID, id string, expiresAt time.Time) (domain.APIKey, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return domain.APIKey{}, err
	}
	defer postgres.Rollback(tx)

	// Only schedule expiry on keys that aren't already revoked — rotating a
	// revoked key makes no sense, and the predicate guards against racing
	// revoke+rotate for the same row.
	k, err := scanKey(tx.QueryRowContext(ctx, `
		UPDATE api_keys SET expires_at = $1
		WHERE id = $2 AND revoked_at IS NULL
		RETURNING `+keyCols, expiresAt, id))
	if err == sql.ErrNoRows {
		return domain.APIKey{}, errs.ErrNotFound
	}
	if err != nil {
		return domain.APIKey{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.APIKey{}, err
	}
	return k, nil
}

func (s *PostgresStore) List(ctx context.Context, filter ListFilter) ([]domain.APIKey, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxTenant, filter.TenantID)
	if err != nil {
		return nil, err
	}
	defer postgres.Rollback(tx)

	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	query := `SELECT ` + keyCols + ` FROM api_keys`
	args := []any{}
	idx := 1

	if filter.Role != "" {
		query += fmt.Sprintf(" WHERE key_type = $%d", idx)
		args = append(args, filter.Role)
		idx++
	}

	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", idx, idx+1)
	args = append(args, limit, filter.Offset)

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var keys []domain.APIKey
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (s *PostgresStore) TouchLastUsed(ctx context.Context, id string, usedAt time.Time) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return err
	}
	defer postgres.Rollback(tx)

	_, err = tx.ExecContext(ctx, `UPDATE api_keys SET last_used_at = $1 WHERE id = $2`, usedAt, id)
	if err != nil {
		return err
	}
	return tx.Commit()
}
