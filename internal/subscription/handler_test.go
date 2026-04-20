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

func (m *invoicesMock) GetByProrationSource(_ context.Context, _, _, _ string, _ domain.ItemChangeType, _ time.Time) (domain.Invoice, error) {
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

func (m *creditsMock) GetByProrationSource(_ context.Context, _, _, _ string, _ domain.ItemChangeType, _ time.Time) (domain.CreditLedgerEntry, error) {
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
		Code:        fmt.Sprintf("sub-%d", len(store.subs)+1),
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

// ---------------------------------------------------------------------------
// Event dispatch tests — P0 #2. Design partners building on Velox need a
// webhook stream for subscription lifecycle the same way Stripe customers
// depend on customer.subscription.*. These tests pin the dispatch contract
// for every mutating handler so a silent regression can't ship.
// ---------------------------------------------------------------------------

type capturedEvent struct {
	tenantID  string
	eventType string
	payload   map[string]any
}

type capturingDispatcher struct {
	events []capturedEvent
	err    error
}

func (d *capturingDispatcher) Dispatch(_ context.Context, tenantID, eventType string, payload map[string]any) error {
	d.events = append(d.events, capturedEvent{tenantID: tenantID, eventType: eventType, payload: payload})
	return d.err
}

func (d *capturingDispatcher) firstOfType(eventType string) (capturedEvent, bool) {
	for _, e := range d.events {
		if e.eventType == eventType {
			return e, true
		}
	}
	return capturedEvent{}, false
}

func TestHandler_Cancel_FiresSubscriptionCanceled(t *testing.T) {
	ctx := context.Background()
	tenantID := "t1"
	store := newMemStore()
	subID, _ := seedSubWithItem(t, store, tenantID, "cus_1", "plan_old")
	svc := NewService(store, nil)

	dispatcher := &capturingDispatcher{}
	h := NewHandler(svc)
	h.SetEventDispatcher(dispatcher)

	req := httptest.NewRequest(http.MethodPost, "/subscriptions/"+subID+"/cancel", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", subID)
	req = req.WithContext(context.WithValue(
		context.WithValue(ctx, chi.RouteCtxKey, rctx),
		auth.TestTenantIDKey(), tenantID,
	))

	rr := httptest.NewRecorder()
	h.cancel(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200. body=%s", rr.Code, rr.Body.String())
	}

	ev, ok := dispatcher.firstOfType(domain.EventSubscriptionCanceled)
	if !ok {
		t.Fatalf("expected %s event, got types=%v", domain.EventSubscriptionCanceled, eventTypes(dispatcher.events))
	}
	if ev.tenantID != tenantID {
		t.Errorf("tenant_id: got %q, want %q", ev.tenantID, tenantID)
	}
	if ev.payload["subscription_id"] != subID {
		t.Errorf("payload subscription_id: got %v, want %q", ev.payload["subscription_id"], subID)
	}
	if ev.payload["customer_id"] != "cus_1" {
		t.Errorf("payload customer_id: got %v, want cus_1", ev.payload["customer_id"])
	}
}

func TestHandler_PauseResume_FireLifecycleEvents(t *testing.T) {
	ctx := context.Background()
	tenantID := "t1"
	store := newMemStore()
	subID, _ := seedSubWithItem(t, store, tenantID, "cus_1", "plan_old")
	svc := NewService(store, nil)

	dispatcher := &capturingDispatcher{}
	h := NewHandler(svc)
	h.SetEventDispatcher(dispatcher)

	tenantCtx := context.WithValue(ctx, auth.TestTenantIDKey(), tenantID)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", subID)

	// Pause
	pauseReq := httptest.NewRequest(http.MethodPost, "/subscriptions/"+subID+"/pause", nil).
		WithContext(context.WithValue(tenantCtx, chi.RouteCtxKey, routeCtx))
	pauseRR := httptest.NewRecorder()
	h.pause(pauseRR, pauseReq)
	if pauseRR.Code != http.StatusOK {
		t.Fatalf("pause status: got %d, want 200. body=%s", pauseRR.Code, pauseRR.Body.String())
	}
	if _, ok := dispatcher.firstOfType(domain.EventSubscriptionPaused); !ok {
		t.Errorf("expected %s after pause, got types=%v", domain.EventSubscriptionPaused, eventTypes(dispatcher.events))
	}

	// Resume
	resumeReq := httptest.NewRequest(http.MethodPost, "/subscriptions/"+subID+"/resume", nil).
		WithContext(context.WithValue(tenantCtx, chi.RouteCtxKey, routeCtx))
	resumeRR := httptest.NewRecorder()
	h.resume(resumeRR, resumeReq)
	if resumeRR.Code != http.StatusOK {
		t.Fatalf("resume status: got %d, want 200. body=%s", resumeRR.Code, resumeRR.Body.String())
	}
	if _, ok := dispatcher.firstOfType(domain.EventSubscriptionResumed); !ok {
		t.Errorf("expected %s after resume, got types=%v", domain.EventSubscriptionResumed, eventTypes(dispatcher.events))
	}
}

func TestHandler_UpdateItem_ImmediatePlanChangeFiresItemUpdated(t *testing.T) {
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

	dispatcher := &capturingDispatcher{}
	h := NewHandler(svc)
	h.SetProrationDeps(plans, invoices, credits)
	h.SetEventDispatcher(dispatcher)

	body, _ := json.Marshal(UpdateItemInput{NewPlanID: "plan_new", Immediate: true})
	req := updateItemURL(context.WithValue(ctx, auth.TestTenantIDKey(), tenantID), subID, itemID, body)
	rr := httptest.NewRecorder()
	h.updateItem(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200. body=%s", rr.Code, rr.Body.String())
	}

	ev, ok := dispatcher.firstOfType(domain.EventSubscriptionItemUpdated)
	if !ok {
		t.Fatalf("expected %s, got types=%v", domain.EventSubscriptionItemUpdated, eventTypes(dispatcher.events))
	}
	item, _ := ev.payload["item"].(map[string]any)
	if item == nil {
		t.Fatalf("payload.item missing: %v", ev.payload)
	}
	if item["item_id"] != itemID {
		t.Errorf("item_id: got %v, want %q", item["item_id"], itemID)
	}
	if item["plan_id"] != "plan_new" {
		t.Errorf("plan_id: got %v, want plan_new (post-change plan expected)", item["plan_id"])
	}
	// Scheduled event must NOT fire on immediate plan change.
	if _, fired := dispatcher.firstOfType(domain.EventSubscriptionPendingChangeScheduled); fired {
		t.Errorf("pending_change.scheduled fired on immediate plan change")
	}
}

func TestHandler_UpdateItem_ScheduledPlanChangeFiresPendingChangeScheduled(t *testing.T) {
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

	dispatcher := &capturingDispatcher{}
	h := NewHandler(svc)
	h.SetProrationDeps(plans, invoices, credits)
	h.SetEventDispatcher(dispatcher)

	body, _ := json.Marshal(UpdateItemInput{NewPlanID: "plan_new", Immediate: false})
	req := updateItemURL(context.WithValue(ctx, auth.TestTenantIDKey(), tenantID), subID, itemID, body)
	rr := httptest.NewRecorder()
	h.updateItem(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200. body=%s", rr.Code, rr.Body.String())
	}

	ev, ok := dispatcher.firstOfType(domain.EventSubscriptionPendingChangeScheduled)
	if !ok {
		t.Fatalf("expected %s, got types=%v", domain.EventSubscriptionPendingChangeScheduled, eventTypes(dispatcher.events))
	}
	if ev.payload["new_plan_id"] != "plan_new" {
		t.Errorf("new_plan_id: got %v, want plan_new", ev.payload["new_plan_id"])
	}
	// item.updated must NOT also fire for a scheduled change — the intent is
	// observable but the item hasn't mutated yet.
	if _, fired := dispatcher.firstOfType(domain.EventSubscriptionItemUpdated); fired {
		t.Errorf("item.updated fired on scheduled (non-immediate) plan change")
	}
}

func TestHandler_RemoveItem_FiresItemRemoved(t *testing.T) {
	ctx := context.Background()
	tenantID := "t1"

	store := newMemStore()
	// seed a sub with 2 items so RemoveItem doesn't trip the "last item"
	// guard.
	subID, firstItemID := seedSubWithItem(t, store, tenantID, "cus_1", "plan_a")
	svc := NewService(store, nil)
	if _, err := svc.AddItem(ctx, tenantID, subID, AddItemInput{PlanID: "plan_b", Quantity: 1}); err != nil {
		t.Fatalf("seed second item: %v", err)
	}

	dispatcher := &capturingDispatcher{}
	h := NewHandler(svc)
	h.SetEventDispatcher(dispatcher)

	req := httptest.NewRequest(http.MethodDelete, "/subscriptions/"+subID+"/items/"+firstItemID, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", subID)
	rctx.URLParams.Add("itemID", firstItemID)
	req = req.WithContext(context.WithValue(
		context.WithValue(ctx, chi.RouteCtxKey, rctx),
		auth.TestTenantIDKey(), tenantID,
	))
	rr := httptest.NewRecorder()
	h.removeItem(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status: got %d, want 204. body=%s", rr.Code, rr.Body.String())
	}

	ev, ok := dispatcher.firstOfType(domain.EventSubscriptionItemRemoved)
	if !ok {
		t.Fatalf("expected %s, got types=%v", domain.EventSubscriptionItemRemoved, eventTypes(dispatcher.events))
	}
	if ev.payload["item_id"] != firstItemID {
		t.Errorf("item_id: got %v, want %q", ev.payload["item_id"], firstItemID)
	}
}

// TestHandler_CancelPendingItemChange_FiresPendingChangeCanceled locks in that
// clearing a scheduled plan change surfaces as its own event type, so a
// webhook consumer listening for upgrade/downgrade flows can distinguish
// "scheduled but rolled back" from "applied at cycle boundary".
func TestHandler_CancelPendingItemChange_FiresPendingChangeCanceled(t *testing.T) {
	ctx := context.Background()
	tenantID := "t1"

	store := newMemStore()
	subID, itemID := seedSubWithItem(t, store, tenantID, "cus_1", "plan_old")
	svc := NewService(store, nil)

	plans := &plansMock{plans: map[string]domain.Plan{
		"plan_old": {ID: "plan_old", Name: "Basic", BaseAmountCents: 1000, Currency: "USD"},
		"plan_new": {ID: "plan_new", Name: "Pro", BaseAmountCents: 3000, Currency: "USD"},
	}}

	// Stage a scheduled change first so CancelPendingItemChange has something
	// to clear. The UpdateItem path drives this via svc so the scheduled fields
	// land on the item row.
	if _, err := svc.UpdateItem(ctx, tenantID, subID, itemID, UpdateItemInput{NewPlanID: "plan_new", Immediate: false}); err != nil {
		t.Fatalf("stage scheduled change: %v", err)
	}

	dispatcher := &capturingDispatcher{}
	h := NewHandler(svc)
	h.SetProrationDeps(plans, &invoicesMock{}, &creditsMock{})
	h.SetEventDispatcher(dispatcher)

	req := httptest.NewRequest(http.MethodDelete, "/subscriptions/"+subID+"/items/"+itemID+"/pending-change", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", subID)
	rctx.URLParams.Add("itemID", itemID)
	req = req.WithContext(context.WithValue(
		context.WithValue(ctx, chi.RouteCtxKey, rctx),
		auth.TestTenantIDKey(), tenantID,
	))
	rr := httptest.NewRecorder()
	h.cancelPendingItemChange(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200. body=%s", rr.Code, rr.Body.String())
	}

	if _, ok := dispatcher.firstOfType(domain.EventSubscriptionPendingChangeCanceled); !ok {
		t.Fatalf("expected %s, got types=%v", domain.EventSubscriptionPendingChangeCanceled, eventTypes(dispatcher.events))
	}
}

// ---------------------------------------------------------------------------
// Mid-cycle proration tests — P1 gap #188. Plan changes were the only mutation
// that drove proration; qty changes, item adds, and item removes silently
// skipped it. These tests pin the new behaviour so the invoice/credit is
// produced even when the billing engine doesn't touch the row.
// ---------------------------------------------------------------------------

// addItemURL builds the POST /subscriptions/{id}/items request with chi params.
func addItemURL(ctx context.Context, subID string, body []byte) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/subscriptions/"+subID+"/items", bytes.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", subID)
	return req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, rctx))
}

// removeItemURL builds the DELETE /subscriptions/{id}/items/{itemID} request.
func removeItemURL(ctx context.Context, subID, itemID string) *http.Request {
	url := fmt.Sprintf("/subscriptions/%s/items/%s", subID, itemID)
	req := httptest.NewRequest(http.MethodDelete, url, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", subID)
	rctx.URLParams.Add("itemID", itemID)
	return req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, rctx))
}

