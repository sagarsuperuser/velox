package portalapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/customerportal"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/invoice"
	"github.com/sagarsuperuser/velox/internal/subscription"
)

// fakeInvoiceService is a minimal in-memory stand-in. It stores invoices by
// tenant+customer and returns them for List; GetWithLineItems returns by ID
// without tenant scoping (test setup is expected to provision only the test's
// own invoices so we can assert cross-customer behavior at the handler layer).
type fakeInvoiceService struct {
	invoices map[string]domain.Invoice // id → invoice
	items    map[string][]domain.InvoiceLineItem
}

func (f *fakeInvoiceService) List(_ context.Context, filter invoice.ListFilter) ([]domain.Invoice, int, error) {
	var out []domain.Invoice
	for _, inv := range f.invoices {
		if inv.TenantID != filter.TenantID {
			continue
		}
		if filter.CustomerID != "" && inv.CustomerID != filter.CustomerID {
			continue
		}
		out = append(out, inv)
	}
	return out, len(out), nil
}

func (f *fakeInvoiceService) GetWithLineItems(_ context.Context, tenantID, id string) (domain.Invoice, []domain.InvoiceLineItem, error) {
	inv, ok := f.invoices[id]
	if !ok || inv.TenantID != tenantID {
		return domain.Invoice{}, nil, errs.ErrNotFound
	}
	return inv, f.items[id], nil
}

type fakeSubscriptionService struct {
	subs       map[string]domain.Subscription // id → sub
	cancelErr  error
	cancelFunc func(string) domain.Subscription // allows custom cancel response
}

func (f *fakeSubscriptionService) List(_ context.Context, filter subscription.ListFilter) ([]domain.Subscription, int, error) {
	var out []domain.Subscription
	for _, sub := range f.subs {
		if sub.TenantID != filter.TenantID {
			continue
		}
		if filter.CustomerID != "" && sub.CustomerID != filter.CustomerID {
			continue
		}
		out = append(out, sub)
	}
	return out, len(out), nil
}

func (f *fakeSubscriptionService) Get(_ context.Context, tenantID, id string) (domain.Subscription, error) {
	sub, ok := f.subs[id]
	if !ok || sub.TenantID != tenantID {
		return domain.Subscription{}, errs.ErrNotFound
	}
	return sub, nil
}

func (f *fakeSubscriptionService) Cancel(_ context.Context, tenantID, id string) (domain.Subscription, error) {
	if f.cancelErr != nil {
		return domain.Subscription{}, f.cancelErr
	}
	if f.cancelFunc != nil {
		return f.cancelFunc(id), nil
	}
	sub := f.subs[id]
	sub.Status = domain.SubscriptionCanceled
	now := time.Now().UTC()
	sub.CanceledAt = &now
	return sub, nil
}

type fakeCustomerGetter struct {
	customers map[string]domain.Customer // id → customer
}

func (f *fakeCustomerGetter) Get(_ context.Context, tenantID, id string) (domain.Customer, error) {
	c, ok := f.customers[id]
	if !ok || c.TenantID != tenantID {
		return domain.Customer{}, errs.ErrNotFound
	}
	return c, nil
}

func (f *fakeCustomerGetter) GetBillingProfile(_ context.Context, _ string, _ string) (domain.CustomerBillingProfile, error) {
	return domain.CustomerBillingProfile{}, errs.ErrNotFound
}

type fakeSettingsGetter struct {
	settings domain.TenantSettings
	notFound bool
}

func (f *fakeSettingsGetter) Get(_ context.Context, tenantID string) (domain.TenantSettings, error) {
	if f.notFound {
		return domain.TenantSettings{}, errs.ErrNotFound
	}
	s := f.settings
	s.TenantID = tenantID
	return s, nil
}

type fakeCreditNoteLister struct{}

func (fakeCreditNoteLister) List(_ context.Context, _ string, _ string) ([]domain.CreditNote, error) {
	return nil, nil
}

type fakeEventDispatcher struct {
	dispatched []dispatchedEvent
}

type dispatchedEvent struct {
	TenantID  string
	EventType string
	Payload   map[string]any
}

func (f *fakeEventDispatcher) Dispatch(_ context.Context, tenantID, eventType string, payload map[string]any) error {
	f.dispatched = append(f.dispatched, dispatchedEvent{
		TenantID: tenantID, EventType: eventType, Payload: payload,
	})
	return nil
}

// reqWithIdentity fabricates a request with the portal-session context keys
// that Middleware normally injects. Used so handler tests can exercise the
// identity-scoping logic without wiring the full middleware.
func reqWithIdentity(method, path, tenantID, customerID string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	ctx := customerportal.WithTestIdentity(r.Context(), tenantID, customerID)
	return r.WithContext(ctx)
}

// --- tests ---

func TestListInvoicesScopedToCustomer(t *testing.T) {
	now := time.Now().UTC()
	invs := &fakeInvoiceService{
		invoices: map[string]domain.Invoice{
			"inv_a": {ID: "inv_a", TenantID: "t1", CustomerID: "cust_a", InvoiceNumber: "INV-1", Status: domain.InvoiceFinalized, Currency: "USD", IssuedAt: &now},
			"inv_b": {ID: "inv_b", TenantID: "t1", CustomerID: "cust_b", InvoiceNumber: "INV-2", Status: domain.InvoiceFinalized, Currency: "USD", IssuedAt: &now},
			"inv_d": {ID: "inv_d", TenantID: "t1", CustomerID: "cust_a", InvoiceNumber: "INV-3", Status: domain.InvoiceDraft, Currency: "USD"},
		},
	}
	h := New(Deps{Invoices: invs})

	r := reqWithIdentity("GET", "/invoices", "t1", "cust_a")
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("want 1 invoice (cust_a finalized only), got %d: %+v", len(resp.Data), resp.Data)
	}
	if resp.Data[0].ID != "inv_a" {
		t.Errorf("want inv_a, got %s", resp.Data[0].ID)
	}
	if resp.Data[0].Status == string(domain.InvoiceDraft) {
		t.Errorf("draft leaked into portal response")
	}
}

