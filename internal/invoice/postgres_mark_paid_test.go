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

// TestMarkPaid_IdempotentPreservesAmountPaid is the regression for the audit
// MarkPaid finding (velox-ops): a DUPLICATE MarkPaid must not corrupt the
// recorded paid amount.
//
// The unknown-charge recovery path can settle the same charge twice under
// different Stripe event ids — the reconciler resolves a PaymentUnknown by
// querying Stripe and calling MarkPaid, and the real payment_intent.succeeded
// webhook then lands with a new event id (so event-id dedup does not catch it)
// and calls MarkPaid again. The buggy UPDATE set
// `amount_paid_cents = amount_due_cents`, and on the second call
// amount_due_cents was already 0, so it zeroed amount_paid_cents — corrupting
// the paid amount and blocking card refunds (refunds size against
// amount_paid_cents). The fix makes a re-mark on an already-paid invoice a
// true no-op on the money fields.
func TestMarkPaid_IdempotentPreservesAmountPaid(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := postgres.WithLivemode(context.Background(), false)

	store := invoice.NewPostgresStore(db)
	tenantID := testutil.CreateTestTenant(t, db, "MarkPaid Idempotency")
	invID := seedDraftInvoice(t, db, tenantID)

	// Give the invoice a positive balance and a resolved tax status so it can
	// be finalized and then marked paid.
	tx, err := db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		t.Fatalf("begin seed tx: %v", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE invoices
		    SET subtotal_cents = 1000, total_amount_cents = 1000,
		        amount_due_cents = 1000, tax_status = 'ok'
		  WHERE id = $1`, invID); err != nil {
		t.Fatalf("seed amounts: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit seed tx: %v", err)
	}

	if _, err := store.UpdateStatus(ctx, tenantID, invID, domain.InvoiceFinalized); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)

	first, err := store.MarkPaid(ctx, tenantID, invID, "pi_reconciler", now)
	if err != nil {
		t.Fatalf("first MarkPaid: %v", err)
	}
	if first.AmountPaidCents != 1000 || first.AmountDueCents != 0 {
		t.Fatalf("after first MarkPaid: amount_paid=%d amount_due=%d, want 1000/0",
			first.AmountPaidCents, first.AmountDueCents)
	}
	if first.Status != domain.InvoicePaid {
		t.Fatalf("after first MarkPaid: status=%q, want paid", first.Status)
	}

	// Duplicate MarkPaid — the late webhook with a different PI id resolving
	// the same charge the reconciler already settled. Must be a no-op on the
	// money fields.
	second, err := store.MarkPaid(ctx, tenantID, invID, "pi_late_webhook", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("second (duplicate) MarkPaid: %v", err)
	}
	if second.AmountPaidCents != 1000 {
		t.Errorf("REGRESSION: duplicate MarkPaid zeroed amount_paid_cents: got %d, want 1000 — refunds would be blocked",
			second.AmountPaidCents)
	}
	if second.AmountDueCents != 0 {
		t.Errorf("after duplicate MarkPaid: amount_due_cents=%d, want 0", second.AmountDueCents)
	}
	if second.Status != domain.InvoicePaid {
		t.Errorf("after duplicate MarkPaid: status=%q, want paid", second.Status)
	}
}