// TestAddItem_ProratesMidCycle verifies that adding a priced line to an active
// subscription mid-period emits a proration invoice for the partial-period
// portion of the new item's cost. Prior behaviour was silent skip — the
// customer got the new item for free until next cycle close.
func TestAddItem_ProratesMidCycle(t *testing.T) {
	ctx := context.Background()
	tenantID := "t1"

	store := newMemStore()
	subID, _ := seedSubWithItem(t, store, tenantID, "cus_1", "plan_existing")
	svc := NewService(store, nil)

	plans := &plansMock{plans: map[string]domain.Plan{
		"plan_existing": {ID: "plan_existing", Name: "Basic", BaseAmountCents: 1000, Currency: "USD"},
		"plan_new":      {ID: "plan_new", Name: "Add-on", BaseAmountCents: 2000, Currency: "USD"},
	}}
	invoices := &invoicesMock{}
	credits := &creditsMock{}

	h := NewHandler(svc)
	h.SetProrationDeps(plans, invoices, credits)

	body, _ := json.Marshal(AddItemInput{PlanID: "plan_new", Quantity: 1})
	req := addItemURL(context.WithValue(ctx, auth.TestTenantIDKey(), tenantID), subID, body)

	rr := httptest.NewRecorder()
	h.addItem(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201. body=%s", rr.Code, rr.Body.String())
	}
	if len(invoices.createdInvoices) != 1 {
		t.Fatalf("createdInvoices: got %d, want 1 — proration invoice must be emitted for mid-cycle add",
			len(invoices.createdInvoices))
	}
	inv := invoices.createdInvoices[0]
	if inv.SourceChangeType != domain.ItemChangeTypeAdd {
		t.Errorf("invoice SourceChangeType: got %q, want %q", inv.SourceChangeType, domain.ItemChangeTypeAdd)
	}
	if inv.SubscriptionID != subID {
		t.Errorf("invoice subscription_id: got %q, want %q", inv.SubscriptionID, subID)
	}
	// Factor is ~0.5 (seeded period is ±15 days, now is midpoint), so the
	// prorated amount on a $20 plan should be roughly $10. Accept 800-1200
	// to absorb any near-midnight drift.
	if inv.SubtotalCents < 800 || inv.SubtotalCents > 1200 {
		t.Errorf("invoice subtotal: got %d, want ~1000 (half of 2000)", inv.SubtotalCents)
	}
	if inv.AmountDueCents <= 0 {
		t.Errorf("invoice amount_due: got %d, want > 0", inv.AmountDueCents)
	}
	if len(credits.grantCalls) != 0 {
		t.Errorf("grantCalls: got %d, want 0 — add should bill not credit", len(credits.grantCalls))
	}
}

