package subscription

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
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
	createInvoiceErr    error
	nextNumberErr       error
	nextNumberCalls     int
	createdInvoices     []domain.Invoice
	createdLineItems    [][]domain.InvoiceLineItem
	lookupInvoice       domain.Invoice
	lookupInvoiceErr    error
	existingCreateCalls int
}

func (m *invoicesMock) CreateInvoiceWithLineItems(_ context.Context, tenantID string, inv domain.Invoice, items []domain.InvoiceLineItem) (domain.Invoice, error) {
	if m.createInvoiceErr != nil {
		return domain.Invoice{}, m.createInvoiceErr
	}
	inv.ID = fmt.Sprintf("vlx_inv_test_%d", len(m.createdInvoices)+1)
	inv.TenantID = tenantID
	m.createdInvoices = append(m.createdInvoices, inv)
	m.createdLineItems = append(m.createdLineItems, items)
	return inv, nil
}

func (m *invoicesMock) GetByProrationSource(_ context.Context, _, _, _ string, _ time.Time) (domain.Invoice, error) {
	m.existingCreateCalls++
	if m.lookupInvoiceErr != nil {
		return domain.Invoice{}, m.lookupInvoiceErr
	}
	return m.lookupInvoice, nil
}

func (m *invoicesMock) NextInvoiceNumber(_ context.Context, _ string) (string, error) {
	m.nextNumberCalls++
	if m.nextNumberErr != nil {
		return "", m.nextNumberErr
	}
	return "VLX-000042", nil
}

type creditsMock struct {
	grantErr       error
	grantCalls     []ProrationGrantInput
	lookupEntry    domain.CreditLedgerEntry
	lookupEntryErr error
}

func (m *creditsMock) GrantProration(_ context.Context, _ string, input ProrationGrantInput) error {
	m.grantCalls = append(m.grantCalls, input)
	return m.grantErr
}

func (m *creditsMock) GetByProrationSource(_ context.Context, _, _, _ string, _ time.Time) (domain.CreditLedgerEntry, error) {
	if m.lookupEntryErr != nil {
		return domain.CreditLedgerEntry{}, m.lookupEntryErr
	}
	return m.lookupEntry, nil
}

