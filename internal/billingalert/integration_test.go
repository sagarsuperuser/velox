package billingalert_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/sagarsuperuser/velox/internal/billingalert"
	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/pricing"
	"github.com/sagarsuperuser/velox/internal/subscription"
	"github.com/sagarsuperuser/velox/internal/testutil"
	"github.com/sagarsuperuser/velox/internal/usage"
)

// alertFixture wires real per-domain stores against a clean test DB so
// the evaluator path exercises the full SQL → tx → outbox chain. The
// outbox enqueuer is a fake (we count + capture rows) — the real
// *webhook.OutboxStore is exercised separately, and using it here would
// pull in the entire webhook signing config without adding coverage.
//
// Mirrors previewFixture but stops short of building a billing.Engine —
// the billingalert evaluator never calls into engine paths; it composes
// directly on top of usage.AggregateByPricingRules.
type alertFixture struct {
	db          *postgres.DB
	tenantID    string
	customerSvc *customer.Service
	pricingSvc  *pricing.Service
	subStore    *subscription.PostgresStore
	usageSvc    *usage.Service
	store       *billingalert.PostgresStore
	svc         *billingalert.Service
	outbox      *fakeOutbox
	evaluator   *billingalert.Evaluator
}

func newAlertFixture(t *testing.T, name string) *alertFixture {
	t.Helper()
	db := testutil.SetupTestDB(t)
	tenantID := testutil.CreateTestTenant(t, db, name)

	customerStore := customer.NewPostgresStore(db)
	customerSvc := customer.NewService(customerStore)
	pricingStore := pricing.NewPostgresStore(db)
	pricingSvc := pricing.NewService(pricingStore)
	subStore := subscription.NewPostgresStore(db)
	usageStore := usage.NewPostgresStore(db)
	usageSvc := usage.NewService(usageStore)

	store := billingalert.NewPostgresStore(db)
	svc := billingalert.NewService(store, customerStore, pricingSvc)
	outbox := &fakeOutbox{}
	evaluator := billingalert.NewEvaluator(
		store,
		customerStore,
		&fixtureSubLister{store: subStore},
		pricingSvc,
		usageSvc,
		outbox,
		nil,
	)
	// No locker → the test runs every tick without leader gating, so
	// RunOnce is deterministic. Production wires the real *postgres.DB
	// locker via NewBillingAlertLockerAdapter.

	return &alertFixture{
		db:          db,
		tenantID:    tenantID,
		customerSvc: customerSvc,
		pricingSvc:  pricingSvc,
		subStore:    subStore,
		usageSvc:    usageSvc,
		store:       store,
		svc:         svc,
		outbox:      outbox,
		evaluator:   evaluator,
	}
}

// seedSubscription mirrors previewFixture.seedSubscription. Creates a
// customer, a plan with the given meter, and an active subscription with
// a current cycle of [from, to). Returns customer + sub IDs.
func (f *alertFixture) seedSubscription(
	t *testing.T,
	ctx context.Context,
	externalID, planCode, meterID string,
	cycleStart, cycleEnd time.Time,
) (custID, subID string) {
	t.Helper()

	cust, err := f.customerSvc.Create(ctx, f.tenantID, customer.CreateInput{
		ExternalID:  externalID,
		DisplayName: externalID,
		Email:       externalID + "@example.test",
	})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}

	plan, err := f.pricingSvc.CreatePlan(ctx, f.tenantID, pricing.CreatePlanInput{
		Code:            planCode,
		Name:            planCode,
		Currency:        "USD",
		BillingInterval: domain.BillingMonthly,
		BaseAmountCents: 0,
		MeterIDs:        []string{meterID},
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}

	subID = postgres.NewID("vlx_sub")
	tx, err := f.db.BeginTx(ctx, postgres.TxTenant, f.tenantID)
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
	`, subID, f.tenantID, "code-"+externalID, planCode+"-sub", cust.ID,
		cycleStart, cycleEnd, now)
	if err != nil {
		t.Fatalf("insert sub: %v", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO subscription_items (id, tenant_id, subscription_id, plan_id, quantity, metadata, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 1, '{}'::jsonb, $5, $5)
	`, postgres.NewID("vlx_si"), f.tenantID, subID, plan.ID, now)
	if err != nil {
		t.Fatalf("insert sub item: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit sub: %v", err)
	}
	return cust.ID, subID
}