// TestUpdateItem_QuantityIncreaseProratesAsInvoice verifies a mid-cycle qty
// increase emits an invoice for the partial-period delta. Prior behaviour was
// silent skip, leaving the customer on the new qty with no delta billed.
func TestUpdateItem_QuantityIncreaseProratesAsInvoice(t *testing.T) {
	ctx := context.Background()
	tenantID := "t1"

	store := newMemStore()
	subID, itemID := seedSubWithItem(t, store, tenantID, "cus_1", "plan_seats")
	svc := NewService(store, nil)

	plans := &plansMock{plans: map[string]domain.Plan{
		"plan_seats": {ID: "plan_seats", Name: "Seat", BaseAmountCents: 1000, Currency: "USD"},
	}}
	invoices := &invoicesMock{}
	credits := &creditsMock{}

	h := NewHandler(svc)
	h.SetProrationDeps(plans, invoices, credits)

	// Seed starts at qty=1; bump to 3 → delta = 2 seats × $10 = $20 per full
	// period, half-period → ~$10.
	newQty := int64(3)
	body, _ := json.Marshal(UpdateItemInput{Quantity: &newQty})
	req := updateItemURL(context.WithValue(ctx, auth.TestTenantIDKey(), tenantID), subID, itemID, body)

	rr := httptest.NewRecorder()
	h.updateItem(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200. body=%s", rr.Code, rr.Body.String())
	}
	if len(invoices.createdInvoices) != 1 {
		t.Fatalf("createdInvoices: got %d, want 1 — qty increase must invoice the delta",
			len(invoices.createdInvoices))
	}
	inv := invoices.createdInvoices[0]
	if inv.SourceChangeType != domain.ItemChangeTypeQuantity {
		t.Errorf("invoice SourceChangeType: got %q, want %q", inv.SourceChangeType, domain.ItemChangeTypeQuantity)
	}
	if inv.SourceSubscriptionItemID != itemID {
		t.Errorf("invoice item_id: got %q, want %q", inv.SourceSubscriptionItemID, itemID)
	}
	if inv.SubtotalCents < 800 || inv.SubtotalCents > 1200 {
		t.Errorf("invoice subtotal: got %d, want ~1000 (half of 2000 delta)", inv.SubtotalCents)
	}
	if len(credits.grantCalls) != 0 {
		t.Errorf("grantCalls: got %d, want 0 — qty increase should bill not credit", len(credits.grantCalls))
	}
}

