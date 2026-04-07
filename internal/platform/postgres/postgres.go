package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	DefaultQueryTimeout     = 5 * time.Second
	DefaultMigrationTimeout = 60 * time.Second
)

// DB wraps *sql.DB with query timeout and RLS-aware transactions.
type DB struct {
	Pool         *sql.DB
	QueryTimeout time.Duration
}

func NewDB(pool *sql.DB, queryTimeout time.Duration) *DB {
	if queryTimeout <= 0 {
		queryTimeout = DefaultQueryTimeout
	}
	return &DB{Pool: pool, QueryTimeout: queryTimeout}
}

func (db *DB) Ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), db.QueryTimeout)
}

// TxMode controls how a transaction sets RLS session variables.
type TxMode int

const (
	TxTenant TxMode = iota // Sets app.tenant_id, enforces RLS
	TxBypass               // Sets app.bypass_rls = on, skips RLS
)

func (db *DB) BeginTx(ctx context.Context, mode TxMode, tenantID string) (*sql.Tx, error) {
	tx, err := db.Pool.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}

	switch mode {
	case TxTenant:
		if _, err := tx.ExecContext(ctx, `SELECT set_config('app.bypass_rls', 'off', true)`); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		tid := strings.TrimSpace(tenantID)
		if tid == "" {
			tid = "default"
		}
		if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tid); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
	case TxBypass:
		if _, err := tx.ExecContext(ctx, `SELECT set_config('app.bypass_rls', 'on', true)`); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
	}

	return tx, nil
}

func Rollback(tx *sql.Tx) {
	if tx != nil {
		_ = tx.Rollback()
	}
}

// NewID generates a prefixed random ID (e.g., vlx_cus_a1b2c3d4e5f6).
func NewID(prefix string) string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(buf))
}

func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

func NullableString(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

func NullableTime(v *time.Time) any {
	if v == nil {
		return nil
	}
	return *v
}
