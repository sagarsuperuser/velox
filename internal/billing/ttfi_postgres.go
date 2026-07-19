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
	// scanned into sql.NullTime. The where clause matches the finalize row as the
	// TWO writers actually emit it — invoice.Service.Finalize (the operator +
	// tax-retry chain) and billing.Engine.emitFinalizeAuditTx, which builds the
	// row via finalizeAuditEntry (born-finalized engine invoices; ADR-090
	// replaced the old auditInvoiceFinalized writer).
	// The handler used to write its own copy; that was removed
	// as a duplicate, so any comment pointing at invoice/handler.go for this row
	// is pointing at a writer that no longer exists.
	//
	// Explicit tenant_id + livemode predicates for the same reason as
	// audit.Query (PR2 of the audit e2e arc): the RLS policy's column-free
	// bypass OR-arm blocks planner-derived index quals, so RLS-only quals
	// made this MIN() a scan across ALL tenants' audit rows; with them it
	// can descend the 0151 filter indexes (idx_audit_log_action leads with
	// exactly these tenant_id/livemode/action quals; 0151 dropped the old
	// idx_audit_log_resource). Values mirror the GUCs BeginTx set
	// from this same ctx.
	var t sql.NullTime
	err = tx.QueryRowContext(ctx, `
		SELECT MIN(created_at)
		FROM audit_log
		WHERE tenant_id = $1
		  AND livemode = $2
		  AND action = $3
		  AND resource_type = $4
	`, tenantID, postgres.Livemode(ctx), domain.AuditActionFinalize, "invoice").Scan(&t)
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
