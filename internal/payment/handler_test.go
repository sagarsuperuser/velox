package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

func TestVerifyStripeSignature_Valid(t *testing.T) {
	secret := "whsec_test_secret_123"
	payload := []byte(`{"id":"evt_123","type":"payment_intent.succeeded"}`)
	ts := fmt.Sprintf("%d", time.Now().Unix())

	// Compute valid signature
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + string(payload)))
	sig := hex.EncodeToString(mac.Sum(nil))

	header := fmt.Sprintf("t=%s,v1=%s", ts, sig)

	err := verifyStripeSignature(payload, header, secret)
	if err != nil {
		t.Fatalf("valid signature should pass: %v", err)
	}
}

func TestVerifyStripeSignature_Invalid(t *testing.T) {
	secret := "whsec_test"
	payload := []byte(`{"id":"evt_123"}`)

	tests := []struct {
		name   string
		header string
	}{
		{"empty header", ""},
		{"missing v1", "t=12345"},
		{"wrong signature", fmt.Sprintf("t=%d,v1=deadbeef", time.Now().Unix())},
		{"expired timestamp", fmt.Sprintf("t=%d,v1=deadbeef", time.Now().Unix()-600)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := verifyStripeSignature(payload, tt.header, secret)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestWebhookHandler_SuccessfulPayment(t *testing.T) {
	invoices := newMockInvoiceUpdaterH()
	invoices.invoices["inv_1"] = mockInvoice{
		id: "inv_1", tenantID: "t1", status: "finalized",
		paymentStatus: "processing", stripePI: "pi_test_abc",
	}
	invoices.byPI["pi_test_abc"] = "inv_1"

	webhooks := newMockWebhookStoreHandler()

	stripeAdapter := NewStripe(nil, invoices, webhooks, nil)
	handler := NewHandler(stripeAdapter, "") // No signature verification in test

	event := map[string]any{
		"id":      "evt_success_1",
		"type":    "payment_intent.succeeded",
		"created": time.Now().Unix(),
		"data": map[string]any{
			"object": map[string]any{
				"id":       "pi_test_abc",
				"object":   "payment_intent",
				"status":   "succeeded",
				"amount":   19900,
				"currency": "usd",
				"metadata": map[string]string{
					"velox_tenant_id":  "t1",
					"velox_invoice_id": "inv_1",
				},
			},
		},
	}
	body, _ := json.Marshal(event)

	req := httptest.NewRequest("POST", "/stripe", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200. body: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["status"] != "processed" {
		t.Errorf("response status: got %q, want processed", resp["status"])
	}

	// Verify invoice was updated
	inv := invoices.invoices["inv_1"]
	if inv.paymentStatus != "succeeded" {
		t.Errorf("payment_status: got %q, want succeeded", inv.paymentStatus)
	}
}

func TestWebhookHandler_NoVeloxMetadata(t *testing.T) {
	stripeAdapter := NewStripe(nil, newMockInvoiceUpdaterH(), newMockWebhookStoreHandler(), nil)
	handler := NewHandler(stripeAdapter, "")

	event := map[string]any{
		"id":      "evt_foreign",
		"type":    "payment_intent.succeeded",
		"created": time.Now().Unix(),
		"data": map[string]any{
			"object": map[string]any{
				"id":       "pi_not_ours",
				"object":   "payment_intent",
				"metadata": map[string]string{}, // No velox metadata
			},
		},
	}
	body, _ := json.Marshal(event)

	req := httptest.NewRequest("POST", "/stripe", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	handler.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("should return 200 for non-Velox events, got %d", rec.Code)
	}

	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["status"] != "skipped" {
		t.Errorf("should skip non-Velox events, got %q", resp["status"])
	}
}

func TestWebhookHandler_SignatureRequired(t *testing.T) {
	stripeAdapter := NewStripe(nil, newMockInvoiceUpdaterH(), newMockWebhookStoreHandler(), nil)
	handler := NewHandler(stripeAdapter, "whsec_real_secret")

	req := httptest.NewRequest("POST", "/stripe", strings.NewReader(`{"id":"evt_1"}`))
	// No Stripe-Signature header
	rec := httptest.NewRecorder()
	handler.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("should reject unsigned webhook, got %d", rec.Code)
	}
}

// --- Mock helpers for handler tests ---

type mockInvoice struct {
	id, tenantID, status, paymentStatus, stripePI, lastError string
	paidAt                                                    *time.Time
}

type mockInvoiceUpdaterHandler struct {
	invoices map[string]mockInvoice
	byPI     map[string]string
}

func newMockInvoiceUpdaterH() *mockInvoiceUpdaterHandler {
	return &mockInvoiceUpdaterHandler{
		invoices: make(map[string]mockInvoice),
		byPI:     make(map[string]string),
	}
}

func (m *mockInvoiceUpdaterHandler) UpdatePayment(_ context.Context, tenantID, id string, ps domain.InvoicePaymentStatus, piID, errMsg string, paidAt *time.Time) (domain.Invoice, error) {
	inv, ok := m.invoices[id]
	if !ok {
		return domain.Invoice{}, fmt.Errorf("not found")
	}
	inv.paymentStatus = string(ps)
	inv.stripePI = piID
	inv.lastError = errMsg
	inv.paidAt = paidAt
	m.invoices[id] = inv
	if piID != "" {
		m.byPI[piID] = id
	}
	return domain.Invoice{ID: id, TenantID: tenantID, PaymentStatus: ps}, nil
}

func (m *mockInvoiceUpdaterHandler) UpdateStatus(_ context.Context, _, id string, status domain.InvoiceStatus) (domain.Invoice, error) {
	inv, ok := m.invoices[id]
	if !ok {
		return domain.Invoice{}, fmt.Errorf("not found")
	}
	inv.status = string(status)
	m.invoices[id] = inv
	return domain.Invoice{ID: id, Status: status}, nil
}

func (m *mockInvoiceUpdaterHandler) GetByStripePaymentIntentID(_ context.Context, _, piID string) (domain.Invoice, error) {
	id, ok := m.byPI[piID]
	if !ok {
		return domain.Invoice{}, fmt.Errorf("not found")
	}
	inv := m.invoices[id]
	return domain.Invoice{ID: id, TenantID: inv.tenantID}, nil
}

func (m *mockInvoiceUpdaterHandler) Get(_ context.Context, _, id string) (domain.Invoice, error) {
	inv, ok := m.invoices[id]
	if !ok {
		return domain.Invoice{}, fmt.Errorf("not found")
	}
	return domain.Invoice{ID: id, TenantID: inv.tenantID}, nil
}

func (m *mockInvoiceUpdaterHandler) MarkPaid(_ context.Context, _, id string, piID string, paidAt time.Time) (domain.Invoice, error) {
	inv, ok := m.invoices[id]
	if !ok {
		return domain.Invoice{}, fmt.Errorf("not found")
	}
	inv.paymentStatus = "succeeded"
	inv.stripePI = piID
	inv.paidAt = &paidAt
	m.invoices[id] = inv
	if piID != "" {
		m.byPI[piID] = id
	}
	return domain.Invoice{ID: id, TenantID: inv.tenantID, Status: domain.InvoicePaid}, nil
}

func (m *mockInvoiceUpdaterHandler) ApplyCreditNote(_ context.Context, _, id string, _ int64) (domain.Invoice, error) {
	inv, ok := m.invoices[id]
	if !ok {
		return domain.Invoice{}, fmt.Errorf("not found")
	}
	return domain.Invoice{ID: id, TenantID: inv.tenantID}, nil
}

type mockWebhookStoreH struct {
	seen map[string]bool
}

func newMockWebhookStoreHandler() *mockWebhookStoreH {
	return &mockWebhookStoreH{seen: make(map[string]bool)}
}

func (m *mockWebhookStoreH) IngestEvent(_ context.Context, _ string, event domain.StripeWebhookEvent) (domain.StripeWebhookEvent, bool, error) {
	if m.seen[event.StripeEventID] {
		return event, false, nil
	}
	m.seen[event.StripeEventID] = true
	return event, true, nil
}
