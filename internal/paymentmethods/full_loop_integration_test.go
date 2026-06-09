package paymentmethods_test

import (
	"context"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/paymentmethods"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// TestAttach_DedupesByFingerprint exercises the migration-0099
// dedupe-by-fingerprint behavior. Stripe mints a fresh PaymentMethod
// every time a customer completes Checkout, even for the same card
// number — without dedupe the portal renders Visa-4242 twice. The
// store collapses these on attach using card.fingerprint as the
// dedupe key.
//
// Scenario:
//  1. Attach pm_first (fingerprint=fp_A) → becomes default.
//  2. Re-attach pm_second with SAME fingerprint fp_A → old row is
//     detached, new row inherits is_default=true. List returns 1.
//  3. Attach pm_third with DIFFERENT fingerprint fp_B → 2 rows, the
//     existing one stays default, the new one is non-default.
func TestAttach_DedupesByFingerprint(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx, cancel := context.WithTimeout(postgres.WithLivemode(context.Background(), false), 20*time.Second)
	defer cancel()

	tenantID := testutil.CreateTestTenant(t, db, "dedupe-test")
	custStore := customer.NewPostgresStore(db)
	cust, err := custStore.Create(ctx, tenantID, domain.Customer{
		ExternalID: "cus_dedupe", DisplayName: "Dedupe Co", Email: "x@dedupe.test",
	})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}

	pmStore := paymentmethods.NewPostgresStore(db)
	stripe := &dedupeFakeStripe{}
	svc := paymentmethods.NewService(pmStore, stripe, custStore)
	if err := custStore.SetStripeCustomerID(ctx, tenantID, cust.ID, "cus_stripe_dedupe"); err != nil {
		t.Fatalf("seed stripe customer: %v", err)
	}

	// 1. First attach — pm_first, fingerprint fp_A.
	stripe.next = paymentmethods.CardMetadata{Brand: "visa", Last4: "4242", ExpMonth: 12, ExpYear: 2030, Fingerprint: "fp_A"}
	pm1, err := svc.AttachFromSetupIntent(ctx, tenantID, cust.ID, "pm_first")
	if err != nil {
		t.Fatalf("attach first: %v", err)
	}
	if !pm1.IsDefault {
		t.Fatalf("first PM must be default")
	}

	// 2. Re-attach same card under a NEW Stripe PM id but SAME
	// fingerprint — Stripe-realistic for "customer clicked Add again
	// with the same card". Should dedupe: pm_first detached, pm_second
	// inherits is_default=true. List size stays at 1.
	stripe.next = paymentmethods.CardMetadata{Brand: "visa", Last4: "4242", ExpMonth: 12, ExpYear: 2030, Fingerprint: "fp_A"}
	pm2, err := svc.AttachFromSetupIntent(ctx, tenantID, cust.ID, "pm_second")
	if err != nil {
		t.Fatalf("attach second (dedupe): %v", err)
	}
	if !pm2.IsDefault {
		t.Fatalf("re-attached PM must inherit is_default=true from the detached duplicate; got false")
	}
	pms, err := svc.List(ctx, tenantID, cust.ID)
	if err != nil {
		t.Fatalf("list after dedupe: %v", err)
	}
	if len(pms) != 1 {
		t.Fatalf("dedupe should leave exactly 1 active PM; got %d", len(pms))
	}
	if pms[0].StripePaymentMethodID != "pm_second" {
		t.Fatalf("active PM should be pm_second; got %q", pms[0].StripePaymentMethodID)
	}

	// 3. Attach a genuinely different card — different fingerprint.
	// No dedupe should fire. Existing pm_second stays default;
	// pm_third is the second non-default row.
	stripe.next = paymentmethods.CardMetadata{Brand: "mastercard", Last4: "5555", ExpMonth: 6, ExpYear: 2031, Fingerprint: "fp_B"}
	pm3, err := svc.AttachFromSetupIntent(ctx, tenantID, cust.ID, "pm_third")
	if err != nil {
		t.Fatalf("attach third (different card): %v", err)
	}
	if pm3.IsDefault {
		t.Fatalf("third PM (different card) must NOT steal default; existing default wins")
	}
	pms, err = svc.List(ctx, tenantID, cust.ID)
	if err != nil {
		t.Fatalf("list after third attach: %v", err)
	}
	if len(pms) != 2 {
		t.Fatalf("different-card attach should add a row; got %d active", len(pms))
	}
}

// dedupeFakeStripe lets the test inject different CardMetadata per
// AttachFromSetupIntent call by mutating .next between calls.
type dedupeFakeStripe struct {
	next paymentmethods.CardMetadata
}

func (f *dedupeFakeStripe) EnsureStripeCustomer(_ context.Context, _, _ string) (string, error) {
	return "cus_stripe_dedupe", nil
}
func (f *dedupeFakeStripe) CreateSetupIntent(_ context.Context, _ string, _ map[string]string) (string, string, error) {
	return "seti_secret_dedupe", "seti_dedupe", nil
}
func (f *dedupeFakeStripe) CreateSetupCheckoutSession(_ context.Context, _, _, _ string, _ map[string]string) (string, string, error) {
	return "https://checkout.stripe.com/dedupe", "cs_dedupe", nil
}
func (f *dedupeFakeStripe) DetachPaymentMethod(_ context.Context, _ string) error { return nil }
func (f *dedupeFakeStripe) FetchPaymentMethodCard(_ context.Context, _ string) (paymentmethods.CardMetadata, error) {
	return f.next, nil
}
func (f *dedupeFakeStripe) SetDefaultPaymentMethod(_ context.Context, _, _, _ string) error {
	return nil
}