// rollSubCycle advances the subscription's current_billing_period to the
// supplied window. The evaluator's per_period rearm path keys on the
// new from > alert.last_period_start condition; bumping the cycle in the
// fixture is the cleanest way to simulate cycle rollover without running
// the real billing scheduler.
func (f *alertFixture) rollSubCycle(t *testing.T, ctx context.Context, subID string, from, to time.Time) {
	t.Helper()
	tx, err := f.db.BeginTx(ctx, postgres.TxTenant, f.tenantID)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer postgres.Rollback(tx)
	if _, err := tx.ExecContext(ctx, `
		UPDATE subscriptions
		SET current_billing_period_start = $2,
		    current_billing_period_end = $3,
		    next_billing_at = $3,
		    updated_at = now()
		WHERE id = $1
	`, subID, from, to); err != nil {
		t.Fatalf("roll cycle: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit roll: %v", err)
	}
}

// seedSimpleMeter creates a meter with a single flat rating rule at the
// given amount-cents. Most tests want one alert against one meter; this
// keeps the boilerplate down.
func (f *alertFixture) seedSimpleMeter(
	t *testing.T,
	ctx context.Context,
	key, ruleKey string,
	flatCents int64,
) string {
	t.Helper()
	rrv, err := f.pricingSvc.CreateRatingRule(ctx, f.tenantID, pricing.CreateRatingRuleInput{
		RuleKey:         ruleKey,
		Name:            ruleKey,
		Mode:            domain.PricingFlat,
		Currency:        "USD",
		FlatAmountCents: flatCents,
	})
	if err != nil {
		t.Fatalf("create rating rule: %v", err)
	}
	meter, err := f.pricingSvc.CreateMeter(ctx, f.tenantID, pricing.CreateMeterInput{
		Key:                 key,
		Name:                key,
		Unit:                "tokens",
		Aggregation:         "sum",
		RatingRuleVersionID: rrv.ID,
	})
	if err != nil {
		t.Fatalf("create meter: %v", err)
	}
	return meter.ID
}

// TestEvaluator_FiresOnceForOneTime is the happy path: a one_time alert
// crosses its threshold, the evaluator inserts a trigger row, flips the
// alert's status to triggered, and enqueues exactly one outbox row. A
// subsequent tick (no new usage) MUST NOT fire again — one_time alerts
// transition to a terminal status.
func TestEvaluator_FiresOnceForOneTime(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: skipped in -short mode")
	}
	f := newAlertFixture(t, "Alerts FiresOnceForOneTime")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cycleStart := time.Now().UTC().Truncate(time.Hour).Add(-72 * time.Hour)
	cycleEnd := cycleStart.Add(30 * 24 * time.Hour)

	meterID := f.seedSimpleMeter(t, ctx, "tokens-1t", "tokens-1t-rule", 1)
	custID, _ := f.seedSubscription(t, ctx, "cus_alrt_1t", "pln_alrt_1t", meterID, cycleStart, cycleEnd)

	// 100 events × qty=10 × 1¢ = 1000c. Threshold = 500c → fires.
	for i := 0; i < 100; i++ {
		ts := cycleStart.Add(time.Duration(i) * time.Hour)
		if _, err := f.usageSvc.Ingest(ctx, f.tenantID, usage.IngestInput{
			CustomerID: custID, MeterID: meterID,
			Quantity: decimal.NewFromInt(10), Timestamp: &ts,
		}); err != nil {
			t.Fatalf("ingest %d: %v", i, err)
		}
	}

	threshold := int64(500)
	alert, err := f.svc.Create(ctx, f.tenantID, billingalert.CreateRequest{
		Title:          "spend > 500c",
		CustomerID:     custID,
		MeterID:        meterID,
		AmountCentsGTE: &threshold,
		Recurrence:     domain.BillingAlertRecurrenceOneTime,
	})
	if err != nil {
		t.Fatalf("create alert: %v", err)
	}

	f.evaluator.RunOnce(ctx)

	// Reload alert + assert state transition.
	got, err := f.svc.Get(ctx, f.tenantID, alert.ID)
	if err != nil {
		t.Fatalf("get alert: %v", err)
	}
	if got.Status != domain.BillingAlertStatusTriggered {
		t.Errorf("status = %q, want triggered", got.Status)
	}
	if got.LastTriggeredAt == nil {
		t.Error("last_triggered_at should be set")
	}

	if len(f.outbox.captured) != 1 {
		t.Fatalf("expected 1 outbox row, got %d", len(f.outbox.captured))
	}
	if f.outbox.captured[0].eventType != domain.EventBillingAlertTriggered {
		t.Errorf("event_type = %q, want %q", f.outbox.captured[0].eventType, domain.EventBillingAlertTriggered)
	}
	if f.outbox.captured[0].tenantID != f.tenantID {
		t.Errorf("outbox tenant_id = %q, want %q", f.outbox.captured[0].tenantID, f.tenantID)
	}

	// Tick again — should NOT fire (terminal).
	f.evaluator.RunOnce(ctx)
	if len(f.outbox.captured) != 1 {
		t.Errorf("one_time fired again on second tick; outbox count = %d, want 1", len(f.outbox.captured))
	}
}

