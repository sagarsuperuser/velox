package invoice

import (
	"context"
	"strings"
	"testing"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// spyCustomerReader returns a fixed customer and counts lookups, so a test can
// assert both the email-presence branch AND the lazy-fetch guard (the customer
// is looked up only for the invoices that actually reach the no_payment_method
// branch, not on every read).
type spyCustomerReader struct {
	cust  domain.Customer
	calls int
}

func (s *spyCustomerReader) Get(_ context.Context, _, _ string) (domain.Customer, error) {
	s.calls++
	return s.cust, nil
}

func finalizedUnpaidNoPM() domain.Invoice {
	return domain.Invoice{
		ID:            "inv_1",
		TenantID:      "t1",
		CustomerID:    "cus_1",
		Status:        domain.InvoiceFinalized,
		PaymentStatus: domain.PaymentPending,
	}
}

// TestAttachAttention_NoPaymentMethod_EmailVariant pins the real service
// wiring end to end: attachAttention reads the customer's email presence and
// the classifier renders the honest no_payment_method banner variant — because
// the engine's finalize-time setup-link email skips silently when there's no
// address, so a hardcoded "we emailed a link" lied on exactly those invoices.
// It also pins the lazy-fetch guard: the customer is looked up ONLY for the
// finalized-unpaid-no-PM invoices that reach the branch, so list reads don't
// pay a customer lookup per row.
func TestAttachAttention_NoPaymentMethod_EmailVariant(t *testing.T) {
	ctx := context.Background()

	t.Run("customer has email -> emailed-a-link variant, one lookup", func(t *testing.T) {
		spy := &spyCustomerReader{cust: domain.Customer{Email: "ops@acme.test"}}
		svc := NewService(newMemStore(), nil, newMemNumberer())
		svc.SetCustomerReader(spy)

		inv := svc.attachAttention(ctx, finalizedUnpaidNoPM())
		if inv.Attention == nil || inv.Attention.Reason != domain.AttentionReasonNoPaymentMethod {
			t.Fatalf("expected no_payment_method attention, got %+v", inv.Attention)
		}
		if !strings.Contains(inv.Attention.Message, "has been emailed a setup link") {
			t.Errorf("has-email variant must claim the link was emailed, got %q", inv.Attention.Message)
		}
		var hasResend bool
		for _, a := range inv.Attention.Actions {
			if a.Code == domain.AttentionActionSendReminder {
				hasResend = true
			}
		}
		if !hasResend {
			t.Errorf("has-email variant must offer a resend, got %+v", inv.Attention.Actions)
		}
		if spy.calls != 1 {
			t.Errorf("expected exactly one customer lookup, got %d", spy.calls)
		}
	})

	t.Run("customer has no email -> honest no-address variant, no resend", func(t *testing.T) {
		spy := &spyCustomerReader{cust: domain.Customer{Email: ""}}
		svc := NewService(newMemStore(), nil, newMemNumberer())
		svc.SetCustomerReader(spy)

		inv := svc.attachAttention(ctx, finalizedUnpaidNoPM())
		if inv.Attention == nil {
			t.Fatalf("expected attention")
		}
		if strings.Contains(inv.Attention.Message, "has been emailed") {
			t.Errorf("no-email variant must NOT claim a send happened, got %q", inv.Attention.Message)
		}
		if !strings.Contains(inv.Attention.Message, "no email address") {
			t.Errorf("no-email variant must name the missing-email cause, got %q", inv.Attention.Message)
		}
		for _, a := range inv.Attention.Actions {
			if a.Code == domain.AttentionActionSendReminder {
				t.Errorf("no-email variant must NOT offer a resend that can't send, got %+v", inv.Attention.Actions)
			}
		}
	})

	t.Run("paid invoice -> no customer lookup (lazy guard)", func(t *testing.T) {
		spy := &spyCustomerReader{cust: domain.Customer{Email: "ops@acme.test"}}
		svc := NewService(newMemStore(), nil, newMemNumberer())
		svc.SetCustomerReader(spy)

		inv := finalizedUnpaidNoPM()
		inv.Status = domain.InvoicePaid // terminal: never reaches no_payment_method
		_ = svc.attachAttention(ctx, inv)
		if spy.calls != 0 {
			t.Errorf("a paid invoice must not trigger a customer lookup, got %d", spy.calls)
		}
	})
}
