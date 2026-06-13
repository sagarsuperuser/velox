package invoice_test

import (
	"context"
	"database/sql"
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

// TestMarkPaidReportingTransition_FlagsTheRealTransition locks the store-level
// signal H7 relies on: the first MarkPaidReportingTransition on a finalized
// invoice reports transitioned=true; a second call on the now-paid invoice
// reports transitioned=false (the idempotent no-op branch). The SELECT … FOR
// UPDATE serializes concurrent callers, so for two at-least-once webhook
// deliveries of the same charge exactly one sees true — which is what lets
// SettleSucceeded fire the receipt email / payment.succeeded event once.
func TestMarkPaidReportingTransition_FlagsTheRealTransition(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := postgres.WithLivemode(context.Background(), false)

	store := invoice.NewPostgresStore(db)
	tenantID := testutil.CreateTestTenant(t, db, "MarkPaid Transition Flag")
	invID := seedDraftInvoice(t, db, tenantID)

	tx, err := db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		t.Fatalf("begin seed tx: %v", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE invoices SET subtotal_cents = 1000, total_amount_cents = 1000,
		    amount_due_cents = 1000, tax_status = 'ok' WHERE id = $1`, invID); err != nil {
		t.Fatalf("seed amounts: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit seed tx: %v", err)
	}
	if _, err := store.UpdateStatus(ctx, tenantID, invID, domain.InvoiceFinalized); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)

	_, transitioned, err := store.MarkPaidReportingTransition(ctx, tenantID, invID, "pi_1", now)
	if err != nil {
		t.Fatalf("first MarkPaidReportingTransition: %v", err)
	}
	if !transitioned {
		t.Error("first call on a finalized invoice must report transitioned=true")
	}

	_, transitioned2, err := store.MarkPaidReportingTransition(ctx, tenantID, invID, "pi_2", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("second MarkPaidReportingTransition: %v", err)
	}
	if transitioned2 {
		t.Error("second call on an already-paid invoice must report transitioned=false (idempotent no-op)")
	}
}

// recordingOutbox captures the events MarkPaid enqueues, ignoring the tx.
type recordingOutbox struct{ events []string }

func (r *recordingOutbox) Enqueue(_ context.Context, _ *sql.Tx, _, eventType string, _ map[string]any) (string, error) {
	r.events = append(r.events, eventType)
	return "vlx_whob_test", nil
}

// TestMarkPaid_FiresInvoicePaidOnceOnTransition asserts invoice.paid is emitted
// exactly once — on the real finalized→paid transition — and NOT on a duplicate
// MarkPaid of an already-paid invoice (the already-paid branch returns before
// the emit). This is what lets the single MarkPaid emit point cover every
// settlement path (card, credits, offline, dunning, reconciler fallback)
// without double-firing.
func TestMarkPaid_FiresInvoicePaidOnceOnTransition(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := postgres.WithLivemode(context.Background(), false)

	store := invoice.NewPostgresStore(db)
	rec := &recordingOutbox{}
	store.SetOutboxEnqueuer(rec)
	tenantID := testutil.CreateTestTenant(t, db, "MarkPaid InvoicePaid Event")
	invID := seedDraftInvoice(t, db, tenantID)

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

	// Real transition → exactly one invoice.paid.
	if _, err := store.MarkPaid(ctx, tenantID, invID, "pi_1", now); err != nil {
		t.Fatalf("first MarkPaid: %v", err)
	}
	if len(rec.events) != 1 || rec.events[0] != domain.EventInvoicePaid {
		t.Fatalf("after transition: got events %v, want exactly [%s]", rec.events, domain.EventInvoicePaid)
	}

	// Duplicate MarkPaid (already paid) → no new event.
	if _, err := store.MarkPaid(ctx, tenantID, invID, "pi_2", now.Add(time.Minute)); err != nil {
		t.Fatalf("duplicate MarkPaid: %v", err)
	}
	if len(rec.events) != 1 {
		t.Errorf("REGRESSION: duplicate MarkPaid re-emitted invoice.paid: got %v, want exactly one", rec.events)
	}
}
