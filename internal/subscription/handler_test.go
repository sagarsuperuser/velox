package subscription

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
)

// ---------------------------------------------------------------------------
// Mocks for handler-level tests. Kept in this file because the service-level
// mocks in service_test.go don't cover the proration dependency set.
// ---------------------------------------------------------------------------

type plansMock struct{ plans map[string]domain.Plan }

func (m *plansMock) GetPlan(_ context.Context, _, id string) (domain.Plan, error) {
	p, ok := m.plans[id]
	if !ok {
		return domain.Plan{}, errors.New("plan not found")
	}
	return p, nil
}

type invoicesMock struct {
	createInvoiceErr error
	nextNumberErr    error
	nextNumberCalls  int
	createdInvoices  []domain.Invoice
}

func (m *invoicesMock) CreateInvoice(_ context.Context, tenantID string, inv domain.Invoice) (domain.Invoice, error) {
	if m.createInvoiceErr != nil {
		return domain.Invoice{}, m.createInvoiceErr
	}
	inv.ID = "vlx_inv_test"
	inv.TenantID = tenantID
	m.createdInvoices = append(m.createdInvoices, inv)
	return inv, nil
}

func (m *invoicesMock) CreateLineItem(_ context.Context, _ string, item domain.InvoiceLineItem) (domain.InvoiceLineItem, error) {
	item.ID = "vlx_ili_test"
	return item, nil
}

func (m *invoicesMock) NextInvoiceNumber(_ context.Context, _ string) (string, error) {
	m.nextNumberCalls++
	if m.nextNumberErr != nil {
		return "", m.nextNumberErr
	}
	return "VLX-000042", nil
}

type creditsMock struct{ grantErr error }

func (m *creditsMock) Grant(_ context.Context, _, _ string, _ int64, _ string) error {
	return m.grantErr
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestChangePlan_ProrationFailureSurfacesAs500 locks in the fix for a silent
// data-loss bug: previously, if proration generation failed after the plan
// change committed, the error was logged and the client received 200 OK.
// That meant customers ended up on new plans without ever being charged the
// upgrade proration (or without receiving their downgrade credit).
//
// The handler must now return 500 with the distinct code "proration_failed"
// so clients and operators can distinguish this partial-success state from
// either a total failure or a clean success.
func TestChangePlan_ProrationFailureSurfacesAs500(t *testing.T) {
	ctx := context.Background()
	tenantID := "t1"

	// Seed a subscription with a billing period so proration factor > 0.
	now := time.Now().UTC()
	periodStart := now.Add(-15 * 24 * time.Hour)
	periodEnd := now.Add(15 * 24 * time.Hour)
	store := &memStore{
		subs: map[string]domain.Subscription{
			"sub_1": {
				ID: "sub_1", TenantID: tenantID, CustomerID: "cus_1",
				PlanID: "plan_old", Status: domain.SubscriptionActive,
				CurrentBillingPeriodStart: &periodStart,
				CurrentBillingPeriodEnd:   &periodEnd,
			},
		},
	}
	svc := NewService(store, nil)

	plans := &plansMock{plans: map[string]domain.Plan{
		"plan_old": {ID: "plan_old", Name: "Basic", BaseAmountCents: 1000, Currency: "USD"},
		"plan_new": {ID: "plan_new", Name: "Pro", BaseAmountCents: 3000, Currency: "USD"},
	}}
	// NextInvoiceNumber fails → proration generation fails after the plan
	// change has already committed.
	invoices := &invoicesMock{nextNumberErr: errors.New("sequence unavailable")}
	credits := &creditsMock{}

	h := NewHandler(svc)
	h.SetProrationDeps(plans, invoices, credits)

	body, _ := json.Marshal(ChangePlanInput{NewPlanID: "plan_new", Immediate: true})
	req := httptest.NewRequest(http.MethodPost, "/subscriptions/sub_1/change-plan", bytes.NewReader(body))

	reqCtx := context.WithValue(ctx, auth.TestTenantIDKey(), tenantID)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "sub_1")
	reqCtx = context.WithValue(reqCtx, chi.RouteCtxKey, rctx)
	req = req.WithContext(reqCtx)

	rr := httptest.NewRecorder()
	h.changePlan(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500", rr.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("missing error object in response: %s", rr.Body.String())
	}
	if code, _ := errObj["code"].(string); code != "proration_failed" {
		t.Errorf("error.code: got %q, want %q", code, "proration_failed")
	}

	// The plan change itself must still have committed — that's the whole
	// point of surfacing the error: state is divergent, and the client has
	// to know so they can reconcile.
	stored := store.subs["sub_1"]
	if stored.PlanID != "plan_new" {
		t.Errorf("subscription plan_id: got %q, want %q (plan change should commit even when proration fails)",
			stored.PlanID, "plan_new")
	}
}
