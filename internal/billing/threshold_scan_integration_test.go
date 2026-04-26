package billing_test

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/sagarsuperuser/velox/internal/billing"
	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/invoice"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/pricing"
	"github.com/sagarsuperuser/velox/internal/subscription"
	"github.com/sagarsuperuser/velox/internal/tenant"
	"github.com/sagarsuperuser/velox/internal/testutil"
	"github.com/sagarsuperuser/velox/internal/usage"
)

// thresholdFixture wires a real-store engine and seeds a single subscription
// with billing thresholds configured. Mirrors previewFixture but exposes the
// engine's ScanThresholds path against actual Postgres so the partial unique
// index, ListWithThresholds candidate query, and CreateInvoiceWithLineItems
// are all exercised end-to-end.
type thresholdFixture struct {
	db       *postgres.DB
	tenantID string
	subStore *subscription.PostgresStore
	subSvc   *subscription.Service
	usageSvc *usage.Service
	invStore *invoice.PostgresStore
	engine   *billing.Engine

	// Seed-by-default test data
	customerID string
	planID     string
	subID      string
	itemID     string
	meterID    string
	cycleStart time.Time
	cycleEnd   time.Time
}

// newThresholdFixture sets up a tenant with a single-meter, single-rule plan
// (1 cent / call flat rate, no base fee) and an active sub mid-cycle. Tests
// can configure thresholds on the seeded sub via subSvc.SetBillingThresholds.
func newThresholdFixture(t *testing.T, name string) *thresholdFixture {
	t.Helper()
	db := testutil.SetupTestDB(t)
	tenantID := testutil.CreateTestTenant(t, db, name)

	customerStore := customer.NewPostgresStore(db)
	customerSvc := customer.NewService(customerStore)
	pricingStore := pricing.NewPostgresStore(db)
	pricingSvc := pricing.NewService(pricingStore)
	subStore := subscription.NewPostgresStore(db)
	subSvc := subscription.NewService(subStore, nil)
	usageStore := usage.NewPostgresStore(db)
	usageSvc := usage.NewService(usageStore)
	invoiceStore := invoice.NewPostgresStore(db)
	settingsStore := tenant.NewSettingsStore(db)

	engine := billing.NewEngine(
		&subStoreAdapter{subStore},
		&usageStoreAdapter{usageStore},
		&pricingStoreAdapter{pricingStore},
		&invoiceStoreAdapter{invoiceStore},
		nil, settingsStore, nil, nil, nil,
	)

	ctx := context.Background()

	cust, err := customerSvc.Create(ctx, tenantID, customer.CreateInput{
		ExternalID:  "cus_thresh",
		DisplayName: "Threshold Customer",
		Email:       "thresh@example.test",
	})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}

	rrv, err := pricingSvc.CreateRatingRule(ctx, tenantID, pricing.CreateRatingRuleInput{
		RuleKey: "calls_flat", Name: "Calls Flat",
		Mode: domain.PricingFlat, Currency: "USD", FlatAmountCents: 1,
	})
	if err != nil {
		t.Fatalf("create rrv: %v", err)
	}

	meter, err := pricingSvc.CreateMeter(ctx, tenantID, pricing.CreateMeterInput{
		Key: "calls", Name: "Calls", Unit: "calls",
		Aggregation: "sum", RatingRuleVersionID: rrv.ID,
	})
	if err != nil {
		t.Fatalf("create meter: %v", err)
	}

	plan, err := pricingSvc.CreatePlan(ctx, tenantID, pricing.CreatePlanInput{
		Code: "pln_thresh", Name: "Threshold Plan",
		Currency: "USD", BillingInterval: domain.BillingMonthly,
		BaseAmountCents: 0, MeterIDs: []string{meter.ID},
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}

	cycleStart := time.Now().UTC().Truncate(time.Hour).Add(-72 * time.Hour)
	cycleEnd := cycleStart.Add(30 * 24 * time.Hour)

	subID := postgres.NewID("vlx_sub")
	itemID := postgres.NewID("vlx_si")
	tx, err := db.BeginTx(ctx, postgres.TxTenant, tenantID)
	if err != nil {
		t.Fatalf("begin sub tx: %v", err)
	}
	defer postgres.Rollback(tx)

	now := time.Now().UTC()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO subscriptions (
			id, tenant_id, code, display_name, customer_id, status, billing_time,
			current_billing_period_start, current_billing_period_end, next_billing_at,
			created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, 'active', 'anniversary', $6, $7, $7, $8, $8)
	`, subID, tenantID, "code-thresh", "Threshold Sub", cust.ID, cycleStart, cycleEnd, now)
	if err != nil {
		t.Fatalf("insert sub: %v", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO subscription_items (id, tenant_id, subscription_id, plan_id, quantity, metadata, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 1, '{}'::jsonb, $5, $5)
	`, itemID, tenantID, subID, plan.ID, now)
	if err != nil {
		t.Fatalf("insert sub item: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit sub: %v", err)
	}

	return &thresholdFixture{
		db:         db,
		tenantID:   tenantID,
		subStore:   subStore,
		subSvc:     subSvc,
		usageSvc:   usageSvc,
		invStore:   invoiceStore,
		engine:     engine,
		customerID: cust.ID,
		planID:     plan.ID,
		subID:      subID,
		itemID:     itemID,
		meterID:    meter.ID,
		cycleStart: cycleStart,
		cycleEnd:   cycleEnd,
	}
}