// TestEvaluator_FiresPerPeriodAndRearms simulates the per_period
// recurrence: alert fires in cycle N, status flips to
// triggered_for_period; cycle rolls forward; evaluator rearms the alert
// to active and fires again on the new cycle's threshold crossing.
func TestEvaluator_FiresPerPeriodAndRearms(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: skipped in -short mode")
	}
	f := newAlertFixture(t, "Alerts FiresPerPeriodAndRearms")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cycleStart := time.Now().UTC().Truncate(time.Hour).Add(-72 * time.Hour)
	cycleEnd := cycleStart.Add(30 * 24 * time.Hour)

	meterID := f.seedSimpleMeter(t, ctx, "tokens-pp", "tokens-pp-rule", 1)
	custID, subID := f.seedSubscription(t, ctx, "cus_alrt_pp", "pln_alrt_pp", meterID, cycleStart, cycleEnd)

	// 100 × 10 × 1¢ = 1000c, threshold 500c.
	for i := 0; i < 100; i++ {
		ts := cycleStart.Add(time.Duration(i) * time.Hour)
		if _, err := f.usageSvc.Ingest(ctx, f.tenantID, usage.IngestInput{
			CustomerID: custID, MeterID: meterID,
			Quantity: decimal.NewFromInt(10), Timestamp: &ts,
		}); err != nil {
			t.Fatalf("ingest %d: %v", i, err)
		}
	}

	threshold := int64(500)
	alert, err := f.svc.Create(ctx, f.tenantID, billingalert.CreateRequest{
		Title:          "spend > 500c per period",
		CustomerID:     custID,
		MeterID:        meterID,
		AmountCentsGTE: &threshold,
		Recurrence:     domain.BillingAlertRecurrencePerPeriod,
	})
	if err != nil {
		t.Fatalf("create alert: %v", err)
	}

	// First tick fires.
	f.evaluator.RunOnce(ctx)
	got, err := f.svc.Get(ctx, f.tenantID, alert.ID)
	if err != nil {
		t.Fatalf("get alert: %v", err)
	}
	if got.Status != domain.BillingAlertStatusTriggeredForPeriod {
		t.Errorf("status after fire = %q, want triggered_for_period", got.Status)
	}
	if len(f.outbox.captured) != 1 {
		t.Fatalf("expected 1 outbox row after first fire, got %d", len(f.outbox.captured))
	}

	// Second tick within same cycle — must not fire again.
	f.evaluator.RunOnce(ctx)
	if len(f.outbox.captured) != 1 {
		t.Errorf("per_period fired again within same cycle; outbox count = %d, want 1", len(f.outbox.captured))
	}

	// Roll the cycle forward + ingest more usage in the new cycle to push
	// it across the threshold.
	newStart := cycleEnd
	newEnd := newStart.Add(30 * 24 * time.Hour)
	f.rollSubCycle(t, ctx, subID, newStart, newEnd)

	for i := 0; i < 100; i++ {
		ts := newStart.Add(time.Duration(i) * time.Hour)
		if _, err := f.usageSvc.Ingest(ctx, f.tenantID, usage.IngestInput{
			CustomerID: custID, MeterID: meterID,
			Quantity: decimal.NewFromInt(10), Timestamp: &ts,
		}); err != nil {
			t.Fatalf("ingest cycle 2 #%d: %v", i, err)
		}
	}

	// Tick — rearm + fire.
	f.evaluator.RunOnce(ctx)

	got, err = f.svc.Get(ctx, f.tenantID, alert.ID)
	if err != nil {
		t.Fatalf("get after rearm: %v", err)
	}
	if got.Status != domain.BillingAlertStatusTriggeredForPeriod {
		t.Errorf("status after rearm fire = %q, want triggered_for_period", got.Status)
	}
	if len(f.outbox.captured) != 2 {
		t.Errorf("expected 2 outbox rows after cycle rollover, got %d", len(f.outbox.captured))
	}

	// Verify two distinct trigger rows exist for this alert (one per cycle).
	count := f.countTriggers(t, ctx, alert.ID)
	if count != 2 {
		t.Errorf("billing_alert_triggers row count = %d, want 2", count)
	}
}

