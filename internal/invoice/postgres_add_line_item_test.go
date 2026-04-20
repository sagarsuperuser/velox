package invoice_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/invoice"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/pricing"
	"github.com/sagarsuperuser/velox/internal/subscription"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// TestAddLineItemAtomic_ConcurrentAdds is the regression test for COR-4a:
// the previous implementation issued CreateLineItem, ListLineItems, and
// UpdateTotals as separate transactions, so two goroutines adding line
// items at the same time could each read the subtotal before the other
// wrote, then both write back a stale sum — a classic lost-update race.
// The atomic version locks the invoice row FOR UPDATE, so the second
// caller blocks until the first commits and sees the updated subtotal.
func TestAddLineItemAtomic_ConcurrentAdds(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := context.Background()

	store := invoice.NewPostgresStore(db)
	tenantID := testutil.CreateTestTenant(t, db, "AddLineItem Concurrency")
	invID := seedDraftInvoice(t, db, tenantID)

	const (
		goroutines = 8
		perG       = 5 // each goroutine appends 5 lines
		unitCents  = int64(100)
		qty        = int64(1)
	)

	var wg sync.WaitGroup
	errCh := make(chan error, goroutines*perG)

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				_, _, err := store.AddLineItemAtomic(ctx, tenantID, invID, domain.InvoiceLineItem{
					LineType:         domain.LineTypeAddOn,
					Description:      "concurrent add",
					Quantity:         qty,
					UnitAmountCents:  unitCents,
					AmountCents:      qty * unitCents,
					TotalAmountCents: qty * unitCents,
				})
				if err != nil {
					errCh <- err
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent add: %v", err)
	}

	inv, err := store.Get(ctx, tenantID, invID)
	if err != nil {
		t.Fatalf("get final invoice: %v", err)
	}

	expected := int64(goroutines*perG) * qty * unitCents
	if inv.SubtotalCents != expected {
		t.Fatalf("lost-update race: subtotal = %d, want %d (concurrent writes were not serialized)",
			inv.SubtotalCents, expected)
	}
	if inv.TotalAmountCents != expected {
		t.Fatalf("total_amount_cents = %d, want %d", inv.TotalAmountCents, expected)
	}
	if inv.AmountDueCents != expected {
		t.Fatalf("amount_due_cents = %d, want %d", inv.AmountDueCents, expected)
	}

	items, err := store.ListLineItems(ctx, tenantID, invID)
	if err != nil {
		t.Fatalf("list line items: %v", err)
	}
	if len(items) != goroutines*perG {
		t.Fatalf("line item count = %d, want %d", len(items), goroutines*perG)
	}
}

// TestAddLineItemAtomic_RejectsNonDraft ensures the status check happens
// inside the locking tx: callers cannot add a line item to a finalized
// or voided invoice, even if they race a concurrent Finalize call.
func TestAddLineItemAtomic_RejectsNonDraft(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := context.Background()

	store := invoice.NewPostgresStore(db)
	tenantID := testutil.CreateTestTenant(t, db, "AddLineItem NonDraft")
	invID := seedDraftInvoice(t, db, tenantID)

	if _, err := store.UpdateStatus(ctx, tenantID, invID, domain.InvoiceFinalized); err != nil {
		t.Fatalf("finalize invoice: %v", err)
	}

	_, _, err := store.AddLineItemAtomic(ctx, tenantID, invID, domain.InvoiceLineItem{
		LineType:         domain.LineTypeAddOn,
		Description:      "rejected",
		Quantity:         1,
		UnitAmountCents:  500,
		AmountCents:      500,
		TotalAmountCents: 500,
	})
	if err == nil {
		t.Fatal("expected error adding line item to finalized invoice, got nil")
	}
}

// seedDraftInvoice creates the minimum fixture chain (customer → plan →
// subscription → invoice) and returns the draft invoice ID.
func seedDraftInvoice(t *testing.T, db *postgres.DB, tenantID string) string {
	t.Helper()
	ctx := context.Background()

	cust, err := customer.NewPostgresStore(db).Create(ctx, tenantID, domain.Customer{
		ExternalID: "cus_add_line_item_test", DisplayName: "Line Item Tester",
	})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}

	plan, err := pricing.NewPostgresStore(db).CreatePlan(ctx, tenantID, domain.Plan{
		Code: "line-item-plan", Name: "Line Item Plan", Currency: "USD",
		BillingInterval: domain.BillingMonthly, Status: domain.PlanActive,
		BaseAmountCents: 0,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}

	periodStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	sub, err := subscription.NewPostgresStore(db).Create(ctx, tenantID, domain.Subscription{
		Code: "sub-line-item", DisplayName: "Line Item Sub",
		CustomerID: cust.ID,
		Status:     domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
		StartedAt: &periodStart,
		Items:     []domain.SubscriptionItem{{PlanID: plan.ID, Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("create subscription: %v", err)
	}

	issuedAt := periodStart
	dueAt := periodStart.AddDate(0, 0, 30)
	inv, err := invoice.NewPostgresStore(db).Create(ctx, tenantID, domain.Invoice{
		CustomerID: cust.ID, SubscriptionID: sub.ID,
		InvoiceNumber:      "VLX-LINEITEM-001",
		Status:             domain.InvoiceDraft,
		PaymentStatus:      domain.PaymentPending,
		Currency:           "USD",
		BillingPeriodStart: periodStart,
		BillingPeriodEnd:   periodStart.AddDate(0, 1, 0),
		IssuedAt:           &issuedAt,
		DueAt:              &dueAt,
		NetPaymentTermDays: 30,
	})
	if err != nil {
		t.Fatalf("create invoice: %v", err)
	}
	return inv.ID
}
