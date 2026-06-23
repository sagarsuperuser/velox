package payment

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// flakyUpdater embeds the full mock InvoiceUpdater and fails the first N
// UpdatePayment calls, so the load-bearing-write retry can be exercised.
type flakyUpdater struct {
	*mockInvoiceUpdater
	failFirst int
	calls     int
}

func (f *flakyUpdater) UpdatePayment(ctx context.Context, tenantID, id string, ps domain.InvoicePaymentStatus, piID, errMsg string, paidAt *time.Time) (domain.Invoice, error) {
	f.calls++
	if f.calls <= f.failFirst {
		return domain.Invoice{}, errors.New("transient db blip")
	}
	return f.mockInvoiceUpdater.UpdatePayment(ctx, tenantID, id, ps, piID, errMsg, paidAt)
}

// TestPersistChargeOutcomeWithRetry locks the fix for the swallowed
// PaymentUnknown enrollment write (`_, _ = UpdatePayment`). That write is the
// SOLE thing enrolling a possibly-succeeded PI into the reconciler's
// unknown-payment sweep, so a transient failure must be retried, and a
// permanent failure must surface (so the caller logs loud) rather than be
// silently dropped.
func TestPersistChargeOutcomeWithRetry(t *testing.T) {
	t.Run("recovers after transient failures", func(t *testing.T) {
		base := newMockInvoiceUpdater()
		base.invoices["inv_1"] = domain.Invoice{ID: "inv_1", TenantID: "t1"}
		f := &flakyUpdater{mockInvoiceUpdater: base, failFirst: 2}

		err := persistChargeOutcomeWithRetry(context.Background(), f, "t1", "inv_1", domain.PaymentUnknown, "pi_1", "timeout")
		if err != nil {
			t.Fatalf("expected recovery after retries, got %v", err)
		}
		if f.calls != 3 {
			t.Fatalf("calls = %d, want 3 (2 fail + 1 success)", f.calls)
		}
		if base.invoices["inv_1"].PaymentStatus != domain.PaymentUnknown {
			t.Fatalf("payment_status not persisted on recovery: %q", base.invoices["inv_1"].PaymentStatus)
		}
		if base.invoices["inv_1"].StripePaymentIntentID != "pi_1" {
			t.Fatalf("PaymentIntent id not persisted on recovery")
		}
	})

	t.Run("fails loud after exhausting retries", func(t *testing.T) {
		base := newMockInvoiceUpdater()
		base.invoices["inv_1"] = domain.Invoice{ID: "inv_1", TenantID: "t1"}
		f := &flakyUpdater{mockInvoiceUpdater: base, failFirst: 99}

		err := persistChargeOutcomeWithRetry(context.Background(), f, "t1", "inv_1", domain.PaymentUnknown, "pi_1", "timeout")
		if err == nil {
			t.Fatal("expected an error after exhausting retries so the caller fails loud, got nil")
		}
		if f.calls != 3 {
			t.Fatalf("calls = %d, want 3 attempts", f.calls)
		}
	})
}
