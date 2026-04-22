package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

type PostgresStore struct {
	db *postgres.DB
}

func NewPostgresStore(db *postgres.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

func (s *PostgresStore) Create(ctx context.Context, sess Session) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer postgres.Rollback(tx)

	_, err = tx.ExecContext(ctx,
		`INSERT INTO sessions (id_hash, user_id, tenant_id, livemode, expires_at, user_agent, ip)
		 VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''))`,
		sess.IDHash, sess.UserID, sess.TenantID, sess.Livemode,
		sess.ExpiresAt, sess.UserAgent, sess.IP)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return tx.Commit()
}

func (s *PostgresStore) GetByIDHash(ctx context.Context, idHash string) (Session, error) {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return Session{}, fmt.Errorf("begin: %w", err)
	}
	defer postgres.Rollback(tx)

	var sess Session
	var ua, ip sql.NullString
	var revoked sql.NullTime
	err = tx.QueryRowContext(ctx,
		`SELECT id_hash, user_id, tenant_id, livemode, created_at, last_seen_at, expires_at, revoked_at, user_agent, ip
		 FROM sessions WHERE id_hash = $1`,
		idHash,
	).Scan(
		&sess.IDHash, &sess.UserID, &sess.TenantID, &sess.Livemode,
		&sess.CreatedAt, &sess.LastSeenAt, &sess.ExpiresAt, &revoked, &ua, &ip,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("query session: %w", err)
	}
	if revoked.Valid {
		t := revoked.Time
		sess.RevokedAt = &t
	}
	if ua.Valid {
		sess.UserAgent = ua.String
	}
	if ip.Valid {
		sess.IP = ip.String
	}
	if err := tx.Commit(); err != nil {
		return Session{}, fmt.Errorf("commit: %w", err)
	}
	return sess, nil
}

func (s *PostgresStore) Touch(ctx context.Context, idHash string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer postgres.Rollback(tx)

	_, err = tx.ExecContext(ctx,
		`UPDATE sessions SET last_seen_at = $1 WHERE id_hash = $2 AND revoked_at IS NULL`,
		now, idHash)
	if err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	return tx.Commit()
}

func (s *PostgresStore) UpdateLivemode(ctx context.Context, idHash string, livemode bool) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer postgres.Rollback(tx)

	res, err := tx.ExecContext(ctx,
		`UPDATE sessions SET livemode = $1 WHERE id_hash = $2 AND revoked_at IS NULL`,
		livemode, idHash)
	if err != nil {
		return fmt.Errorf("update livemode: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *PostgresStore) Revoke(ctx context.Context, idHash string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer postgres.Rollback(tx)

	_, err = tx.ExecContext(ctx,
		`UPDATE sessions SET revoked_at = $1 WHERE id_hash = $2 AND revoked_at IS NULL`,
		now, idHash)
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return tx.Commit()
}

func (s *PostgresStore) RevokeAllForUser(ctx context.Context, userID string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer postgres.Rollback(tx)

	_, err = tx.ExecContext(ctx,
		`UPDATE sessions SET revoked_at = $1 WHERE user_id = $2 AND revoked_at IS NULL`,
		now, userID)
	if err != nil {
		return fmt.Errorf("revoke all sessions: %w", err)
	}
	return tx.Commit()
}
