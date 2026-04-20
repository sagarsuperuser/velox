package paymentmethods_test

import (
	"context"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/customerportal"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/paymentmethods"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// TestFullLoop_PortalCustomer_E2E walks the complete FEAT-3 customer
// lifecycle against a real Postgres — the one code path we can't cover with
// the Service-level unit tests because those mock the store and miss
// livemode triggers, partial unique indexes, and the RLS round-trip.
//
// Steps (mirrors the end-user journey):
//  1. Operator creates a tenant and customer.
//  2. Operator mints a portal session — this is the token handed to the
//     customer as a URL ?token=vlx_cps_... query arg.
//  3. Portal Service.Validate resolves the raw token back to the same
//     (tenant, customer) — proves the session is retrievable without any
//     tenant context (TxBypass path).
//  4. Customer lists payment methods → empty.
//  5. Customer starts "add card" — fake Stripe returns a Checkout URL.
//  6. Webhook fires setup_intent.succeeded → AttachFromSetupIntent
//     persists the payment method and promotes it to default (first PM
//     rule). Summary row is populated.
//  7. Customer lists → 1 PM, marked default.
//  8. A second setup completes → 2 PMs; the first stays default because
//     the customer never explicitly switched.
//  9. Customer promotes the second PM → the partial unique index admits
//     the swap atomically; summary row reflects the new card.
//  10. Customer detaches the current default → the replacement (older PM)
//     is auto-promoted to default; summary row reflects that.
//  11. Customer detaches the remaining PM → list empty; summary row
//     returns to "missing" state.
//
// If any assertion here fails, a regression has been introduced either in
// the Postgres store (livemode trigger, unique index, CASCADE), the Service
// coordination between PMs and the summary, or the portal session token.
func TestFullLoop_PortalCustomer_E2E(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	tenantID := testutil.CreateTestTenant(t, db, "FEAT-3 E2E")

	custStore := customer.NewPostgresStore(db)
	cust, err := custStore.Create(ctx, tenantID, domain.Customer{
		ExternalID:  "cus_feat3_e2e",
		DisplayName: "Full-loop Co",
		Email:       "ops@full-loop.test",
	})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}

	// -------------------------------------------------------------------
	// 1-3. Portal session mint + token validate round-trip.
	// -------------------------------------------------------------------
	portalSvc := customerportal.NewService(customerportal.NewPostgresStore(db))
	minted, err := portalSvc.Create(ctx, tenantID, cust.ID, time.Hour)
	if err != nil {
		t.Fatalf("mint portal session: %v", err)
	}
	if minted.RawToken == "" {
		t.Fatalf("expected raw token from Create")
	}
	sess, err := portalSvc.Validate(ctx, minted.RawToken)
	if err != nil {
		t.Fatalf("validate portal token: %v", err)
	}
	if sess.TenantID != tenantID || sess.CustomerID != cust.ID {
		t.Fatalf("session identity mismatch: got (%s, %s) want (%s, %s)",
			sess.TenantID, sess.CustomerID, tenantID, cust.ID)
	}

	// -------------------------------------------------------------------
	// 4-11. Payment-method lifecycle.
	// -------------------------------------------------------------------
	pmStore := paymentmethods.NewPostgresStore(db)
	stripe := &loopFakeStripe{customerID: "cus_stripe_loop"}
	svc := paymentmethods.NewService(pmStore, stripe, custStore)

	// Seed the summary row with a Stripe customer ID so EnsureStripeCustomer
	// can short-circuit (the real adapter would have lazily created one).
	if _, err := custStore.UpsertPaymentSetup(ctx, tenantID, domain.CustomerPaymentSetup{
		CustomerID:       cust.ID,
		TenantID:         tenantID,
		SetupStatus:      domain.PaymentSetupPending,
		StripeCustomerID: "cus_stripe_loop",
	}); err != nil {
		t.Fatalf("seed payment setup: %v", err)
	}

	// 4. List empty.
	pms, err := svc.List(ctx, tenantID, cust.ID)
	if err != nil {
		t.Fatalf("list (empty): %v", err)
	}
	if len(pms) != 0 {
		t.Fatalf("expected 0 PMs initially, got %d", len(pms))
	}

	// 5. Customer starts add-card flow.
	url, sessID, err := svc.CreateSetupSession(ctx, tenantID, cust.ID, "https://app.example.com/portal")
	if err != nil {
		t.Fatalf("create setup session: %v", err)
	}
	if url == "" || sessID == "" {
		t.Fatalf("expected non-empty checkout url + session id, got (%q, %q)", url, sessID)
	}
	if stripe.setupSessionCalls != 1 {
		t.Fatalf("expected 1 setup session call, got %d", stripe.setupSessionCalls)
	}

	// 6. Webhook completes → first PM attached. Should auto-default.
	pm1, err := svc.AttachFromSetupIntent(ctx, tenantID, cust.ID, "pm_loop_first")
	if err != nil {
		t.Fatalf("attach first: %v", err)
	}
	if !pm1.IsDefault {
		t.Fatalf("first PM must be default, got is_default=false")
	}

	// Summary row should now reflect the default card.
	summary, err := custStore.GetPaymentSetup(ctx, tenantID, cust.ID)
	if err != nil {
		t.Fatalf("get summary after first attach: %v", err)
	}
	if summary.StripePaymentMethodID != "pm_loop_first" {
		t.Fatalf("summary pm mismatch: got %q want pm_loop_first", summary.StripePaymentMethodID)
	}
	if summary.SetupStatus != domain.PaymentSetupReady {
		t.Fatalf("summary status: got %q want ready", summary.SetupStatus)
	}

	// 7. List returns exactly one PM.
	pms, err = svc.List(ctx, tenantID, cust.ID)
	if err != nil {
		t.Fatalf("list after first attach: %v", err)
	}
	if len(pms) != 1 {
		t.Fatalf("expected 1 PM, got %d", len(pms))
	}

	// 8. Attach a second PM — first must remain default.
	pm2, err := svc.AttachFromSetupIntent(ctx, tenantID, cust.ID, "pm_loop_second")
	if err != nil {
		t.Fatalf("attach second: %v", err)
	}
	if pm2.IsDefault {
		t.Fatalf("second PM must NOT auto-steal default; existing one wins")
	}
	pms, err = svc.List(ctx, tenantID, cust.ID)
	if err != nil {
		t.Fatalf("list after second attach: %v", err)
	}
	if len(pms) != 2 {
		t.Fatalf("expected 2 PMs, got %d", len(pms))
	}

	// 9. Customer promotes the second PM.
	promoted, err := svc.SetDefault(ctx, tenantID, cust.ID, pm2.ID)
	if err != nil {
		t.Fatalf("set default: %v", err)
	}
	if !promoted.IsDefault {
		t.Fatalf("promoted PM should be default")
	}
	summary, err = custStore.GetPaymentSetup(ctx, tenantID, cust.ID)
	if err != nil {
		t.Fatalf("get summary after set-default: %v", err)
	}
	if summary.StripePaymentMethodID != "pm_loop_second" {
		t.Fatalf("summary should track new default; got %q", summary.StripePaymentMethodID)
	}

	// 10. Detach current default — the older PM auto-promotes.
	if _, err := svc.Detach(ctx, tenantID, cust.ID, pm2.ID); err != nil {
		t.Fatalf("detach current default: %v", err)
	}
	if stripe.detachCalls != 1 {
		t.Fatalf("expected 1 detach call on Stripe, got %d", stripe.detachCalls)
	}
	pms, err = svc.List(ctx, tenantID, cust.ID)
	if err != nil {
		t.Fatalf("list after first detach: %v", err)
	}
	if len(pms) != 1 || pms[0].ID != pm1.ID || !pms[0].IsDefault {
		t.Fatalf("expected pm1 to be sole, default-promoted PM; got %+v", pms)
	}
	summary, err = custStore.GetPaymentSetup(ctx, tenantID, cust.ID)
	if err != nil {
		t.Fatalf("get summary after promote-on-detach: %v", err)
	}
	if summary.StripePaymentMethodID != "pm_loop_first" {
		t.Fatalf("summary should reflect auto-promoted pm1; got %q", summary.StripePaymentMethodID)
	}

	// 11. Detach the last PM — summary should clear back to missing.
	if _, err := svc.Detach(ctx, tenantID, cust.ID, pm1.ID); err != nil {
		t.Fatalf("detach last PM: %v", err)
	}
	pms, err = svc.List(ctx, tenantID, cust.ID)
	if err != nil {
		t.Fatalf("list after final detach: %v", err)
	}
	if len(pms) != 0 {
		t.Fatalf("expected 0 PMs after final detach, got %d", len(pms))
	}
	summary, err = custStore.GetPaymentSetup(ctx, tenantID, cust.ID)
	if err != nil {
		t.Fatalf("get summary after final detach: %v", err)
	}
	if summary.SetupStatus != domain.PaymentSetupMissing {
		t.Fatalf("summary status should be missing after all PMs detached; got %q", summary.SetupStatus)
	}
	if summary.DefaultPaymentMethodPresent {
		t.Fatalf("summary default_present should be false after all PMs detached")
	}
	if summary.StripePaymentMethodID != "" {
		t.Fatalf("summary stripe pm should be cleared; got %q", summary.StripePaymentMethodID)
	}
}