// TestEvaluator_DoubleFireIdempotent simulates a replica race: two
// fires for the same (alert_id, period_from). The UNIQUE constraint
// catches the duplicate; the evaluator surfaces ErrAlreadyFired and
// swallows it as a no-op. No second outbox row, no second trigger row.
func TestEvaluator_DoubleFireIdempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: skipped in -short mode")
	}
	f := newAlertFixture(t, "Alerts DoubleFireIdempotent")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cycleStart := time.Now().UTC().Truncate(time.Hour).Add(-72 * time.Hour)
	cycleEnd := cycleStart.Add(30 * 24 * time.Hour)
	meterID := f.seedSimpleMeter(t, ctx, "tokens-idem", "tokens-idem-rule", 1)
	custID, _ := f.seedSubscription(t, ctx, "cus_alrt_idem", "pln_alrt_idem", meterID, cycleStart, cycleEnd)

	for i := 0; i < 100; i++ {
		ts := cycleStart.Add(time.Duration(i) * time.Hour)
		if _, err := f.usageSvc.Ingest(ctx, f.tenantID, usage.IngestInput{
			CustomerID: custID, MeterID: meterID,
			Quantity: decimal.NewFromInt(10), Timestamp: &ts,
		}); err != nil {
			t.Fatalf("ingest %d: %v", i, err)
		}
	}

	threshold := int64(500)
	alert, err := f.svc.Create(ctx, f.tenantID, billingalert.CreateRequest{
		Title:          "spend > 500c idem",
		CustomerID:     custID,
		MeterID:        meterID,
		AmountCentsGTE: &threshold,
		Recurrence:     domain.BillingAlertRecurrencePerPeriod,
	})
	if err != nil {
		t.Fatalf("create alert: %v", err)
	}

	// First tick fires.
	f.evaluator.RunOnce(ctx)
	if len(f.outbox.captured) != 1 {
		t.Fatalf("first tick: expected 1 outbox row, got %d", len(f.outbox.captured))
	}

	// Manually flip the alert back to 'active' to bypass the
	// triggered-for-period guard and force a second fire attempt
	// against the same (alert_id, period_from). The UNIQUE catches it.
	tx, err := f.db.BeginTx(ctx, postgres.TxTenant, f.tenantID)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE billing_alerts SET status='active' WHERE id=$1`, alert.ID); err != nil {
		t.Fatalf("force active: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit force: %v", err)
	}

	f.evaluator.RunOnce(ctx)

	// Outbox count must remain 1; trigger row count must remain 1.
	if len(f.outbox.captured) != 1 {
		t.Errorf("double-fire emitted second outbox row; count = %d", len(f.outbox.captured))
	}
	if got := f.countTriggers(t, ctx, alert.ID); got != 1 {
		t.Errorf("trigger row count = %d, want 1 (UNIQUE collapsed the duplicate)", got)
	}
}

// TestEvaluator_ArchivedSkipped: archived alerts must NOT be evaluated,
// even when usage crosses the threshold. The partial index includes only
// active + triggered_for_period; archived rows are excluded by predicate.
func TestEvaluator_ArchivedSkipped(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: skipped in -short mode")
	}
	f := newAlertFixture(t, "Alerts ArchivedSkipped")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cycleStart := time.Now().UTC().Truncate(time.Hour).Add(-72 * time.Hour)
	cycleEnd := cycleStart.Add(30 * 24 * time.Hour)
	meterID := f.seedSimpleMeter(t, ctx, "tokens-arc", "tokens-arc-rule", 1)
	custID, _ := f.seedSubscription(t, ctx, "cus_alrt_arc", "pln_alrt_arc", meterID, cycleStart, cycleEnd)

	for i := 0; i < 100; i++ {
		ts := cycleStart.Add(time.Duration(i) * time.Hour)
		if _, err := f.usageSvc.Ingest(ctx, f.tenantID, usage.IngestInput{
			CustomerID: custID, MeterID: meterID,
			Quantity: decimal.NewFromInt(10), Timestamp: &ts,
		}); err != nil {
			t.Fatalf("ingest: %v", err)
		}
	}

	threshold := int64(500)
	alert, err := f.svc.Create(ctx, f.tenantID, billingalert.CreateRequest{
		Title:          "spend > 500c (will archive)",
		CustomerID:     custID,
		MeterID:        meterID,
		AmountCentsGTE: &threshold,
		Recurrence:     domain.BillingAlertRecurrenceOneTime,
	})
	if err != nil {
		t.Fatalf("create alert: %v", err)
	}

	if _, err := f.svc.Archive(ctx, f.tenantID, alert.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}

	f.evaluator.RunOnce(ctx)

	if len(f.outbox.captured) != 0 {
		t.Errorf("archived alert fired; outbox count = %d, want 0", len(f.outbox.captured))
	}
	got, err := f.svc.Get(ctx, f.tenantID, alert.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != domain.BillingAlertStatusArchived {
		t.Errorf("status = %q, want archived", got.Status)
	}
}

// TestEvaluator_BelowThresholdNoFire: alerts whose observed amount is
// below the threshold MUST NOT fire — basic correctness.
func TestEvaluator_BelowThresholdNoFire(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: skipped in -short mode")
	}
	f := newAlertFixture(t, "Alerts BelowThresholdNoFire")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cycleStart := time.Now().UTC().Truncate(time.Hour).Add(-72 * time.Hour)
	cycleEnd := cycleStart.Add(30 * 24 * time.Hour)
	meterID := f.seedSimpleMeter(t, ctx, "tokens-low", "tokens-low-rule", 1)
	custID, _ := f.seedSubscription(t, ctx, "cus_alrt_low", "pln_alrt_low", meterID, cycleStart, cycleEnd)

	// 10 events × 10 × 1¢ = 100c. Threshold 500c → must not fire.
	for i := 0; i < 10; i++ {
		ts := cycleStart.Add(time.Duration(i) * time.Hour)
		if _, err := f.usageSvc.Ingest(ctx, f.tenantID, usage.IngestInput{
			CustomerID: custID, MeterID: meterID,
			Quantity: decimal.NewFromInt(10), Timestamp: &ts,
		}); err != nil {
			t.Fatalf("ingest: %v", err)
		}
	}

	threshold := int64(500)
	alert, err := f.svc.Create(ctx, f.tenantID, billingalert.CreateRequest{
		Title:          "spend > 500c (low usage)",
		CustomerID:     custID,
		MeterID:        meterID,
		AmountCentsGTE: &threshold,
		Recurrence:     domain.BillingAlertRecurrenceOneTime,
	})
	if err != nil {
		t.Fatalf("create alert: %v", err)
	}

	f.evaluator.RunOnce(ctx)

	got, err := f.svc.Get(ctx, f.tenantID, alert.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != domain.BillingAlertStatusActive {
		t.Errorf("status = %q, want active (below threshold)", got.Status)
	}
	if len(f.outbox.captured) != 0 {
		t.Errorf("below-threshold alert fired; outbox count = %d, want 0", len(f.outbox.captured))
	}
}

// TestEvaluator_NoSubscription: a customer without an active sub has no
// current cycle; the evaluator skips the alert (debug log) and leaves
// status=active so it can fire when the customer resubscribes.
func TestEvaluator_NoSubscription(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: skipped in -short mode")
	}
	f := newAlertFixture(t, "Alerts NoSubscription")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create a customer (no sub) — required because the alert references
	// it. The seedSimpleMeter is also required because filter.meter_id is
	// validated by the service via meter existence check.
	meterID := f.seedSimpleMeter(t, ctx, "tokens-nosub", "tokens-nosub-rule", 1)
	cust, err := f.customerSvc.Create(ctx, f.tenantID, customer.CreateInput{
		ExternalID:  "cus_no_sub",
		DisplayName: "no_sub",
		Email:       "no_sub@example.test",
	})
	if err != nil {
		t.Fatalf("create cust: %v", err)
	}

	threshold := int64(500)
	alert, err := f.svc.Create(ctx, f.tenantID, billingalert.CreateRequest{
		Title:          "no sub alert",
		CustomerID:     cust.ID,
		MeterID:        meterID,
		AmountCentsGTE: &threshold,
		Recurrence:     domain.BillingAlertRecurrenceOneTime,
	})
	if err != nil {
		t.Fatalf("create alert: %v", err)
	}

	f.evaluator.RunOnce(ctx)

	got, err := f.svc.Get(ctx, f.tenantID, alert.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != domain.BillingAlertStatusActive {
		t.Errorf("status = %q, want active (no sub → no cycle → skip)", got.Status)
	}
	if len(f.outbox.captured) != 0 {
		t.Errorf("no-sub alert fired; outbox count = %d, want 0", len(f.outbox.captured))
	}
}

// TestEvaluator_MultiTenantIsolation: ListCandidates runs under TxBypass
// (cross-tenant scan) but the per-alert evaluation tx is tenant-scoped.
// Two tenants each with an alert past threshold must each fire exactly
// one outbox row tagged with the right tenant_id.
func TestEvaluator_MultiTenantIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: skipped in -short mode")
	}
	f := newAlertFixture(t, "Alerts MultiTenantIsolation")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tenantB := testutil.CreateTestTenant(t, f.db, "Alerts MultiTenantIsolation B")

	cycleStart := time.Now().UTC().Truncate(time.Hour).Add(-72 * time.Hour)
	cycleEnd := cycleStart.Add(30 * 24 * time.Hour)

	// Tenant A.
	meterA := f.seedSimpleMeter(t, ctx, "tokens-a", "tokens-a-rule", 1)
	custA, _ := f.seedSubscription(t, ctx, "cus_alrt_a", "pln_alrt_a", meterA, cycleStart, cycleEnd)
	for i := 0; i < 100; i++ {
		ts := cycleStart.Add(time.Duration(i) * time.Hour)
		if _, err := f.usageSvc.Ingest(ctx, f.tenantID, usage.IngestInput{
			CustomerID: custA, MeterID: meterA,
			Quantity: decimal.NewFromInt(10), Timestamp: &ts,
		}); err != nil {
			t.Fatalf("ingest A: %v", err)
		}
	}

	// Tenant B — independent fixture-like setup (different tenantID).
	customerStoreB := customer.NewPostgresStore(f.db)
	customerSvcB := customer.NewService(customerStoreB)
	pricingStoreB := pricing.NewPostgresStore(f.db)
	pricingSvcB := pricing.NewService(pricingStoreB)
	usageSvcB := usage.NewService(usage.NewPostgresStore(f.db))
	storeB := billingalert.NewPostgresStore(f.db)
	svcB := billingalert.NewService(storeB, customerStoreB, pricingSvcB)

	rrvB, err := pricingSvcB.CreateRatingRule(ctx, tenantB, pricing.CreateRatingRuleInput{
		RuleKey: "tokens-b-rule", Name: "B", Mode: domain.PricingFlat, Currency: "USD", FlatAmountCents: 1,
	})
	if err != nil {
		t.Fatalf("rrv B: %v", err)
	}
	meterB, err := pricingSvcB.CreateMeter(ctx, tenantB, pricing.CreateMeterInput{
		Key: "tokens-b", Name: "tokens-b", Unit: "tokens",
		Aggregation: "sum", RatingRuleVersionID: rrvB.ID,
	})
	if err != nil {
		t.Fatalf("meter B: %v", err)
	}
	planB, err := pricingSvcB.CreatePlan(ctx, tenantB, pricing.CreatePlanInput{
		Code: "pln_b", Name: "pln_b", Currency: "USD",
		BillingInterval: domain.BillingMonthly, MeterIDs: []string{meterB.ID},
	})
	if err != nil {
		t.Fatalf("plan B: %v", err)
	}
	custB, err := customerSvcB.Create(ctx, tenantB, customer.CreateInput{
		ExternalID: "cus_alrt_b", DisplayName: "B", Email: "b@example.test",
	})
	if err != nil {
		t.Fatalf("cust B: %v", err)
	}
	subB := postgres.NewID("vlx_sub")
	tx, err := f.db.BeginTx(ctx, postgres.TxTenant, tenantB)
	if err != nil {
		t.Fatalf("begin sub B tx: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO subscriptions (id, tenant_id, code, display_name, customer_id, status, billing_time,
			current_billing_period_start, current_billing_period_end, next_billing_at, created_at, updated_at)
		VALUES ($1, $2, 'code-B', 'sub-B', $3, 'active', 'anniversary', $4, $5, $5, now(), now())
	`, subB, tenantB, custB.ID, cycleStart, cycleEnd); err != nil {
		t.Fatalf("insert sub B: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO subscription_items (id, tenant_id, subscription_id, plan_id, quantity, metadata, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 1, '{}'::jsonb, now(), now())
	`, postgres.NewID("vlx_si"), tenantB, subB, planB.ID); err != nil {
		t.Fatalf("insert sub item B: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit sub B: %v", err)
	}
	for i := 0; i < 100; i++ {
		ts := cycleStart.Add(time.Duration(i) * time.Hour)
		if _, err := usageSvcB.Ingest(ctx, tenantB, usage.IngestInput{
			CustomerID: custB.ID, MeterID: meterB.ID,
			Quantity: decimal.NewFromInt(10), Timestamp: &ts,
		}); err != nil {
			t.Fatalf("ingest B: %v", err)
		}
	}

	thresholdA := int64(500)
	if _, err := f.svc.Create(ctx, f.tenantID, billingalert.CreateRequest{
		Title: "A alert", CustomerID: custA, MeterID: meterA,
		AmountCentsGTE: &thresholdA, Recurrence: domain.BillingAlertRecurrenceOneTime,
	}); err != nil {
		t.Fatalf("create alert A: %v", err)
	}
	thresholdB := int64(500)
	if _, err := svcB.Create(ctx, tenantB, billingalert.CreateRequest{
		Title: "B alert", CustomerID: custB.ID, MeterID: meterB.ID,
		AmountCentsGTE: &thresholdB, Recurrence: domain.BillingAlertRecurrenceOneTime,
	}); err != nil {
		t.Fatalf("create alert B: %v", err)
	}

	f.evaluator.RunOnce(ctx)

	if len(f.outbox.captured) != 2 {
		t.Fatalf("expected 2 outbox rows (one per tenant), got %d", len(f.outbox.captured))
	}
	tenants := map[string]int{}
	for _, e := range f.outbox.captured {
		tenants[e.tenantID]++
	}
	if tenants[f.tenantID] != 1 {
		t.Errorf("tenant A outbox count = %d, want 1", tenants[f.tenantID])
	}
	if tenants[tenantB] != 1 {
		t.Errorf("tenant B outbox count = %d, want 1", tenants[tenantB])
	}
}

// TestEvaluator_AtomicityOnRollback: an outbox.Enqueue that fails MUST
// roll back the trigger row insert + alert status update. After the
// failed tick, the alert is still 'active' and the trigger row count is
// zero — the invariant the whole atomicity contract rests on.
func TestEvaluator_AtomicityOnRollback(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: skipped in -short mode")
	}
	f := newAlertFixture(t, "Alerts AtomicityOnRollback")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cycleStart := time.Now().UTC().Truncate(time.Hour).Add(-72 * time.Hour)
	cycleEnd := cycleStart.Add(30 * 24 * time.Hour)
	meterID := f.seedSimpleMeter(t, ctx, "tokens-atomic", "tokens-atomic-rule", 1)
	custID, _ := f.seedSubscription(t, ctx, "cus_alrt_atomic", "pln_alrt_atomic", meterID, cycleStart, cycleEnd)

	for i := 0; i < 100; i++ {
		ts := cycleStart.Add(time.Duration(i) * time.Hour)
		if _, err := f.usageSvc.Ingest(ctx, f.tenantID, usage.IngestInput{
			CustomerID: custID, MeterID: meterID,
			Quantity: decimal.NewFromInt(10), Timestamp: &ts,
		}); err != nil {
			t.Fatalf("ingest: %v", err)
		}
	}

	threshold := int64(500)
	alert, err := f.svc.Create(ctx, f.tenantID, billingalert.CreateRequest{
		Title:          "atomic test",
		CustomerID:     custID,
		MeterID:        meterID,
		AmountCentsGTE: &threshold,
		Recurrence:     domain.BillingAlertRecurrenceOneTime,
	})
	if err != nil {
		t.Fatalf("create alert: %v", err)
	}

	// Force the outbox enqueue to fail; the evaluator should roll back
	// the trigger+status update inside the same tx.
	f.outbox.failNext = errors.New("simulated outbox failure")
	f.evaluator.RunOnce(ctx)

	got, err := f.svc.Get(ctx, f.tenantID, alert.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != domain.BillingAlertStatusActive {
		t.Errorf("status after failed enqueue = %q, want active (rollback)", got.Status)
	}
	if got.LastTriggeredAt != nil {
		t.Errorf("last_triggered_at should be nil after rollback, got %v", got.LastTriggeredAt)
	}
	if got := f.countTriggers(t, ctx, alert.ID); got != 0 {
		t.Errorf("trigger row count after rollback = %d, want 0", got)
	}
	if len(f.outbox.captured) != 0 {
		t.Errorf("outbox should not have captured the failed row, got %d", len(f.outbox.captured))
	}

	// Clear the failure and re-tick; the alert should now fire cleanly.
	f.outbox.failNext = nil
	f.evaluator.RunOnce(ctx)
	got, err = f.svc.Get(ctx, f.tenantID, alert.ID)
	if err != nil {
		t.Fatalf("get after recovery: %v", err)
	}
	if got.Status != domain.BillingAlertStatusTriggered {
		t.Errorf("status after recovery tick = %q, want triggered", got.Status)
	}
	if len(f.outbox.captured) != 1 {
		t.Errorf("expected 1 outbox row after recovery, got %d", len(f.outbox.captured))
	}
}

// TestCreateAlert_RLS: cross-tenant resource references (customer, meter)
// surface as 404, not an authorization-bypass leak. The customer / meter
// lookups in Service.Create are tenant-scoped via RLS — a misrouted ID
// returns ErrNotFound.
func TestCreateAlert_RLS(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: skipped in -short mode")
	}
	f := newAlertFixture(t, "Alerts CreateRLS")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tenantB := testutil.CreateTestTenant(t, f.db, "Alerts CreateRLS B")

	custB, err := f.customerSvc.Create(ctx, tenantB, customer.CreateInput{
		ExternalID: "cus_b", DisplayName: "B", Email: "b@example.test",
	})
	if err != nil {
		t.Fatalf("create cust B: %v", err)
	}

	threshold := int64(500)
	_, err = f.svc.Create(ctx, f.tenantID, billingalert.CreateRequest{
		Title:          "cross-tenant",
		CustomerID:     custB.ID, // belongs to tenant B; tenant A is creating
		AmountCentsGTE: &threshold,
		Recurrence:     domain.BillingAlertRecurrenceOneTime,
	})
	if !errors.Is(err, errs.ErrNotFound) {
		t.Errorf("expected ErrNotFound for cross-tenant customer, got %v", err)
	}
}

// countTriggers returns the number of billing_alert_triggers rows for an
// alert under the fixture's tenant. Used by integration tests that need
// to assert on persistence side-effects beyond what the public service
// surface returns.
func (f *alertFixture) countTriggers(t *testing.T, ctx context.Context, alertID string) int {
	t.Helper()
	tx, err := f.db.BeginTx(ctx, postgres.TxTenant, f.tenantID)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer postgres.Rollback(tx)
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM billing_alert_triggers WHERE alert_id = $1`, alertID).Scan(&n); err != nil {
		t.Fatalf("count triggers: %v", err)
	}
	return n
}