// TestUpdateItem_QuantityDecreaseProratesAsCredit verifies a mid-cycle qty
// reduction issues a credit for the unused portion of the removed seats.
func TestUpdateItem_QuantityDecreaseProratesAsCredit(t *testing.T) {
	ctx := context.Background()
	tenantID := "t1"

	store := newMemStore()
	subID, itemID := seedSubWithItem(t, store, tenantID, "cus_1", "plan_seats")
	svc := NewService(store, nil)
	// Start at qty=3 so we can drop to 1.
	startQty := int64(3)
	if _, err := svc.UpdateItem(ctx, tenantID, subID, itemID, UpdateItemInput{Quantity: &startQty}); err != nil {
		t.Fatalf("seed qty=3: %v", err)
	}

	plans := &plansMock{plans: map[string]domain.Plan{
		"plan_seats": {ID: "plan_seats", Name: "Seat", BaseAmountCents: 1000, Currency: "USD"},
	}}
	invoices := &invoicesMock{}
	credits := &creditsMock{}

	h := NewHandler(svc)
	h.SetProrationDeps(plans, invoices, credits)

	// Drop 3 → 1 = 2 seats × $10 = $20 full-period, half-period → ~$10 credit.
	newQty := int64(1)
	body, _ := json.Marshal(UpdateItemInput{Quantity: &newQty})
	req := updateItemURL(context.WithValue(ctx, auth.TestTenantIDKey(), tenantID), subID, itemID, body)

	rr := httptest.NewRecorder()
	h.updateItem(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200. body=%s", rr.Code, rr.Body.String())
	}
	if len(invoices.createdInvoices) != 0 {
		t.Errorf("createdInvoices: got %d, want 0 — qty decrease should credit not invoice",
			len(invoices.createdInvoices))
	}
	if len(credits.grantCalls) != 1 {
		t.Fatalf("grantCalls: got %d, want 1 — qty decrease must credit the unused portion",
			len(credits.grantCalls))
	}
	call := credits.grantCalls[0]
	if call.SourceChangeType != domain.ItemChangeTypeQuantity {
		t.Errorf("credit SourceChangeType: got %q, want %q", call.SourceChangeType, domain.ItemChangeTypeQuantity)
	}
	if call.AmountCents < 800 || call.AmountCents > 1200 {
		t.Errorf("credit amount: got %d, want ~1000", call.AmountCents)
	}
}

