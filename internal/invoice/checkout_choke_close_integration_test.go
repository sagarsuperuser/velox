package invoice_test

import (
	"context"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/invoice"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// seedOpenClaim inserts an open checkout claim for the invoice (raw SQL —
// the payment package owns the store; these tests lock the INVOICE store's
// choke-point behavior).
func seedOpenClaim(t *testing.T, db *postgres.DB, ctx context.Context, tenantID, invoiceID string) string {
	t.Helper()
	id := postgres.NewID("vlx_cks")
	tx, err := db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer postgres.Rollback(tx)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO checkout_sessions (id, tenant_id, invoice_id, livemode, amount_cents, currency, status)
		VALUES ($1, $2, $3, false, 1000, 'USD', 'open')
	`, id, tenantID, invoiceID); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return id
}

func claimStatus(t *testing.T, db *postgres.DB, ctx context.Context, tenantID, claimID string) string {
	t.Helper()
	tx, err := db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer postgres.Rollback(tx)
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM checkout_sessions WHERE id = $1`, claimID).Scan(&status); err != nil {
		t.Fatalf("read claim: %v", err)
	}
	return status
}

// TestChokePointCloses locks the ADR-068 rule that EVERY exit from the
// payable state closes open checkout claims IN the exiting transaction —
// at the store choke points, not per-call-site hooks. Mutation seam: strip
// any of the three close statements and its subtest fails with status
// 'open' (the pre-fix "stored open session for a settled invoice" race).
func TestChokePointCloses(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: skipped in -short mode")
	}
	db := testutil.SetupTestDB(t)
	ctx := postgres.WithLivemode(context.Background(), false)
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("mark paid settles claims", func(t *testing.T) {
		tenantID := testutil.CreateTestTenant(t, db, "Choke MarkPaid")
		store := invoice.NewPostgresStore(db)
		invID := seedFinalizedInvoice(t, db, store, ctx, tenantID)
		claimID := seedOpenClaim(t, db, ctx, tenantID, invID)
		if _, err := store.MarkPaid(ctx, tenantID, invID, "pi_x", now); err != nil {
			t.Fatalf("mark paid: %v", err)
		}
		if got := claimStatus(t, db, ctx, tenantID, claimID); got != "invoice_settled" {
			t.Fatalf("claim after MarkPaid = %q, want invoice_settled (in-tx choke close)", got)
		}
	})

	t.Run("void settles claims", func(t *testing.T) {
		tenantID := testutil.CreateTestTenant(t, db, "Choke Void")
		store := invoice.NewPostgresStore(db)
		invID := seedFinalizedInvoice(t, db, store, ctx, tenantID)
		claimID := seedOpenClaim(t, db, ctx, tenantID, invID)
		if _, err := store.UpdateStatus(ctx, tenantID, invID, domain.InvoiceVoided); err != nil {
			t.Fatalf("void: %v", err)
		}
		if got := claimStatus(t, db, ctx, tenantID, claimID); got != "invoice_settled" {
			t.Fatalf("claim after void = %q, want invoice_settled", got)
		}
	})

	t.Run("credit application supersedes claims", func(t *testing.T) {
		tenantID := testutil.CreateTestTenant(t, db, "Choke Credit")
		store := invoice.NewPostgresStore(db)
		invID := seedFinalizedInvoice(t, db, store, ctx, tenantID)
		claimID := seedOpenClaim(t, db, ctx, tenantID, invID)
		// Partial credit: the open claim's minted amount is now stale — the
		// next POST must remint at the new amount.
		if _, err := store.ApplyCredits(ctx, tenantID, invID, 400); err != nil {
			t.Fatalf("apply credits: %v", err)
		}
		if got := claimStatus(t, db, ctx, tenantID, claimID); got != "superseded" {
			t.Fatalf("claim after partial credit = %q, want superseded", got)
		}
	})
}
