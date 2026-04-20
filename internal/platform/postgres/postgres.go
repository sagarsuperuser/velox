package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/xid"
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

// Context key for livemode propagation. Owned by the postgres package because
// BeginTx is the single choke point that needs to read it — having the key
// live here avoids circular imports between auth and postgres.
type livemodeCtxKey struct{}

// WithLivemode returns a derived context carrying the mode flag. BeginTx
// reads this to set app.livemode on the tx session, which the RLS policy
// uses to filter rows by mode alongside tenant.
func WithLivemode(ctx context.Context, live bool) context.Context {
	return context.WithValue(ctx, livemodeCtxKey{}, live)
}

// Livemode reads the livemode flag from ctx. Absent a value, defaults to
// true — the RLS policy interprets unset as "live mode" so background
// workers and bootstrap tooling that don't propagate mode operate safely
// against production data by default.
func Livemode(ctx context.Context) bool {
	v, ok := ctx.Value(livemodeCtxKey{}).(bool)
	if !ok {
		return true
	}
	return v
}

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
		mode := "on"
		if !Livemode(ctx) {
			mode = "off"
		}
		if _, err := tx.ExecContext(ctx, `SELECT set_config('app.livemode', $1, true)`, mode); err != nil {
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

// NewID generates a prefixed, time-sortable ID (e.g., vlx_cus_cv2q6ktjml6ng3v2q0tg).
// Uses xid: globally unique, 20-char, URL-safe, naturally ordered by creation time.
func NewID(prefix string) string {
	return fmt.Sprintf("%s_%s", prefix, xid.New().String())
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

// UniqueViolationConstraint returns the constraint name if err is a Postgres
// unique-violation (SQLSTATE 23505), otherwise "". Use this to disambiguate
// multiple unique constraints on the same table — e.g. subscriptions has
// both (tenant_id, code) and a partial-unique (tenant_id, customer_id, plan_id)
// for live statuses, and the callers need to surface distinct errors.
func UniqueViolationConstraint(err error) string {
	if err == nil {
		return ""
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return pgErr.ConstraintName
	}
	return ""
}

func IsForeignKeyViolation(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23503"
	}
	return false
}

func NullableString(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

func NullableFloat64(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func NullableTime(v *time.Time) any {
	if v == nil {
		return nil
	}
	return *v
}

// StringArray is a []string that implements sql.Scanner and driver.Valuer
// for PostgreSQL TEXT[] columns.
type StringArray []string

// Value converts the StringArray to a PostgreSQL array literal.
func (a StringArray) Value() (interface{}, error) {
	if a == nil {
		return "{}", nil
	}
	if len(a) == 0 {
		return "{}", nil
	}
	elements := make([]string, len(a))
	for i, s := range a {
		// Escape double quotes and backslashes inside elements
		escaped := strings.ReplaceAll(s, `\`, `\\`)
		escaped = strings.ReplaceAll(escaped, `"`, `\"`)
		elements[i] = `"` + escaped + `"`
	}
	return "{" + strings.Join(elements, ",") + "}", nil
}

// Scan parses a PostgreSQL TEXT[] value into a StringArray.
func (a *StringArray) Scan(src interface{}) error {
	if src == nil {
		*a = StringArray{}
		return nil
	}
	var s string
	switch v := src.(type) {
	case string:
		s = v
	case []byte:
		s = string(v)
	default:
		return fmt.Errorf("StringArray.Scan: unsupported type %T", src)
	}
	// Trim outer braces
	s = strings.TrimSpace(s)
	if s == "{}" || s == "" {
		*a = StringArray{}
		return nil
	}
	s = s[1 : len(s)-1] // Remove { and }
	var result []string
	var current strings.Builder
	inQuote := false
	escaped := false
	for _, ch := range s {
		if escaped {
			current.WriteRune(ch)
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == '"' {
			inQuote = !inQuote
			continue
		}
		if ch == ',' && !inQuote {
			result = append(result, current.String())
			current.Reset()
			continue
		}
		current.WriteRune(ch)
	}
	if current.Len() > 0 {
		result = append(result, current.String())
	}
	*a = result
	return nil
}