// seedSubWithItem creates an active subscription on `plan_old` for the given
// tenant and returns the subscription ID plus the seeded item ID. The billing
// period spans 30 days centered on now so proration_factor > 0.
func seedSubWithItem(t *testing.T, store *memStore, tenantID, custID, initialPlan string) (string, string) {
	t.Helper()
	now := time.Now().UTC()
	periodStart := now.Add(-15 * 24 * time.Hour)
	periodEnd := now.Add(15 * 24 * time.Hour)
	sub, err := store.Create(context.Background(), tenantID, domain.Subscription{
		Code: fmt.Sprintf("sub-%d", len(store.subs)+1),
		DisplayName: "Test Sub", CustomerID: custID,
		Status:                    domain.SubscriptionActive,
		BillingTime:               domain.BillingTimeCalendar,
		CurrentBillingPeriodStart: &periodStart,
		CurrentBillingPeriodEnd:   &periodEnd,
		Items:                     []domain.SubscriptionItem{{PlanID: initialPlan, Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("seed subscription: %v", err)
	}
	return sub.ID, sub.Items[0].ID
}

// updateItemURL builds the route URL for the per-item PATCH handler and wires
// the chi context with {id, itemID} params so chi.URLParam resolves them.
func updateItemURL(ctx context.Context, subID, itemID string, body []byte) *http.Request {
	url := fmt.Sprintf("/subscriptions/%s/items/%s", subID, itemID)
	req := httptest.NewRequest(http.MethodPatch, url, bytes.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", subID)
	rctx.URLParams.Add("itemID", itemID)
	return req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, rctx))
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestUpdateItem_ProrationFailureSurfacesAs500 locks in the fix for a silent
// data-loss bug: if proration generation fails after the plan change has
// already committed, the error must surface as a 500 with the distinct code
// "proration_failed". Prior behaviour was a 200 OK, leaving the customer on
// the new plan but never billed (or credited) for the mid-cycle swap.
func TestUpdateItem_ProrationFailureSurfacesAs500(t *testing.T) {
	ctx := context.Background()
	tenantID := "t1"

	store := newMemStore()
	subID, itemID := seedSubWithItem(t, store, tenantID, "cus_1", "plan_old")
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

	body, _ := json.Marshal(UpdateItemInput{NewPlanID: "plan_new", Immediate: true})
	req := updateItemURL(context.WithValue(ctx, auth.TestTenantIDKey(), tenantID), subID, itemID, body)

	rr := httptest.NewRecorder()
	h.updateItem(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500. body=%s", rr.Code, rr.Body.String())
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
	// point of surfacing the error: state is divergent, and the client has to
	// know so they can reconcile.
	storedItem, err := store.GetItem(ctx, tenantID, itemID)
	if err != nil {
		t.Fatalf("reload item: %v", err)
	}
	if storedItem.PlanID != "plan_new" {
		t.Errorf("item plan_id: got %q, want %q (plan change should commit even when proration fails)",
			storedItem.PlanID, "plan_new")
	}
}

// TestUpdateItem_ProrationDedup_UpgradeReturnsExisting exercises the
// idempotent-retry path: if a proration invoice for the same (subscription,
// item, plan_changed_at) already exists, the store returns errs.ErrAlreadyExists
// and the handler re-fetches the existing invoice rather than creating a duplicate.
//
// Without this, an operator-triggered retry (or any worker that recomputes
// proration from subscription state) would silently double-bill on the
// upgrade side, or grant double credits on the downgrade side.
func TestUpdateItem_ProrationDedup_UpgradeReturnsExisting(t *testing.T) {
	ctx := context.Background()
	tenantID := "t1"

	store := newMemStore()
	subID, itemID := seedSubWithItem(t, store, tenantID, "cus_1", "plan_old")
	svc := NewService(store, nil)

	plans := &plansMock{plans: map[string]domain.Plan{
		"plan_old": {ID: "plan_old", Name: "Basic", BaseAmountCents: 1000, Currency: "USD"},
		"plan_new": {ID: "plan_new", Name: "Pro", BaseAmountCents: 3000, Currency: "USD"},
	}}

	existingAt := time.Now().UTC().Add(-time.Minute)
	existingInvoice := domain.Invoice{
		ID:                       "vlx_inv_prior",
		TenantID:                 tenantID,
		SubscriptionID:           subID,
		InvoiceNumber:            "VLX-000001",
		Status:                   domain.InvoiceFinalized,
		SourcePlanChangedAt:      &existingAt,
		SourceSubscriptionItemID: itemID,
	}
	// CreateInvoiceWithLineItems returns ErrAlreadyExists to emulate the
	// unique partial index firing. The handler must then call
	// GetByProrationSource and surface that result instead of bubbling up the
	// error.
	invoices := &invoicesMock{
		createInvoiceErr: errs.ErrAlreadyExists,
		lookupInvoice:    existingInvoice,
	}
	credits := &creditsMock{}

	h := NewHandler(svc)
	h.SetProrationDeps(plans, invoices, credits)

	body, _ := json.Marshal(UpdateItemInput{NewPlanID: "plan_new", Immediate: true})
	req := updateItemURL(context.WithValue(ctx, auth.TestTenantIDKey(), tenantID), subID, itemID, body)

	rr := httptest.NewRecorder()
	h.updateItem(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (dedup should succeed as idempotent retry), body=%s",
			rr.Code, rr.Body.String())
	}
	if invoices.existingCreateCalls != 1 {
		t.Errorf("GetByProrationSource call count: got %d, want 1 (must be called on ErrAlreadyExists)",
			invoices.existingCreateCalls)
	}
	if len(invoices.createdInvoices) != 0 {
		t.Errorf("createdInvoices: got %d, want 0 (dedup must not create a new invoice)", len(invoices.createdInvoices))
	}

	var resp ItemChangeResult
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Proration == nil {
		t.Fatal("response missing proration detail")
	}
	if resp.Proration.InvoiceID != existingInvoice.ID {
		t.Errorf("proration.invoice_id: got %q, want %q (existing id)",
			resp.Proration.InvoiceID, existingInvoice.ID)
	}
	if resp.Proration.Type != "invoice" {
		t.Errorf("proration.type: got %q, want %q", resp.Proration.Type, "invoice")
	}
}

// TestUpdateItem_ProrationDedup_DowngradeReturnsExisting exercises the
// credit-ledger side of proration dedup. Same failure mode as the upgrade
// variant: a retry with the same (subscription, item, plan_changed_at) must
// return the prior credit entry, never double-grant.
func TestUpdateItem_ProrationDedup_DowngradeReturnsExisting(t *testing.T) {
	ctx := context.Background()
	tenantID := "t1"

	store := newMemStore()
	subID, itemID := seedSubWithItem(t, store, tenantID, "cus_2", "plan_pro")
	svc := NewService(store, nil)

	// Downgrade: Pro (3000) → Basic (1000).
	plans := &plansMock{plans: map[string]domain.Plan{
		"plan_pro":   {ID: "plan_pro", Name: "Pro", BaseAmountCents: 3000, Currency: "USD"},
		"plan_basic": {ID: "plan_basic", Name: "Basic", BaseAmountCents: 1000, Currency: "USD"},
	}}

	existingEntry := domain.CreditLedgerEntry{
		ID:                       "vlx_ccl_prior",
		TenantID:                 tenantID,
		CustomerID:               "cus_2",
		EntryType:                domain.CreditGrant,
		AmountCents:              987, // arbitrary — handler must surface this, not recompute.
		SourceSubscriptionID:     subID,
		SourceSubscriptionItemID: itemID,
	}

	invoices := &invoicesMock{}
	credits := &creditsMock{
		grantErr:    errs.ErrAlreadyExists,
		lookupEntry: existingEntry,
	}

	h := NewHandler(svc)
	h.SetProrationDeps(plans, invoices, credits)

	body, _ := json.Marshal(UpdateItemInput{NewPlanID: "plan_basic", Immediate: true})
	req := updateItemURL(context.WithValue(ctx, auth.TestTenantIDKey(), tenantID), subID, itemID, body)

	rr := httptest.NewRecorder()
	h.updateItem(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	if len(credits.grantCalls) != 1 {
		t.Errorf("GrantProration call count: got %d, want 1", len(credits.grantCalls))
	}

	var resp ItemChangeResult
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Proration == nil {
		t.Fatal("response missing proration detail")
	}
	if resp.Proration.Type != "credit" {
		t.Errorf("proration.type: got %q, want %q", resp.Proration.Type, "credit")
	}
	if resp.Proration.AmountCents != existingEntry.AmountCents {
		t.Errorf("proration.amount_cents: got %d, want %d (must surface existing entry's amount, not recomputed)",
			resp.Proration.AmountCents, existingEntry.AmountCents)
	}
}

// prorationTaxApplierMock records calls and returns a canned result. Used to
// verify the proration handler threads tax fields onto the invoice when an
// applier is wired.
type prorationTaxApplierMock struct {
	calls  int
	result ProrationTaxResult
	err    error
}

func (m *prorationTaxApplierMock) ApplyTaxToLineItems(_ context.Context, _, _, _ string, subtotal, discount int64, lineItems []domain.InvoiceLineItem) (ProrationTaxResult, error) {
	m.calls++
	if m.err != nil {
		return ProrationTaxResult{}, m.err
	}
	// Mutate line items like the real engine: split invoice tax into first line.
	if len(lineItems) > 0 && m.result.TaxAmountCents > 0 {
		lineItems[0].TaxRateBP = m.result.TaxRateBP
		lineItems[0].TaxAmountCents = m.result.TaxAmountCents
		lineItems[0].TotalAmountCents = lineItems[0].AmountCents + m.result.TaxAmountCents
	}
	return m.result, nil
}

// TestUpdateItem_ProrationAppliesTax locks in COR-2: proration invoices must
// carry tax. Prior behaviour was tax-free proration even when the customer
// had a configured rate.
func TestUpdateItem_ProrationAppliesTax(t *testing.T) {
	ctx := context.Background()
	tenantID := "t1"

	store := newMemStore()
	subID, itemID := seedSubWithItem(t, store, tenantID, "cus_1", "plan_old")
	svc := NewService(store, nil)

	plans := &plansMock{plans: map[string]domain.Plan{
		"plan_old": {ID: "plan_old", Name: "Basic", BaseAmountCents: 1000, Currency: "USD"},
		"plan_new": {ID: "plan_new", Name: "Pro", BaseAmountCents: 3000, Currency: "USD"},
	}}

	invoices := &invoicesMock{}
	credits := &creditsMock{}
	taxMock := &prorationTaxApplierMock{
		result: ProrationTaxResult{
			TaxAmountCents: 185,
			TaxRateBP:      1850,
			TaxName:        "VAT",
			TaxCountry:     "GB",
		},
	}

	h := NewHandler(svc)
	h.SetProrationDeps(plans, invoices, credits)
	h.SetProrationTaxApplier(taxMock)

	body, _ := json.Marshal(UpdateItemInput{NewPlanID: "plan_new", Immediate: true})
	req := updateItemURL(context.WithValue(ctx, auth.TestTenantIDKey(), tenantID), subID, itemID, body)

	rr := httptest.NewRecorder()
	h.updateItem(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200. body=%s", rr.Code, rr.Body.String())
	}
	if taxMock.calls != 1 {
		t.Errorf("tax applier called %d times, want 1", taxMock.calls)
	}
	if len(invoices.createdInvoices) != 1 {
		t.Fatalf("got %d invoices, want 1", len(invoices.createdInvoices))
	}

	inv := invoices.createdInvoices[0]
	if inv.TaxAmountCents != 185 {
		t.Errorf("invoice TaxAmountCents = %d, want 185", inv.TaxAmountCents)
	}
	if inv.TaxRateBP != 1850 {
		t.Errorf("invoice TaxRateBP = %d, want 1850", inv.TaxRateBP)
	}
	if inv.TaxName != "VAT" {
		t.Errorf("invoice TaxName = %q, want VAT", inv.TaxName)
	}
	// SubtotalCents should remain the pre-discount proration amount;
	// TotalAmountCents should add tax on top of discounted subtotal (no
	// discount here → subtotal + tax).
	wantTotal := inv.SubtotalCents - inv.DiscountCents + inv.TaxAmountCents
	if inv.TotalAmountCents != wantTotal {
		t.Errorf("invoice total = %d, want %d (subtotal - discount + tax)", inv.TotalAmountCents, wantTotal)
	}
	if inv.AmountDueCents != wantTotal {
		t.Errorf("invoice amount_due = %d, want %d", inv.AmountDueCents, wantTotal)
	}
}