func TestDownloadInvoicePDFDeniesCrossCustomer(t *testing.T) {
	invs := &fakeInvoiceService{
		invoices: map[string]domain.Invoice{
			"inv_b": {ID: "inv_b", TenantID: "t1", CustomerID: "cust_b", InvoiceNumber: "INV-2", Status: domain.InvoiceFinalized, Currency: "USD"},
		},
	}
	h := New(Deps{Invoices: invs, Customers: &fakeCustomerGetter{customers: map[string]domain.Customer{}}})

	r := reqWithIdentity("GET", "/invoices/inv_b/pdf", "t1", "cust_a")
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404 (cross-customer), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestDownloadInvoicePDFDeniesDraft(t *testing.T) {
	invs := &fakeInvoiceService{
		invoices: map[string]domain.Invoice{
			"inv_d": {ID: "inv_d", TenantID: "t1", CustomerID: "cust_a", Status: domain.InvoiceDraft, Currency: "USD"},
		},
	}
	h := New(Deps{Invoices: invs, Customers: &fakeCustomerGetter{customers: map[string]domain.Customer{}}})

	r := reqWithIdentity("GET", "/invoices/inv_d/pdf", "t1", "cust_a")
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404 (draft), got %d", w.Code)
	}
}

func TestListSubscriptionsScopedToCustomer(t *testing.T) {
	subs := &fakeSubscriptionService{
		subs: map[string]domain.Subscription{
			"sub_a": {ID: "sub_a", TenantID: "t1", CustomerID: "cust_a", Status: domain.SubscriptionActive, DisplayName: "Pro"},
			"sub_b": {ID: "sub_b", TenantID: "t1", CustomerID: "cust_b", Status: domain.SubscriptionActive, DisplayName: "Enterprise"},
		},
	}
	h := New(Deps{Subscriptions: subs})

	r := reqWithIdentity("GET", "/subscriptions", "t1", "cust_a")
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "sub_a" {
		t.Fatalf("want only sub_a, got %+v", resp.Data)
	}
}

func TestCancelSubscriptionHappyPath(t *testing.T) {
	subs := &fakeSubscriptionService{
		subs: map[string]domain.Subscription{
			"sub_a": {ID: "sub_a", TenantID: "t1", CustomerID: "cust_a", Status: domain.SubscriptionActive},
		},
	}
	ev := &fakeEventDispatcher{}
	h := New(Deps{Subscriptions: subs, Events: ev})

	r := reqWithIdentity("POST", "/subscriptions/sub_a/cancel", "t1", "cust_a")
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if len(ev.dispatched) != 1 {
		t.Fatalf("want 1 dispatched event, got %d", len(ev.dispatched))
	}
	if ev.dispatched[0].EventType != domain.EventSubscriptionCanceled {
		t.Errorf("want subscription.canceled, got %s", ev.dispatched[0].EventType)
	}
	if got := ev.dispatched[0].Payload["canceled_by"]; got != "customer" {
		t.Errorf("want canceled_by=customer, got %v", got)
	}
}

func TestCancelSubscriptionDeniesCrossCustomer(t *testing.T) {
	subs := &fakeSubscriptionService{
		subs: map[string]domain.Subscription{
			"sub_b": {ID: "sub_b", TenantID: "t1", CustomerID: "cust_b", Status: domain.SubscriptionActive},
		},
	}
	ev := &fakeEventDispatcher{}
	h := New(Deps{Subscriptions: subs, Events: ev})

	r := reqWithIdentity("POST", "/subscriptions/sub_b/cancel", "t1", "cust_a")
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404 (cross-customer), got %d body=%s", w.Code, w.Body.String())
	}
	if len(ev.dispatched) != 0 {
		t.Fatalf("expected zero events on cross-customer cancel, got %d", len(ev.dispatched))
	}
}

func TestBrandingReturnsSafeFieldsOnly(t *testing.T) {
	settings := &fakeSettingsGetter{
		settings: domain.TenantSettings{
			CompanyName:   "Acme Corp",
			LogoURL:       "https://cdn.example/logo.png",
			SupportURL:    "https://support.example",
			TaxID:         "VAT-999999", // must NOT leak
			InvoicePrefix: "ACME",       // must NOT leak
		},
	}
	h := New(Deps{Settings: settings})

	r := reqWithIdentity("GET", "/branding", "t1", "cust_a")
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["company_name"] != "Acme Corp" {
		t.Errorf("want company_name=Acme Corp, got %v", body["company_name"])
	}
	if body["logo_url"] != "https://cdn.example/logo.png" {
		t.Errorf("want logo_url, got %v", body["logo_url"])
	}
	if _, leaked := body["tax_id"]; leaked {
		t.Errorf("tax_id leaked to portal response: %v", body)
	}
	if _, leaked := body["invoice_prefix"]; leaked {
		t.Errorf("invoice_prefix leaked to portal response: %v", body)
	}
}

func TestBrandingReturnsEmptyWhenNoSettings(t *testing.T) {
	h := New(Deps{Settings: &fakeSettingsGetter{notFound: true}})

	r := reqWithIdentity("GET", "/branding", "t1", "cust_a")
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestMissingIdentityReturns401(t *testing.T) {
	h := New(Deps{})
	r := httptest.NewRequest("GET", "/invoices", nil) // no identity ctx
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}