// loopFakeStripe satisfies paymentmethods.StripeAPI for the integration
// test. Returns deterministic stand-ins for the four Stripe RPCs the
// Service makes during the portal lifecycle.
type loopFakeStripe struct {
	customerID        string
	setupSessionCalls int
	setupIntentCalls  int
	detachCalls       int
}

func (f *loopFakeStripe) EnsureStripeCustomer(_ context.Context, _, _ string) (string, error) {
	return f.customerID, nil
}
func (f *loopFakeStripe) CreateSetupIntent(_ context.Context, _ string, _ map[string]string) (string, string, error) {
	f.setupIntentCalls++
	return "seti_secret_loop", "seti_loop", nil
}
func (f *loopFakeStripe) CreateSetupCheckoutSession(_ context.Context, _, _ string, _ map[string]string) (string, string, error) {
	f.setupSessionCalls++
	return "https://checkout.stripe.com/loop", "cs_loop", nil
}
func (f *loopFakeStripe) DetachPaymentMethod(_ context.Context, _ string) error {
	f.detachCalls++
	return nil
}
func (f *loopFakeStripe) FetchPaymentMethodCard(_ context.Context, pmID string) (string, string, int, int, error) {
	// Yield distinct-but-deterministic metadata per PM so the summary row
	// can be inspected for correctness.
	switch pmID {
	case "pm_loop_first":
		return "visa", "4242", 12, 2030, nil
	case "pm_loop_second":
		return "mastercard", "5555", 6, 2031, nil
	default:
		return "visa", "0000", 1, 2030, nil
	}
}
