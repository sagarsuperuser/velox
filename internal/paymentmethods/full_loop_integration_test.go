package paymentmethods_test

import (
	"context"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/customerportal"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/paymentmethods"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
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
	ctx, cancel := context.WithTimeout(postgres.WithLivemode(context.Background(), false), 20*time.Second)
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

	// Seed the canonical Stripe Customer mapping (customers.stripe_customer_id)
	// so EnsureStripeCustomer can short-circuit. Replaces the old
	// customer_payment_setups summary seed (migration 0097 dropped the
	// table).
	if err := custStore.SetStripeCustomerID(ctx, tenantID, cust.ID, "cus_stripe_loop"); err != nil {
		t.Fatalf("seed stripe_customer_id: %v", err)
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

	// Canonical state: payment_methods row exists with is_default=true.
	// Summary table was retired; List filters detached_at IS NULL.
	if pm1.StripePaymentMethodID != "pm_loop_first" {
		t.Fatalf("attached PM mismatch: got %q want pm_loop_first", pm1.StripePaymentMethodID)
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
	if promoted.StripePaymentMethodID != "pm_loop_second" {
		t.Fatalf("promoted should be pm_loop_second; got %q", promoted.StripePaymentMethodID)
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
	// Canonical state assertion lives on the List output above —
	// pm1 promoted, is_default=true. No more summary to check.

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
	// All PMs detached → List returns 0 (canonical state). No more
	// summary row to check; billing.PaymentReadiness reads the same
	// canonical source and will return hasDefaultPM=false here.
}

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
func (f *loopFakeStripe) CreateSetupCheckoutSession(_ context.Context, _, _, _ string, _ map[string]string) (string, string, error) {
	f.setupSessionCalls++
	return "https://checkout.stripe.com/loop", "cs_loop", nil
}
func (f *loopFakeStripe) DetachPaymentMethod(_ context.Context, _ string) error {
	f.detachCalls++
	return nil
}
func (f *loopFakeStripe) FetchPaymentMethodCard(_ context.Context, pmID string) (paymentmethods.CardMetadata, error) {
	// Yield distinct-but-deterministic metadata per PM. Each loop
	// PM gets a distinct fingerprint so the integration test
	// exercises the no-dedupe path (different physical cards).
	switch pmID {
	case "pm_loop_first":
		return paymentmethods.CardMetadata{Brand: "visa", Last4: "4242", ExpMonth: 12, ExpYear: 2030, Fingerprint: "fp_loop_first"}, nil
	case "pm_loop_second":
		return paymentmethods.CardMetadata{Brand: "mastercard", Last4: "5555", ExpMonth: 6, ExpYear: 2031, Fingerprint: "fp_loop_second"}, nil
	default:
		return paymentmethods.CardMetadata{Brand: "visa", Last4: "0000", ExpMonth: 1, ExpYear: 2030, Fingerprint: "fp_loop_default"}, nil
	}
}
func (f *loopFakeStripe) SetDefaultPaymentMethod(_ context.Context, _, _, _ string) error {
	return nil
}