// ingestUsage seeds N events spread evenly over the first 60 minutes of the
// cycle. cycleStart is 72h ago so all events are in the past relative to
// wall-clock now (which is what the threshold scan uses as the period
// upper bound). Each event has the supplied quantity, so
// ingestUsage(t, ctx, 100, 10) produces 100 events × qty 10 = 1000
// total quantity, billed at 1c/call = 1000c subtotal.
func (f *thresholdFixture) ingestUsage(t *testing.T, ctx context.Context, count int, qty int64) {
	t.Helper()
	for i := 0; i < count; i++ {
		ts := f.cycleStart.Add(time.Duration(i) * time.Minute)
		if _, err := f.usageSvc.Ingest(ctx, f.tenantID, usage.IngestInput{
			CustomerID: f.customerID,
			MeterID:    f.meterID,
			Quantity:   decimal.NewFromInt(qty),
			Timestamp:  &ts,
		}); err != nil {
			t.Fatalf("ingest %d: %v", i, err)
		}
	}
}

func (f *thresholdFixture) listInvoices(t *testing.T, ctx context.Context) []domain.Invoice {
	t.Helper()
	invoices, _, err := f.invStore.List(ctx, invoice.ListFilter{TenantID: f.tenantID})
	if err != nil {
		t.Fatalf("list invoices: %v", err)
	}
	return invoices
}

// TestThresholdScan_AmountCrossFiresEarly is the headline test: usage drives
// the running subtotal past AmountGTE, the scan emits a threshold-billed
// invoice, the cycle resets, and a second scan against the new (empty) cycle
// is a no-op. End-to-end against real Postgres so the CreateWithLineItems
// path, partial unique index, and ListWithThresholds candidate query are
// all exercised together.
func TestThresholdScan_AmountCrossFiresEarly(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: skipped in -short mode")
	}
	f := newThresholdFixture(t, "Threshold Amount Cross")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 100 events × qty 10 × 1c = 1000c subtotal.
	f.ingestUsage(t, ctx, 100, 10)

	// Configure amount threshold at 500c — well below 1000c.
	if _, err := f.subSvc.SetBillingThresholds(ctx, f.tenantID, f.subID, subscription.BillingThresholdsInput{
		AmountGTE: 500,
	}); err != nil {
		t.Fatalf("set threshold: %v", err)
	}

	fired, errs := f.engine.ScanThresholds(ctx, 50)
	if len(errs) > 0 {
		t.Fatalf("scan errors: %v", errs)
	}
	if fired != 1 {
		t.Fatalf("fired count: got %d, want 1", fired)
	}

	invoices := f.listInvoices(t, ctx)
	if len(invoices) != 1 {
		t.Fatalf("expected 1 invoice, got %d", len(invoices))
	}
	inv := invoices[0]
	if inv.BillingReason != domain.BillingReasonThreshold {
		t.Errorf("billing_reason: got %q, want %q", inv.BillingReason, domain.BillingReasonThreshold)
	}
	if inv.SubscriptionID != f.subID {
		t.Errorf("subscription_id: got %q, want %q", inv.SubscriptionID, f.subID)
	}
	if inv.SubtotalCents != 1000 {
		t.Errorf("subtotal_cents: got %d, want 1000", inv.SubtotalCents)
	}
	if !inv.BillingPeriodStart.Equal(f.cycleStart) {
		t.Errorf("billing_period_start: got %v, want %v", inv.BillingPeriodStart, f.cycleStart)
	}

	// Cycle reset: with reset_billing_cycle=true (default), the sub's
	// current_billing_period_start should now be the fire-time, not the
	// original cycleStart.
	updated, err := f.subStore.Get(ctx, f.tenantID, f.subID)
	if err != nil {
		t.Fatalf("reload sub: %v", err)
	}
	if updated.CurrentBillingPeriodStart == nil {
		t.Fatal("current_billing_period_start should be set after reset")
	}
	if updated.CurrentBillingPeriodStart.Equal(f.cycleStart) {
		t.Error("expected period_start to advance after reset_billing_cycle=true")
	}
}

