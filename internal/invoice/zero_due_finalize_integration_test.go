package invoice_test

import (
	"context"
	"testing"

	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/invoice"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/tenant"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// TestManualFinalize_ZeroDue_SettlesPaid_E2E drives the real path an operator
// hits when finalizing a manual invoice that ends up with nothing due (today:
// an empty draft; tomorrow: a fully-discounted one) against Postgres: the
// invoice must land PAID — exercising the store's MarkPaid state-machine
// guard (finalized-only, tax ok) with real column defaults — not strand
// finalized/payment_pending as a permanently-overdue attention item
// (ADR-066 class; the manual writer's T12).
func TestManualFinalize_ZeroDue_SettlesPaid_E2E(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := postgres.WithLivemode(context.Background(), false)
	tenantID := testutil.CreateTestTenant(t, db, "Zero Due Corp")

	store := invoice.NewPostgresStore(db)
	svc := invoice.NewService(store, clock.Real(), tenant.NewSettingsStore(db))

	cust, err := customer.NewPostgresStore(db).Create(ctx, tenantID, domain.Customer{
		ExternalID: "cus_zero_1", DisplayName: "Zero Due",
	})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}

	draft, err := svc.Create(ctx, tenantID, invoice.CreateInput{CustomerID: cust.ID})
	if err != nil {
		t.Fatalf("create empty manual invoice: %v", err)
	}
	if draft.AmountDueCents != 0 || draft.Status != domain.InvoiceDraft {
		t.Fatalf("seed expectations: due=%d status=%s, want 0/draft", draft.AmountDueCents, draft.Status)
	}

	finalized, err := svc.Finalize(ctx, tenantID, draft.ID)
	if err != nil {
		t.Fatalf("finalize empty draft: %v", err)
	}
	if finalized.Status != domain.InvoiceFinalized {
		t.Fatalf("finalize: status=%s, want finalized", finalized.Status)
	}

	settled, err := svc.SettleZeroDue(ctx, tenantID, finalized.ID)
	if err != nil {
		t.Fatalf("SettleZeroDue: %v", err)
	}
	if settled.Status != domain.InvoicePaid || settled.PaymentStatus != domain.PaymentSucceeded {
		t.Errorf("settled = %s/%s, want paid/succeeded", settled.Status, settled.PaymentStatus)
	}

	got, err := store.Get(ctx, tenantID, finalized.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if got.Status != domain.InvoicePaid || got.PaymentStatus != domain.PaymentSucceeded {
		t.Errorf("stored = %s/%s, want paid/succeeded (zero-due must settle, never strand)", got.Status, got.PaymentStatus)
	}
	if got.PaidAt == nil {
		t.Error("paid_at must be stamped on the zero-due settle")
	}
}