// fakeOutbox satisfies billingalert.OutboxEnqueuer with a fail-next hook
// for atomicity tests + a captured slice the test can introspect. The
// enqueue runs inside the caller's tx exactly like the real
// *webhook.OutboxStore — but skips the actual INSERT. Atomicity of the
// caller's tx is exercised through failNext: when it returns an error,
// the caller MUST roll back, so the trigger row + status update are
// reverted.
type fakeOutbox struct {
	captured []capturedOutboxRow
	failNext error
}

type capturedOutboxRow struct {
	tenantID  string
	eventType string
	payload   map[string]any
}

func (f *fakeOutbox) Enqueue(ctx context.Context, tx *sql.Tx, tenantID, eventType string, payload map[string]any) (string, error) {
	if f.failNext != nil {
		err := f.failNext
		return "", err
	}
	f.captured = append(f.captured, capturedOutboxRow{
		tenantID:  tenantID,
		eventType: eventType,
		payload:   payload,
	})
	return "vlx_evt_" + eventType, nil
}

// fixtureSubLister adapts *subscription.PostgresStore →
// billingalert.SubscriptionLister with the same translation the
// production adapters do — kept here so the integration test owns no
// state beyond what fixture seeding produces.
type fixtureSubLister struct {
	store *subscription.PostgresStore
}

func (a *fixtureSubLister) List(ctx context.Context, filter billingalert.SubscriptionListFilter) ([]domain.Subscription, int, error) {
	return a.store.List(ctx, subscription.ListFilter{
		TenantID:   filter.TenantID,
		CustomerID: filter.CustomerID,
	})
}