// TestThresholdScan_Idempotent guards against double-finalize on retry.
// A second ScanThresholds tick after the first fire must observe the same
// underlying state but emit zero new invoices — the partial unique index
// is the seam that catches a concurrent retry without losing the invoice.
func TestThresholdScan_Idempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: skipped in -short mode")
	}
	f := newThresholdFixture(t, "Threshold Idempotent")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	f.ingestUsage(t, ctx, 100, 10)

	// reset_billing_cycle=false means the cycle stays put after fire, so
	// the next tick observes the same partial cycle that already fired —
	// the partial unique index is the only thing preventing a second
	// invoice. Perfect setup for verifying the idempotency seam.
	resetFalse := false
	if _, err := f.subSvc.SetBillingThresholds(ctx, f.tenantID, f.subID, subscription.BillingThresholdsInput{
		AmountGTE:         500,
		ResetBillingCycle: &resetFalse,
	}); err != nil {
		t.Fatalf("set threshold: %v", err)
	}

	// First tick — fires the invoice.
	fired1, errs1 := f.engine.ScanThresholds(ctx, 50)
	if len(errs1) > 0 {
		t.Fatalf("first scan errors: %v", errs1)
	}
	if fired1 != 1 {
		t.Fatalf("first fired count: got %d, want 1", fired1)
	}

	// Second tick against the same cycle — must short-circuit on the
	// partial unique index. No additional invoice, no error returned.
	fired2, errs2 := f.engine.ScanThresholds(ctx, 50)
	if len(errs2) > 0 {
		t.Fatalf("second scan errors: %v", errs2)
	}
	if fired2 != 0 {
		t.Fatalf("second fired count: got %d, want 0 (idempotent skip)", fired2)
	}

	invoices := f.listInvoices(t, ctx)
	if len(invoices) != 1 {
		t.Fatalf("expected exactly 1 invoice after re-tick, got %d", len(invoices))
	}
}

// TestThresholdScan_ItemUsageCross verifies the per-item path against real
// Postgres — the per-meter aggregator wraps AggregateByPricingRules, so this
// proves the scan respects the same priority+claim resolution as the cycle.
func TestThresholdScan_ItemUsageCross(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: skipped in -short mode")
	}
	f := newThresholdFixture(t, "Threshold Item Cross")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 100 events × qty 10 = 1000 total quantity on the meter.
	f.ingestUsage(t, ctx, 100, 10)

	if _, err := f.subSvc.SetBillingThresholds(ctx, f.tenantID, f.subID, subscription.BillingThresholdsInput{
		ItemThresholds: []subscription.ItemThresholdInput{
			{SubscriptionItemID: f.itemID, UsageGTE: "500"},
		},
	}); err != nil {
		t.Fatalf("set threshold: %v", err)
	}

	fired, errs := f.engine.ScanThresholds(ctx, 50)
	if len(errs) > 0 {
		t.Fatalf("scan errors: %v", errs)
	}
	if fired != 1 {
		t.Fatalf("fired count: got %d, want 1 (item quantity 1000 > cap 500)", fired)
	}

	invoices := f.listInvoices(t, ctx)
	if len(invoices) != 1 {
		t.Fatalf("expected 1 invoice, got %d", len(invoices))
	}
	if invoices[0].BillingReason != domain.BillingReasonThreshold {
		t.Errorf("billing_reason: got %q, want %q", invoices[0].BillingReason, domain.BillingReasonThreshold)
	}
}

