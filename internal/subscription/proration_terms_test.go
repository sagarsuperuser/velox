package subscription

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
)

// netTermsStub satisfies NetTermsReader with a fixed tenant setting.
type netTermsStub struct{ days int }

func (s netTermsStub) NetPaymentTermDays(_ context.Context, _ string) int { return s.days }

// runUpgradeProration drives the shared upgrade-proration harness (paid
// source → charge invoice) and returns the created proration invoice.
func runUpgradeProration(t *testing.T, configure func(*Handler)) domain.Invoice {
	t.Helper()
	ctx := clock.WithEffectiveNow(context.Background(), proNow)
	tenantID := "t1"

	store := newMemStore()
	subID, itemID := seedSubWithItemAt(t, store, tenantID, "cus_1", "plan_basic", proPeriodStart, proPeriodEnd)
	svc := NewService(store, nil)

	plans := &plansMock{plans: map[string]domain.Plan{
		"plan_basic": {ID: "plan_basic", Name: "Basic", BaseAmountCents: 2000, Currency: "USD", BaseBillTiming: domain.BillInAdvance},
		"plan_pro":   {ID: "plan_pro", Name: "Pro", BaseAmountCents: 6000, Currency: "USD", BaseBillTiming: domain.BillInAdvance},
	}}
	invoices := &invoicesMock{
		sourceInvoice: domain.Invoice{
			ID: "src_inv", PaymentStatus: domain.PaymentSucceeded,
			SubtotalCents: 2000, TotalAmountCents: 2000, AmountDueCents: 0, AmountPaidCents: 2000,
		},
	}

	h := NewHandler(svc)
	h.SetProrationDeps(plans, invoices, &creditsMock{})
	if configure != nil {
		configure(h)
	}

	body, _ := json.Marshal(UpdateItemInput{NewPlanID: "plan_pro", Immediate: true})
	req := updateItemURL(context.WithValue(ctx, auth.TestTenantIDKey(), tenantID), subID, itemID, body)
	rr := httptest.NewRecorder()
	h.updateItem(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200. body=%s", rr.Code, rr.Body.String())
	}
	if len(invoices.createdInvoices) != 1 {
		t.Fatalf("expected 1 proration charge invoice, got %d", len(invoices.createdInvoices))
	}
	return invoices.createdInvoices[0]
}

// TestUpdateItem_ProrationInvoiceStampsTenantNetTerms locks the fix for the
// terms drift: the proration invoice hardcoded Net 30 while engine
// cycle/create invoices read the tenant setting — so a Net-15 tenant's
// proration invoice showed different TERMS and a later due date than every
// sibling invoice, mis-timing dunning for send-invoice customers.
func TestUpdateItem_ProrationInvoiceStampsTenantNetTerms(t *testing.T) {
	inv := runUpgradeProration(t, func(h *Handler) {
		h.SetNetTermsReader(netTermsStub{days: 15})
	})

	if inv.NetPaymentTermDays != 15 {
		t.Errorf("NetPaymentTermDays: got %d, want 15 (the tenant setting, not the hardcoded 30)", inv.NetPaymentTermDays)
	}
	wantDue := proNow.AddDate(0, 0, 15)
	if inv.DueAt == nil || !inv.DueAt.Equal(wantDue) {
		t.Errorf("DueAt: got %v, want %v (issued_at + tenant Net terms)", inv.DueAt, wantDue)
	}
	if inv.BillingReason != domain.BillingReasonSubscriptionUpdate {
		t.Errorf("BillingReason: got %q, want %q (was persisted NULL pre-fix)", inv.BillingReason, domain.BillingReasonSubscriptionUpdate)
	}
}

// TestUpdateItem_ProrationInvoiceNetTermsFallback proves the unwired path
// (narrow tests, partial setups) keeps the prior Net-30 behavior instead of
// stamping garbage.
func TestUpdateItem_ProrationInvoiceNetTermsFallback(t *testing.T) {
	inv := runUpgradeProration(t, nil)

	if inv.NetPaymentTermDays != 30 {
		t.Errorf("NetPaymentTermDays: got %d, want fallback 30 when no NetTermsReader is wired", inv.NetPaymentTermDays)
	}
	wantDue := proNow.AddDate(0, 0, 30)
	if inv.DueAt == nil || !inv.DueAt.Equal(wantDue) {
		t.Errorf("DueAt: got %v, want %v", inv.DueAt, wantDue)
	}
}