// TestRemoveItem_ProratesAsCredit verifies mid-cycle item removal credits the
// customer for the unused portion of the already-paid period. Seeds a 2-item
// subscription so RemoveItem's "last item" guard doesn't trip.
func TestRemoveItem_ProratesAsCredit(t *testing.T) {
	ctx := context.Background()
	tenantID := "t1"

	store := newMemStore()
	subID, firstItemID := seedSubWithItem(t, store, tenantID, "cus_1", "plan_a")
	svc := NewService(store, nil)
	if _, err := svc.AddItem(ctx, tenantID, subID, AddItemInput{PlanID: "plan_b", Quantity: 1}); err != nil {
		t.Fatalf("seed second item: %v", err)
	}

	plans := &plansMock{plans: map[string]domain.Plan{
		"plan_a": {ID: "plan_a", Name: "Base A", BaseAmountCents: 2000, Currency: "USD"},
		"plan_b": {ID: "plan_b", Name: "Base B", BaseAmountCents: 1000, Currency: "USD"},
	}}
	invoices := &invoicesMock{}
	credits := &creditsMock{}

	h := NewHandler(svc)
	h.SetProrationDeps(plans, invoices, credits)

	req := removeItemURL(context.WithValue(ctx, auth.TestTenantIDKey(), tenantID), subID, firstItemID)
	rr := httptest.NewRecorder()
	h.removeItem(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status: got %d, want 204. body=%s", rr.Code, rr.Body.String())
	}
	if len(invoices.createdInvoices) != 0 {
		t.Errorf("createdInvoices: got %d, want 0 — remove should credit not invoice",
			len(invoices.createdInvoices))
	}
	if len(credits.grantCalls) != 1 {
		t.Fatalf("grantCalls: got %d, want 1 — removed item must be credited", len(credits.grantCalls))
	}
	call := credits.grantCalls[0]
	if call.SourceChangeType != domain.ItemChangeTypeRemove {
		t.Errorf("credit SourceChangeType: got %q, want %q", call.SourceChangeType, domain.ItemChangeTypeRemove)
	}
	if call.SourceSubscriptionItemID != firstItemID {
		t.Errorf("credit item_id: got %q, want %q", call.SourceSubscriptionItemID, firstItemID)
	}
	// plan_a is $20/period, half-period → ~$10 credit.
	if call.AmountCents < 800 || call.AmountCents > 1200 {
		t.Errorf("credit amount: got %d, want ~1000", call.AmountCents)
	}
}