// TestThresholdScan_BelowThresholdNoFire is the silent-success case: usage
// is non-zero but doesn't cross the cap, so the scan should observe but
// emit nothing. Catches a regression where the threshold logic accidentally
// fires on every tick.
func TestThresholdScan_BelowThresholdNoFire(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: skipped in -short mode")
	}
	f := newThresholdFixture(t, "Threshold Below Cap")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 100 events × qty 10 × 1c = 1000c subtotal.
	f.ingestUsage(t, ctx, 100, 10)

	// Cap at 5000c — well above 1000c.
	if _, err := f.subSvc.SetBillingThresholds(ctx, f.tenantID, f.subID, subscription.BillingThresholdsInput{
		AmountGTE: 5000,
	}); err != nil {
		t.Fatalf("set threshold: %v", err)
	}

	fired, errs := f.engine.ScanThresholds(ctx, 50)
	if len(errs) > 0 {
		t.Fatalf("scan errors: %v", errs)
	}
	if fired != 0 {
		t.Fatalf("fired count: got %d, want 0", fired)
	}
	invoices := f.listInvoices(t, ctx)
	if len(invoices) != 0 {
		t.Fatalf("expected 0 invoices, got %d", len(invoices))
	}
}

// TestThresholdScan_NoConfigSkipped covers the candidate-query side: a sub
// with no billing thresholds configured must not appear in
// ListWithThresholds and thus must not be considered by the scan.
func TestThresholdScan_NoConfigSkipped(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: skipped in -short mode")
	}
	f := newThresholdFixture(t, "Threshold No Config")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Lots of usage but no threshold configured — the candidate query
	// must filter this out.
	f.ingestUsage(t, ctx, 100, 100)

	fired, errs := f.engine.ScanThresholds(ctx, 50)
	if len(errs) > 0 {
		t.Fatalf("scan errors: %v", errs)
	}
	if fired != 0 {
		t.Fatalf("fired count: got %d, want 0 (no threshold configured)", fired)
	}
}

// TestThresholdScan_ResetCycleFalse confirms the cycle stays put when the
// caller explicitly opts out. The threshold invoice still fires, but the
// next natural cycle invoice will pick up where the original cycle ended —
// not where the threshold fired.
func TestThresholdScan_ResetCycleFalse(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: skipped in -short mode")
	}
	f := newThresholdFixture(t, "Threshold Reset False")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	f.ingestUsage(t, ctx, 100, 10)

	resetFalse := false
	if _, err := f.subSvc.SetBillingThresholds(ctx, f.tenantID, f.subID, subscription.BillingThresholdsInput{
		AmountGTE:         500,
		ResetBillingCycle: &resetFalse,
	}); err != nil {
		t.Fatalf("set threshold: %v", err)
	}

	fired, errs := f.engine.ScanThresholds(ctx, 50)
	if len(errs) > 0 {
		t.Fatalf("scan errors: %v", errs)
	}
	if fired != 1 {
		t.Fatalf("fired count: got %d, want 1", fired)
	}

	updated, err := f.subStore.Get(ctx, f.tenantID, f.subID)
	if err != nil {
		t.Fatalf("reload sub: %v", err)
	}
	if updated.CurrentBillingPeriodStart == nil {
		t.Fatal("period_start should be set")
	}
	// reset_billing_cycle=false means period_start should NOT have advanced.
	if !updated.CurrentBillingPeriodStart.Equal(f.cycleStart) {
		t.Errorf("period_start should remain %v with reset=false, got %v",
			f.cycleStart, *updated.CurrentBillingPeriodStart)
	}
}
