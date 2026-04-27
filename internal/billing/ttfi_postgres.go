package billing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// PostgresTTFIReader implements MetricsTTFIReader against the live database.
//
// The audit-log query reads the earliest invoice.finalize entry for the
// tenant. We deliberately read the audit log instead of instrumenting the
// invoice service's Finalize hot path — keeps the invoice service free of
// telemetry coupling and lets backfills, deletions, or operator-driven
// re-finalizes (rare but possible) be answered correctly without remembering
// to re-emit a metric.
//
// audit_log is RLS-protected, so the read goes through BeginTx(TxTenant)
// — matches the pattern in internal/audit/audit.go. The tenants table is
// not RLS-protected (lookups go through the connection pool directly) —
// matches internal/tenant/postgres.go.
type PostgresTTFIReader struct {
	db *postgres.DB
}

func NewPostgresTTFIReader(db *postgres.DB) *PostgresTTFIReader {
	return &PostgresTTFIReader{db: db}
}

// FirstInvoiceFinalizedAt returns the earliest invoice.finalize audit log
// entry timestamp for the tenant, or nil if the tenant has never finalized
// an invoice.
func (r *PostgresTTFIReader) FirstInvoiceFinalizedAt(ctx context.Context, tenantID string) (*time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, r.db.QueryTimeout)
	defer cancel()

	tx, err := r.db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		return nil, fmt.Errorf("ttfi: begin tx: %w", err)
	}
	defer postgres.Rollback(tx)

	// MIN(created_at) over (action='finalize', resource_type='invoice') with
	// COALESCE-style NULL handling: an empty result set returns a SQL NULL
	// scanned into sql.NullTime. The where clause matches what
	// internal/invoice/handler.go:311 emits on Finalize:
	// auditLogger.Log(ctx, tenantID, AuditActionFinalize, "invoice", inv.ID, ...)
	var t sql.NullTime
	err = tx.QueryRowContext(ctx, `
		SELECT MIN(created_at)
		FROM audit_log
		WHERE action = $1
		  AND resource_type = $2
	`, domain.AuditActionFinalize, "invoice").Scan(&t)
	if err != nil {
		return nil, fmt.Errorf("ttfi: query first finalize: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("ttfi: commit: %w", err)
	}

	if !t.Valid {
		return nil, nil
	}
	ts := t.Time.UTC()
	return &ts, nil
}

// TenantCreatedAt returns the row creation timestamp for the tenant. The
// tenants table is not RLS-protected, so we query the pool directly —
// matches internal/tenant/postgres.go's pattern.
func (r *PostgresTTFIReader) TenantCreatedAt(ctx context.Context, tenantID string) (time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, r.db.QueryTimeout)
	defer cancel()

	var createdAt time.Time
	err := r.db.Pool.QueryRowContext(ctx, `
		SELECT created_at FROM tenants WHERE id = $1
	`, tenantID).Scan(&createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, fmt.Errorf("ttfi: tenant %q not found", tenantID)
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("ttfi: query tenant created_at: %w", err)
	}
	return createdAt.UTC(), nil
}