// TestUpdateItem_QuantityNoOpRejected ensures a qty-change request that doesn't
// actually change the qty returns 400 rather than silently emitting a zero
// proration invoice. Mirrors the same guard on plan changes.
func TestUpdateItem_QuantityNoOpRejected(t *testing.T) {
	ctx := context.Background()
	tenantID := "t1"

	store := newMemStore()
	subID, itemID := seedSubWithItem(t, store, tenantID, "cus_1", "plan_seats")
	svc := NewService(store, nil)

	plans := &plansMock{plans: map[string]domain.Plan{
		"plan_seats": {ID: "plan_seats", Name: "Seat", BaseAmountCents: 1000, Currency: "USD"},
	}}
	invoices := &invoicesMock{}
	credits := &creditsMock{}

	h := NewHandler(svc)
	h.SetProrationDeps(plans, invoices, credits)

	sameQty := int64(1) // seeded qty is 1
	body, _ := json.Marshal(UpdateItemInput{Quantity: &sameQty})
	req := updateItemURL(context.WithValue(ctx, auth.TestTenantIDKey(), tenantID), subID, itemID, body)

	rr := httptest.NewRecorder()
	h.updateItem(rr, req)

	if rr.Code != http.StatusBadRequest && rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status: got %d, want 400/422 for no-op qty. body=%s", rr.Code, rr.Body.String())
	}
	if len(invoices.createdInvoices) != 0 || len(credits.grantCalls) != 0 {
		t.Errorf("no-op qty must not trigger proration")
	}
}

func eventTypes(events []capturedEvent) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.eventType)
	}
	return out
}
