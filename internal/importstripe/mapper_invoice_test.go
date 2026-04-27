package importstripe

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stripe/stripe-go/v82"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// loadInvoiceFixture decodes a Stripe invoice fixture into *stripe.Invoice.
// Mirrors the customer/price/subscription helpers.
func loadInvoiceFixture(t *testing.T, name string) *stripe.Invoice {
	t.Helper()
	path := filepath.Join("testdata", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	var inv stripe.Invoice
	if err := json.Unmarshal(data, &inv); err != nil {
		t.Fatalf("decode fixture %s: %v", path, err)
	}
	return &inv
}

func TestMapInvoice_Paid(t *testing.T) {
	inv := loadInvoiceFixture(t, "invoice_paid.json")
	got, err := mapInvoice(inv)
	if err != nil {
		t.Fatalf("mapInvoice: %v", err)
	}
	if got.Invoice.Status != domain.InvoicePaid {
		t.Errorf("Status = %q, want paid", got.Invoice.Status)
	}
	if got.Invoice.PaymentStatus != domain.PaymentSucceeded {
		t.Errorf("PaymentStatus = %q, want succeeded", got.Invoice.PaymentStatus)
	}
	if got.Invoice.StripeInvoiceID != inv.ID {
		t.Errorf("StripeInvoiceID = %q, want %q", got.Invoice.StripeInvoiceID, inv.ID)
	}
	if got.Invoice.Currency != "USD" {
		t.Errorf("Currency = %q, want USD", got.Invoice.Currency)
	}
	if got.Invoice.TotalAmountCents != inv.Total {
		t.Errorf("TotalAmountCents = %d, want %d", got.Invoice.TotalAmountCents, inv.Total)
	}
	if got.Invoice.SubtotalCents != inv.Subtotal {
		t.Errorf("SubtotalCents = %d, want %d", got.Invoice.SubtotalCents, inv.Subtotal)
	}
	if got.Invoice.AmountPaidCents != inv.AmountPaid {
		t.Errorf("AmountPaidCents = %d, want %d", got.Invoice.AmountPaidCents, inv.AmountPaid)
	}
	if got.Invoice.PaidAt == nil {
		t.Fatal("PaidAt = nil, want set for paid invoice")
	}
	if got.Invoice.IssuedAt == nil {
		t.Fatal("IssuedAt = nil, want set for finalized invoice")
	}
	if got.Invoice.BillingReason != domain.BillingReasonSubscriptionCycle {
		t.Errorf("BillingReason = %q, want subscription_cycle", got.Invoice.BillingReason)
	}
	if got.CustomerExternalID != "cus_paid_001" {
		t.Errorf("CustomerExternalID = %q, want cus_paid_001", got.CustomerExternalID)
	}
	if got.SubscriptionExternalID != "sub_paid_001" {
		t.Errorf("SubscriptionExternalID = %q, want sub_paid_001", got.SubscriptionExternalID)
	}
	if len(got.LineItems) != 1 {
		t.Fatalf("LineItems = %d, want 1", len(got.LineItems))
	}
	if got.LineItems[0].AmountCents != 4999 {
		t.Errorf("LineItems[0].AmountCents = %d, want 4999", got.LineItems[0].AmountCents)
	}
	if got.LineItems[0].Description == "" {
		t.Error("LineItems[0].Description is empty; want non-empty")
	}
}

func TestMapInvoice_Void(t *testing.T) {
	inv := loadInvoiceFixture(t, "invoice_void.json")
	got, err := mapInvoice(inv)
	if err != nil {
		t.Fatalf("mapInvoice: %v", err)
	}
	if got.Invoice.Status != domain.InvoiceVoided {
		t.Errorf("Status = %q, want voided", got.Invoice.Status)
	}
	if got.Invoice.PaymentStatus != domain.PaymentFailed {
		t.Errorf("PaymentStatus = %q, want failed", got.Invoice.PaymentStatus)
	}
	if got.Invoice.VoidedAt == nil {
		t.Fatal("VoidedAt = nil, want set for voided invoice")
	}
}

func TestMapInvoice_UncollectibleRemapsToVoided(t *testing.T) {
	inv := loadInvoiceFixture(t, "invoice_uncollectible.json")
	got, err := mapInvoice(inv)
	if err != nil {
		t.Fatalf("mapInvoice: %v", err)
	}
	if got.Invoice.Status != domain.InvoiceVoided {
		t.Errorf("Status = %q, want voided (uncollectible remap)", got.Invoice.Status)
	}
	if !containsAny(got.Notes, "uncollectible") {
		t.Errorf("Notes missing uncollectible remap; got %v", got.Notes)
	}
	if got.Invoice.VoidedAt == nil {
		t.Error("VoidedAt = nil, want set (carrying marked_uncollectible_at when voided_at is unset)")
	}
}

func TestMapInvoice_OpenIsRejected(t *testing.T) {
	inv := loadInvoiceFixture(t, "invoice_open_rejected.json")
	_, err := mapInvoice(inv)
	if !errors.Is(err, ErrInvoiceUnsupportedStatus) {
		t.Errorf("err = %v, want ErrInvoiceUnsupportedStatus", err)
	}
}

func TestMapInvoice_DraftIsRejected(t *testing.T) {
	inv := &stripe.Invoice{
		ID:       "in_draft_x",
		Status:   stripe.InvoiceStatusDraft,
		Customer: &stripe.Customer{ID: "cus_x"},
		Currency: "usd",
	}
	_, err := mapInvoice(inv)
	if !errors.Is(err, ErrInvoiceUnsupportedStatus) {
		t.Errorf("err = %v, want ErrInvoiceUnsupportedStatus", err)
	}
}

func TestMapInvoice_MultiLine(t *testing.T) {
	inv := loadInvoiceFixture(t, "invoice_multi_line.json")
	got, err := mapInvoice(inv)
	if err != nil {
		t.Fatalf("mapInvoice: %v", err)
	}
	if len(got.LineItems) != 3 {
		t.Fatalf("LineItems = %d, want 3", len(got.LineItems))
	}
	// Verify the per-line amounts sum to the invoice subtotal.
	var sum int64
	for _, li := range got.LineItems {
		sum += li.AmountCents
	}
	if sum != got.Invoice.SubtotalCents {
		t.Errorf("sum of line amounts (%d) != subtotal (%d)", sum, got.Invoice.SubtotalCents)
	}
}

func TestMapInvoice_FullWithTax(t *testing.T) {
	inv := loadInvoiceFixture(t, "invoice_full.json")
	got, err := mapInvoice(inv)
	if err != nil {
		t.Fatalf("mapInvoice: %v", err)
	}
	// Tax: 500 cents (one tax-rate entry of 500).
	if got.Invoice.TaxAmountCents != 500 {
		t.Errorf("TaxAmountCents = %d, want 500", got.Invoice.TaxAmountCents)
	}
	if got.Invoice.SubtotalCents != 4999 {
		t.Errorf("SubtotalCents = %d, want 4999", got.Invoice.SubtotalCents)
	}
	if got.Invoice.TotalAmountCents != 5499 {
		t.Errorf("TotalAmountCents = %d, want 5499", got.Invoice.TotalAmountCents)
	}
	if got.Invoice.BillingReason != domain.BillingReasonSubscriptionCreate {
		t.Errorf("BillingReason = %q, want subscription_create", got.Invoice.BillingReason)
	}
	if got.Invoice.Footer != "Pay within 30 days" {
		t.Errorf("Footer = %q, want %q", got.Invoice.Footer, "Pay within 30 days")
	}
	if got.Invoice.Memo == "" {
		t.Error("Memo is empty; want set from description")
	}
}

func TestMapInvoice_LossyExtrasNoted(t *testing.T) {
	inv := loadInvoiceFixture(t, "invoice_with_extras.json")
	got, err := mapInvoice(inv)
	if err != nil {
		t.Fatalf("mapInvoice: %v", err)
	}
	wantSubstrings := []string{"discount", "shipping"}
	for _, want := range wantSubstrings {
		if !containsAny(got.Notes, want) {
			t.Errorf("Notes missing %q; got %v", want, got.Notes)
		}
	}
}

func TestMapInvoice_NoSubscription(t *testing.T) {
	inv := loadInvoiceFixture(t, "invoice_no_subscription.json")
	got, err := mapInvoice(inv)
	if err != nil {
		t.Fatalf("mapInvoice: %v", err)
	}
	if got.SubscriptionExternalID != "" {
		t.Errorf("SubscriptionExternalID = %q, want empty (manual invoice)", got.SubscriptionExternalID)
	}
	if got.Invoice.BillingReason != domain.BillingReasonManual {
		t.Errorf("BillingReason = %q, want manual", got.Invoice.BillingReason)
	}
}

func TestMapInvoice_UnknownBillingReasonRemaps(t *testing.T) {
	inv := loadInvoiceFixture(t, "invoice_unknown_billing_reason.json")
	got, err := mapInvoice(inv)
	if err != nil {
		t.Fatalf("mapInvoice: %v", err)
	}
	// subscription_update remaps to manual + a note explaining why.
	if got.Invoice.BillingReason != domain.BillingReasonManual {
		t.Errorf("BillingReason = %q, want manual", got.Invoice.BillingReason)
	}
	if !containsAny(got.Notes, "subscription_update") {
		t.Errorf("Notes missing subscription_update remap; got %v", got.Notes)
	}
}

func TestMapInvoice_EmptyIDIsError(t *testing.T) {
	_, err := mapInvoice(&stripe.Invoice{ID: "", Status: stripe.InvoiceStatusPaid})
	if !errors.Is(err, ErrMapEmptyInvoiceID) {
		t.Errorf("err = %v, want ErrMapEmptyInvoiceID", err)
	}
}

func TestMapInvoice_NilIsError(t *testing.T) {
	_, err := mapInvoice(nil)
	if err == nil {
		t.Fatal("expected error for nil invoice")
	}
}

func TestMapInvoice_MissingCustomerIsError(t *testing.T) {
	inv := &stripe.Invoice{
		ID:     "in_no_cust",
		Status: stripe.InvoiceStatusPaid,
	}
	_, err := mapInvoice(inv)
	if !errors.Is(err, ErrInvoiceMissingCustomer) {
		t.Errorf("err = %v, want ErrInvoiceMissingCustomer", err)
	}
}
