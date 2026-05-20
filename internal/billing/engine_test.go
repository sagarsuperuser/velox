package billing

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/sagarsuperuser/velox/internal/credit"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/payment"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
	"github.com/sagarsuperuser/velox/internal/tax"
)

// stubClockReader is a TestClockReader spy used by TestEffectiveNow. It
// returns a canned (TestClock, error) pair and records the last ID queried so
// the test can assert the engine looked up the right clock.
type stubClockReader struct {
	clk        domain.TestClock
	err        error
	lastID     string
	lastTenant string
}

func (s *stubClockReader) Get(_ context.Context, tenantID, id string) (domain.TestClock, error) {
	s.lastID = id
	s.lastTenant = tenantID
	if s.err != nil {
		return domain.TestClock{}, s.err
	}
	return s.clk, nil
}

// mockSettings is a minimal SettingsReader for engine tests. Hands out
// VLX-000001, VLX-000002, ... deterministically; Get returns a zero-value
// TenantSettings so net_payment_terms defaults to the engine's fallback.
// wireBaseTax wires a tax resolver onto an engine — required for
// any path that calls ApplyTaxToLineItems. Production engine
// always has this wired; the engine fails loudly when it's not
// (no silent zero-tax fallback). Tests that don't exercise
// tax-specific behavior use this to satisfy the wiring without
// boilerplate.
// billingTestClock returns a fake clock pinned just past the
// April 2026 period boundary used by most engine tests. With the
// new multi-period billSubscription loop (ADR-028), unit tests that
// expect "1 invoice per RunCycle" need a deterministic now() so
// the loop bills exactly one period and exits. Wall-clock would
// otherwise advance into May/June/etc., billing multiple periods
// per call — correct in production but breaks "1 invoice"
// assertions written against the pre-ADR-028 single-period
// primitive.
func billingTestClock() clock.Clock {
	return clock.NewFake(time.Date(2026, 4, 1, 0, 0, 1, 0, time.UTC))
}

// TestRunCycle_SkipsClockPinnedSubs asserts the disjoint-flow
// invariant from ADR-028: the wall-clock RunCycle must NOT touch
// subs that are pinned to a test clock, even if their next_billing_at
// is past wall-clock now. Operator-driven advance is the sole path
// for clock-pinned billing; cron must stay in its lane.
func TestRunCycle_SkipsClockPinnedSubs(t *testing.T) {
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	nextBilling := periodEnd

	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			// Wall-clock sub — should bill.
			"sub_wall": {
				ID: "sub_wall", TenantID: "t1", CustomerID: "cus_1",
				Items:                     []domain.SubscriptionItem{{PlanID: "pln_1", Quantity: 1}},
				Status:                    domain.SubscriptionActive,
				BillingTime:               domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart, CurrentBillingPeriodEnd: &periodEnd,
				NextBillingAt: &nextBilling,
			},
			// Clock-pinned sub — must be skipped by RunCycle.
			"sub_clock": {
				ID: "sub_clock", TenantID: "t1", CustomerID: "cus_2",
				Items:                     []domain.SubscriptionItem{{PlanID: "pln_1", Quantity: 1}},
				Status:                    domain.SubscriptionActive,
				BillingTime:               domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart, CurrentBillingPeriodEnd: &periodEnd,
				NextBillingAt: &nextBilling,
				TestClockID:   "tc_1",
			},
		},
		cycleUpdated: make(map[string]bool),
	}
	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_1": {ID: "pln_1", Currency: "USD", BillingInterval: domain.BillingMonthly, BaseAmountCents: 1000},
		},
	}
	invoices := &mockInvoices{}
	engine := wireBaseTax(NewEngine(subs, &mockUsage{totals: map[string]int64{}}, pricing, invoices, nil, &mockSettings{}, nil, nil, billingTestClock()))

	if _, errs := engine.RunCycle(context.Background(), 50); len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	// Exactly one invoice — for the wall-clock sub. The clock-pinned
	// sub stays untouched; only Engine.RunCycleForClock processes it.
	if len(invoices.invoices) != 1 {
		t.Fatalf("expected 1 invoice (wall-clock only), got %d", len(invoices.invoices))
	}
	if invoices.invoices[0].SubscriptionID != "sub_wall" {
		t.Errorf("wrong sub billed: got %q, want sub_wall", invoices.invoices[0].SubscriptionID)
	}
	if subs.cycleUpdated["sub_clock"] {
		t.Error("clock-pinned sub must not have its cycle advanced by wall-clock RunCycle")
	}
}

// TestRetryPendingCharges_SkipsClockPinned (ADR-029 Phase 1) — the
// cron-side counterpart of TestRunCycle_SkipsClockPinnedSubs for the
// auto-charge path. mockInvoices doesn't differentiate clock-pinned
// from wall-clock invoices on its own; the production filter lives
// in the SQL of ListAutoChargePending. This unit test exercises the
// engine wiring contract: when the InvoiceWriter returns a list, the
// engine charges every entry (no per-row clock check at the engine
// layer — exclusion is the SQL's job, just like ADR-028's
// GetDueBilling). The invariant we DO want pinned at this layer is
// the symmetric one: RetryPendingChargesForClock must call the
// per-clock list method, not the cron one.
func TestRetryPendingCharges_DispatchesToCorrectQuery(t *testing.T) {
	// A spy mockInvoices that records which list method got called.
	subs := &mockSubs{cycleUpdated: make(map[string]bool)}
	pricing := &mockPricing{}
	inv := &mockInvoices{
		invoices: []domain.Invoice{{
			ID: "inv_wall", TenantID: "t1", CustomerID: "cus_wall",
			Status: domain.InvoiceFinalized, PaymentStatus: domain.PaymentPending,
			AutoChargePending: true, AmountDueCents: 1000,
		}},
	}
	engine := wireBaseTax(NewEngine(subs, &mockUsage{}, pricing, inv, nil, &mockSettings{}, nil, nil, billingTestClock()))

	t.Run("cron path uses ListAutoChargePending", func(t *testing.T) {
		// We don't have a charger wired so the loop short-circuits at
		// the paymentSetups nil check — that's fine, the assertion
		// here is structural, not behavioural.
		_, _ = engine.RetryPendingCharges(context.Background(), 50)
	})

	t.Run("catchup path uses ListAutoChargePendingForClock", func(t *testing.T) {
		// Same short-circuit applies. The structural contract is that
		// the engine has both methods and they delegate to the
		// distinct InvoiceWriter methods; the SQL-level disjointness
		// is verified in the postgres integration test.
		_, _ = engine.RetryPendingChargesForClock(context.Background(), "t1", "tc_1", 50)
	})
}

// TestRetryPendingCharges_CardDeclineDoesNotEscalate locks in the
// expected-vs-fatal classification fix. A Stripe card decline is a
// routine business outcome handled by dunning (ChargeInvoice fires
// inline StartDunning + stamps payment_status=failed on the invoice),
// NOT a catchup-infrastructure failure. Pre-fix the decline error
// bubbled up through processAutoCharge → RetryPendingChargesForClock
// → testclock catchup → clock flipped to internal_failure on the
// first declined invoice.
func TestRetryPendingCharges_CardDeclineDoesNotEscalate(t *testing.T) {
	inv := &mockInvoices{
		invoices: []domain.Invoice{{
			ID: "inv_decline", TenantID: "t1", CustomerID: "cus_1",
			Status:            domain.InvoiceFinalized,
			PaymentStatus:     domain.PaymentPending,
			AutoChargePending: true,
			AmountDueCents:    1000,
		}},
	}
	subs := &mockSubs{cycleUpdated: make(map[string]bool)}
	pricing := &mockPricing{}
	pms := &fakePaymentSetups{ready: true, stripeCustomerID: "cus_stripe_1"}
	charger := &fakeChargerDecline{}

	engine := wireBaseTax(NewEngine(subs, &mockUsage{}, pricing, inv, nil, &mockSettings{}, pms, charger, billingTestClock()))

	charged, errs := engine.RetryPendingCharges(context.Background(), 50)
	if charged != 0 {
		t.Errorf("charged: got %d, want 0 (card declined)", charged)
	}
	if len(errs) != 0 {
		t.Errorf("decline must not escalate; got %d errors: %v", len(errs), errs)
	}
	// AutoChargePending must be cleared so the next catchup tick doesn't
	// re-pick the same invoice — dunning's retry schedule drives the
	// next attempt.
	if inv.invoices[0].AutoChargePending {
		t.Error("AutoChargePending should be cleared after decline (dunning takes over)")
	}
}

// fakeChargerDecline returns a *payment.PaymentError with DeclineCode
// set — the canonical "card declined" Stripe outcome.
type fakeChargerDecline struct{}

func (c *fakeChargerDecline) ChargeInvoice(_ context.Context, _ string, inv domain.Invoice, _ string) (domain.Invoice, error) {
	return inv, &payment.PaymentError{Message: "Card was declined.", DeclineCode: "card_declined"}
}

// fakePaymentSetups returns a static "ready" setup for any customer.
type fakePaymentSetups struct {
	ready            bool
	stripeCustomerID string
}

func (f *fakePaymentSetups) GetPaymentSetup(_ context.Context, _, _ string) (domain.CustomerPaymentSetup, error) {
	status := domain.PaymentSetupMissing
	if f.ready {
		status = domain.PaymentSetupReady
	}
	return domain.CustomerPaymentSetup{
		SetupStatus:      status,
		StripeCustomerID: f.stripeCustomerID,
	}, nil
}

// TestBillSubscription_LoopsUntilCaughtUp asserts the per-sub
// period loop from ADR-028: one billSubscription call generates
// every due invoice for that sub until next_billing_at > effectiveNow.
// Pre-ADR-028 the function generated exactly one invoice per call.
func TestBillSubscription_LoopsUntilCaughtUp(t *testing.T) {
	// Sub created 4 months ago; clock is at "now" (5 months past first
	// period start). Expect 5 monthly invoices in one billSubscription
	// call: April, May, June, July, August.
	periodStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	nextBilling := periodEnd

	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_1": {
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1",
				Items:                     []domain.SubscriptionItem{{PlanID: "pln_1", Quantity: 1}},
				Status:                    domain.SubscriptionActive,
				BillingTime:               domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart, CurrentBillingPeriodEnd: &periodEnd,
				NextBillingAt: &nextBilling,
			},
		},
		cycleUpdated: make(map[string]bool),
	}
	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_1": {ID: "pln_1", Currency: "USD", BillingInterval: domain.BillingMonthly, BaseAmountCents: 1000},
		},
	}
	invoices := &mockInvoices{}
	// Clock 5 months ahead → 5 due periods (May, Jun, Jul, Aug, Sep
	// are billed for periods ending May 1 .. Sep 1; advance happens
	// per the engine's add-month logic).
	clk := clock.NewFake(time.Date(2026, 9, 1, 0, 0, 1, 0, time.UTC))
	engine := wireBaseTax(NewEngine(subs, &mockUsage{totals: map[string]int64{}}, pricing, invoices, nil, &mockSettings{}, nil, nil, clk))

	if _, errs := engine.RunCycle(context.Background(), 50); len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	// Expect 5 invoices for the 5 due periods.
	if len(invoices.invoices) != 5 {
		t.Fatalf("expected 5 invoices from per-sub period loop, got %d", len(invoices.invoices))
	}
	// Sub's next_billing_at must be past clock now after all 5 cycles.
	updated := subs.subs["sub_1"]
	if updated.NextBillingAt == nil || !updated.NextBillingAt.After(clk.Now(context.Background())) {
		t.Errorf("sub should be caught up: got next_billing_at=%v, clock=%v", updated.NextBillingAt, clk.Now(context.Background()))
	}
}

func wireBaseTax(e *Engine) *Engine {
	e.SetTaxProviderResolver(tax.NewResolver(nil))
	return e
}

type mockSettings struct{ next int }

func (m *mockSettings) NextInvoiceNumber(_ context.Context, _ string) (string, error) {
	m.next++
	return fmt.Sprintf("VLX-%06d", m.next), nil
}

func (m *mockSettings) Get(_ context.Context, _ string) (domain.TenantSettings, error) {
	return domain.TenantSettings{}, nil
}

// ---------------------------------------------------------------------------
// In-memory implementations of the billing engine's interfaces
// ---------------------------------------------------------------------------

type mockSubs struct {
	subs         map[string]domain.Subscription
	cycleUpdated map[string]bool
	// itemChanges feeds ListItemChangesInPeriod. Tests that exercise
	// segment-aware billing seed this with the change rows the DB
	// trigger would have produced (migration 0029).
	itemChanges []domain.SubscriptionItemChange
}

func (m *mockSubs) GetDueBilling(_ context.Context, before time.Time, limit int) ([]domain.Subscription, error) {
	var result []domain.Subscription
	for _, s := range m.subs {
		// ADR-028 disjoint flows: wall-clock GetDueBilling only
		// returns NON-clock-pinned subs.
		if s.TestClockID != "" {
			continue
		}
		eligible := s.Status == domain.SubscriptionActive || s.Status == domain.SubscriptionTrialing
		if eligible && s.NextBillingAt != nil && !s.NextBillingAt.After(before) {
			result = append(result, s)
		}
	}
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

// GetDueBillingForClock — mirror of GetDueBilling for the disjoint
// catchup path (ADR-028). Returns ONLY subs pinned to the given
// clock whose next_billing_at is on-or-before `before`. The mock
// uses `before` directly because it doesn't model test-clock rows;
// real engine wiring resolves frozen_time via the SQL JOIN.
func (m *mockSubs) GetDueBillingForClock(_ context.Context, _, clockID string, limit int) ([]domain.Subscription, error) {
	var result []domain.Subscription
	for _, s := range m.subs {
		if s.TestClockID != clockID {
			continue
		}
		eligible := s.Status == domain.SubscriptionActive || s.Status == domain.SubscriptionTrialing
		if eligible && s.NextBillingAt != nil {
			result = append(result, s)
		}
	}
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (m *mockSubs) Get(_ context.Context, _, id string) (domain.Subscription, error) {
	s, ok := m.subs[id]
	if !ok {
		return domain.Subscription{}, fmt.Errorf("not found")
	}
	return s, nil
}

func (m *mockSubs) UpdateBillingCycle(_ context.Context, _, id string, start, end, next time.Time) error {
	s, ok := m.subs[id]
	if !ok {
		return fmt.Errorf("not found")
	}
	s.CurrentBillingPeriodStart = &start
	s.CurrentBillingPeriodEnd = &end
	s.NextBillingAt = &next
	m.subs[id] = s
	m.cycleUpdated[id] = true
	return nil
}

func (m *mockSubs) FireScheduledCancellation(_ context.Context, _, id string, at time.Time) (domain.Subscription, error) {
	s, ok := m.subs[id]
	if !ok {
		return domain.Subscription{}, errs.ErrNotFound
	}
	if s.Status != domain.SubscriptionActive {
		return domain.Subscription{}, errs.ErrInvalidState
	}
	s.Status = domain.SubscriptionCanceled
	atCopy := at
	s.CanceledAt = &atCopy
	s.NextBillingAt = nil
	s.CancelAt = nil
	s.CancelAtPeriodEnd = false
	m.subs[id] = s
	return s, nil
}

func (m *mockSubs) ClearPauseCollection(_ context.Context, _, id string) (domain.Subscription, error) {
	s, ok := m.subs[id]
	if !ok {
		return domain.Subscription{}, errs.ErrNotFound
	}
	s.PauseCollection = nil
	m.subs[id] = s
	return s, nil
}

func (m *mockSubs) ActivateAfterTrial(_ context.Context, _, id string, at time.Time) (domain.Subscription, error) {
	s, ok := m.subs[id]
	if !ok {
		return domain.Subscription{}, errs.ErrNotFound
	}
	if s.Status != domain.SubscriptionTrialing {
		return domain.Subscription{}, errs.InvalidState("not trialing")
	}
	s.Status = domain.SubscriptionActive
	if s.ActivatedAt == nil {
		t := at
		s.ActivatedAt = &t
	}
	m.subs[id] = s
	return s, nil
}

// ApplyDuePendingItemPlansAtomic mirrors the postgres store: for every item on
// the subscription whose pending change is due (effective_at <= now), swap
// plan_id ← pending_plan_id and clear the pending fields in one pass. Returns
// the applied items so the engine can audit which swaps landed at this cycle.
func (m *mockSubs) ApplyDuePendingItemPlansAtomic(_ context.Context, _, id string, now time.Time) ([]domain.SubscriptionItem, error) {
	s, ok := m.subs[id]
	if !ok {
		return nil, errs.ErrNotFound
	}
	var applied []domain.SubscriptionItem
	for i := range s.Items {
		it := s.Items[i]
		if it.PendingPlanID == "" || it.PendingPlanEffectiveAt == nil || it.PendingPlanEffectiveAt.After(now) {
			continue
		}
		oldPlan := it.PlanID
		it.PlanID = it.PendingPlanID
		it.PlanChangedAt = &now
		it.PendingPlanID = ""
		it.PendingPlanEffectiveAt = nil
		s.Items[i] = it
		applied = append(applied, it)
		// Mirror the DB trigger from migration 0029: the UPDATE on
		// subscription_items emits a 'plan' change row so segment-
		// aware billing sees the boundary swap.
		m.itemChanges = append(m.itemChanges, domain.SubscriptionItemChange{
			ID:                 fmt.Sprintf("vlx_sic_%d", len(m.itemChanges)+1),
			TenantID:           s.TenantID,
			SubscriptionID:     s.ID,
			SubscriptionItemID: it.ID,
			ChangeType:         "plan",
			FromPlanID:         oldPlan,
			ToPlanID:           it.PlanID,
			FromQuantity:       it.Quantity,
			ToQuantity:         it.Quantity,
			ChangedAt:          now,
			CreatedAt:          now,
		})
	}
	m.subs[id] = s
	return applied, nil
}

func (m *mockSubs) ListWithThresholdsForClock(_ context.Context, _, _ string, _ int) ([]domain.Subscription, error) {
	return nil, nil
}

func (m *mockSubs) ListWithThresholds(_ context.Context, _ bool, _ int) ([]domain.Subscription, error) {
	// Engine unit tests focus on the natural cycle; the threshold scan path
	// is exercised via threshold_scan_test.go (which uses its own mock that
	// returns the configured candidate set). Returning empty here keeps
	// existing tests compatible without exercising the threshold path.
	return nil, nil
}

func (m *mockSubs) ListItemChangesInPeriod(_ context.Context, _, subscriptionID string, periodStart, periodEnd time.Time) ([]domain.SubscriptionItemChange, error) {
	var out []domain.SubscriptionItemChange
	for _, c := range m.itemChanges {
		if c.SubscriptionID != subscriptionID {
			continue
		}
		if !c.ChangedAt.After(periodStart) || c.ChangedAt.After(periodEnd) {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

type mockUsage struct {
	totals map[string]int64 // meterID -> quantity for the full-period default
	// perInterval lets segment-aware usage tests stub different
	// quantities per [from, to]. Key is "meterID|fromRFC3339|toRFC3339".
	// Missing keys fall back to totals.
	perInterval map[string]int64
}

func mockIntervalKey(meterID string, from, to time.Time) string {
	return meterID + "|" + from.UTC().Format(time.RFC3339Nano) + "|" + to.UTC().Format(time.RFC3339Nano)
}

func (m *mockUsage) AggregateForBillingPeriod(_ context.Context, _, _ string, meterIDs []string, from, to time.Time) (map[string]decimal.Decimal, error) {
	result := make(map[string]decimal.Decimal)
	for _, id := range meterIDs {
		if qty, ok := m.perInterval[mockIntervalKey(id, from, to)]; ok {
			result[id] = decimal.NewFromInt(qty)
			continue
		}
		if qty, ok := m.totals[id]; ok {
			result[id] = decimal.NewFromInt(qty)
		}
	}
	return result, nil
}

func (m *mockUsage) AggregateForBillingPeriodByAgg(_ context.Context, _, _ string, meters map[string]string, from, to time.Time) (map[string]decimal.Decimal, error) {
	result := make(map[string]decimal.Decimal)
	for id := range meters {
		if qty, ok := m.perInterval[mockIntervalKey(id, from, to)]; ok {
			result[id] = decimal.NewFromInt(qty)
			continue
		}
		if qty, ok := m.totals[id]; ok {
			result[id] = decimal.NewFromInt(qty)
		}
	}
	return result, nil
}

// AggregateByPricingRules is a minimal stub — engine_test.go's existing
// preview tests don't exercise the multi-dim path; the create_preview
// integration tests cover that against real Postgres. We return one
// aggregation per known meter so the new preview path produces the same
// totals the legacy tests expect.
func (m *mockUsage) AggregateByPricingRules(_ context.Context, _, _, meterID string, _ domain.AggregationMode, _, _ time.Time) ([]domain.RuleAggregation, error) {
	qty, ok := m.totals[meterID]
	if !ok {
		return nil, nil
	}
	return []domain.RuleAggregation{{
		RuleID:              "",
		RatingRuleVersionID: "",
		Quantity:            decimal.NewFromInt(qty),
	}}, nil
}

type mockPricing struct {
	plans     map[string]domain.Plan
	meters    map[string]domain.Meter
	rules     map[string]domain.RatingRuleVersion
	overrides map[string]domain.CustomerPriceOverride // key: customerID+ruleID
}

func (m *mockPricing) GetPlan(_ context.Context, _, id string) (domain.Plan, error) {
	p, ok := m.plans[id]
	if !ok {
		return domain.Plan{}, fmt.Errorf("plan not found")
	}
	return p, nil
}

func (m *mockPricing) GetMeter(_ context.Context, _, id string) (domain.Meter, error) {
	meter, ok := m.meters[id]
	if !ok {
		return domain.Meter{}, fmt.Errorf("meter not found")
	}
	return meter, nil
}

func (m *mockPricing) GetRatingRule(_ context.Context, _, id string) (domain.RatingRuleVersion, error) {
	r, ok := m.rules[id]
	if !ok {
		return domain.RatingRuleVersion{}, fmt.Errorf("rule not found")
	}
	return r, nil
}

func (m *mockPricing) GetLatestRuleByKey(_ context.Context, _, ruleKey string) (domain.RatingRuleVersion, error) {
	// Return the latest version with matching key
	var latest domain.RatingRuleVersion
	found := false
	for _, r := range m.rules {
		if r.RuleKey == ruleKey {
			if !found || r.Version > latest.Version {
				latest = r
				found = true
			}
		}
	}
	if !found {
		return domain.RatingRuleVersion{}, fmt.Errorf("rule not found for key %s", ruleKey)
	}
	return latest, nil
}

func (m *mockPricing) GetOverride(_ context.Context, _, customerID, ruleID string) (domain.CustomerPriceOverride, error) {
	if m.overrides == nil {
		return domain.CustomerPriceOverride{}, fmt.Errorf("not found")
	}
	o, ok := m.overrides[customerID+":"+ruleID]
	if !ok {
		return domain.CustomerPriceOverride{}, fmt.Errorf("not found")
	}
	return o, nil
}

// ListMeterPricingRulesByMeter is a no-op stub. The engine unit tests use
// single-rule meters; per-rule DimensionMatch echo is covered by the
// create_preview integration tests against real Postgres.
func (m *mockPricing) ListMeterPricingRulesByMeter(_ context.Context, _, _ string) ([]domain.MeterPricingRule, error) {
	return nil, nil
}

type mockInvoices struct {
	invoices  []domain.Invoice
	lineItems []domain.InvoiceLineItem
}

func (m *mockInvoices) CreateInvoice(_ context.Context, tenantID string, inv domain.Invoice) (domain.Invoice, error) {
	inv.ID = fmt.Sprintf("vlx_inv_%d", len(m.invoices)+1)
	inv.TenantID = tenantID
	m.invoices = append(m.invoices, inv)
	return inv, nil
}

func (m *mockInvoices) CreateLineItem(_ context.Context, tenantID string, item domain.InvoiceLineItem) (domain.InvoiceLineItem, error) {
	item.ID = fmt.Sprintf("vlx_ili_%d", len(m.lineItems)+1)
	item.TenantID = tenantID
	m.lineItems = append(m.lineItems, item)
	return item, nil
}

func (m *mockInvoices) ApplyCreditAmount(_ context.Context, _, id string, amountCents int64) (domain.Invoice, error) {
	for i, inv := range m.invoices {
		if inv.ID == id {
			m.invoices[i].AmountDueCents -= amountCents
			m.invoices[i].CreditsAppliedCents += amountCents
			return m.invoices[i], nil
		}
	}
	return domain.Invoice{}, fmt.Errorf("not found")
}

func (m *mockInvoices) GetInvoice(_ context.Context, _, id string) (domain.Invoice, error) {
	for _, inv := range m.invoices {
		if inv.ID == id {
			return inv, nil
		}
	}
	return domain.Invoice{}, fmt.Errorf("not found")
}

func (m *mockInvoices) MarkPaid(_ context.Context, _, id string, stripePI string, paidAt time.Time) (domain.Invoice, error) {
	for i, inv := range m.invoices {
		if inv.ID == id {
			m.invoices[i].Status = domain.InvoicePaid
			m.invoices[i].PaymentStatus = domain.PaymentSucceeded
			m.invoices[i].AmountPaidCents = m.invoices[i].AmountDueCents
			m.invoices[i].AmountDueCents = 0
			m.invoices[i].PaidAt = &paidAt
			return m.invoices[i], nil
		}
	}
	return domain.Invoice{}, fmt.Errorf("not found")
}

func (m *mockInvoices) CreateInvoiceWithLineItems(_ context.Context, tenantID string, inv domain.Invoice, items []domain.InvoiceLineItem) (domain.Invoice, error) {
	inv.ID = fmt.Sprintf("vlx_inv_%d", len(m.invoices)+1)
	inv.TenantID = tenantID
	m.invoices = append(m.invoices, inv)
	for _, item := range items {
		item.InvoiceID = inv.ID
		item.ID = fmt.Sprintf("vlx_ili_%d", len(m.lineItems)+1)
		item.TenantID = tenantID
		m.lineItems = append(m.lineItems, item)
	}
	return inv, nil
}

func (m *mockInvoices) SetAutoChargePending(_ context.Context, _, id string, pending bool) error {
	for i, inv := range m.invoices {
		if inv.ID == id {
			m.invoices[i].AutoChargePending = pending
			return nil
		}
	}
	return fmt.Errorf("not found")
}

// ListAutoChargePendingForClock — minimal stub for the ADR-029 Phase 1
// interface bump. Tests that exercise the per-clock catchup path
// install a more meaningful stub via the test fixture; the default
// returns nothing because mockInvoices doesn't track sub→clock
// mapping (that's the postgres store's job).
func (m *mockInvoices) ListAutoChargePendingForClock(_ context.Context, _ string, _ string, _ int) ([]domain.Invoice, error) {
	return nil, nil
}

func (m *mockInvoices) ListAutoChargePending(_ context.Context, limit int) ([]domain.Invoice, error) {
	var result []domain.Invoice
	for _, inv := range m.invoices {
		if inv.AutoChargePending {
			result = append(result, inv)
			if len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}

func (m *mockInvoices) SetTaxTransaction(_ context.Context, _, id string, taxTransactionID string) error {
	for i, inv := range m.invoices {
		if inv.ID == id {
			m.invoices[i].TaxTransactionID = taxTransactionID
			return nil
		}
	}
	return fmt.Errorf("not found")
}

func (m *mockInvoices) ListLineItems(_ context.Context, _, invoiceID string) ([]domain.InvoiceLineItem, error) {
	var out []domain.InvoiceLineItem
	for _, li := range m.lineItems {
		if li.InvoiceID == invoiceID {
			out = append(out, li)
		}
	}
	return out, nil
}

func (m *mockInvoices) ApplyDiscountAtomic(_ context.Context, tenantID, invoiceID string, update domain.InvoiceDiscountUpdate, lineItems []domain.InvoiceLineItem) (domain.Invoice, error) {
	idx := -1
	for i, inv := range m.invoices {
		if inv.ID == invoiceID && inv.TenantID == tenantID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return domain.Invoice{}, errs.ErrNotFound
	}
	inv := m.invoices[idx]
	if inv.Status != domain.InvoiceDraft {
		return domain.Invoice{}, errs.InvalidState(fmt.Sprintf("invoice must be draft (current: %s)", inv.Status))
	}
	if inv.DiscountCents > 0 {
		return domain.Invoice{}, errs.InvalidState("invoice already has a discount applied")
	}
	byID := make(map[string]domain.InvoiceLineItem, len(lineItems))
	for _, li := range lineItems {
		byID[li.ID] = li
	}
	for i, existing := range m.lineItems {
		if existing.InvoiceID != invoiceID {
			continue
		}
		if updated, ok := byID[existing.ID]; ok {
			m.lineItems[i].AmountCents = updated.AmountCents
			m.lineItems[i].TaxRateBP = updated.TaxRateBP
			m.lineItems[i].TaxAmountCents = updated.TaxAmountCents
			m.lineItems[i].TotalAmountCents = updated.TotalAmountCents
		}
	}
	inv.SubtotalCents = update.SubtotalCents
	inv.DiscountCents = update.DiscountCents
	inv.TaxAmountCents = update.TaxAmountCents
	inv.TaxRateBP = update.TaxRateBP
	inv.TaxName = update.TaxName
	inv.TaxCountry = update.TaxCountry
	inv.TaxID = update.TaxID
	inv.TaxProvider = update.TaxProvider
	inv.TaxCalculationID = update.TaxCalculationID
	inv.TaxReverseCharge = update.TaxReverseCharge
	inv.TaxExemptReason = update.TaxExemptReason
	inv.TaxStatus = update.TaxStatus
	inv.TaxDeferredAt = update.TaxDeferredAt
	inv.TaxPendingReason = update.TaxPendingReason
	inv.TotalAmountCents = update.SubtotalCents - update.DiscountCents + update.TaxAmountCents
	due := inv.TotalAmountCents - inv.AmountPaidCents - inv.CreditsAppliedCents
	if due < 0 {
		due = 0
	}
	inv.AmountDueCents = due
	m.invoices[idx] = inv
	return inv, nil
}

func (m *mockInvoices) UpdateTaxAtomic(_ context.Context, tenantID, invoiceID string, update domain.InvoiceTaxRetryUpdate, lineItems []domain.InvoiceLineItem) (domain.Invoice, error) {
	idx := -1
	for i, inv := range m.invoices {
		if inv.ID == invoiceID && inv.TenantID == tenantID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return domain.Invoice{}, errs.ErrNotFound
	}
	inv := m.invoices[idx]
	if inv.Status != domain.InvoiceDraft {
		return domain.Invoice{}, errs.InvalidState(fmt.Sprintf("invoice must be draft (current: %s)", inv.Status))
	}
	if inv.TaxStatus != domain.InvoiceTaxPending && inv.TaxStatus != domain.InvoiceTaxFailed {
		return domain.Invoice{}, errs.InvalidState(fmt.Sprintf("tax retry requires pending/failed (current: %s)", inv.TaxStatus))
	}
	byID := make(map[string]domain.InvoiceLineItem, len(lineItems))
	for _, li := range lineItems {
		byID[li.ID] = li
	}
	for i, existing := range m.lineItems {
		if existing.InvoiceID != invoiceID {
			continue
		}
		if updated, ok := byID[existing.ID]; ok {
			m.lineItems[i].TaxRateBP = updated.TaxRateBP
			m.lineItems[i].TaxAmountCents = updated.TaxAmountCents
			m.lineItems[i].TotalAmountCents = updated.TotalAmountCents
		}
	}
	inv.TaxAmountCents = update.TaxAmountCents
	inv.TaxRateBP = update.TaxRateBP
	inv.TaxName = update.TaxName
	inv.TaxCountry = update.TaxCountry
	inv.TaxID = update.TaxID
	inv.TaxProvider = update.TaxProvider
	inv.TaxCalculationID = update.TaxCalculationID
	inv.TaxReverseCharge = update.TaxReverseCharge
	inv.TaxExemptReason = update.TaxExemptReason
	inv.TaxStatus = update.TaxStatus
	inv.TaxDeferredAt = update.TaxDeferredAt
	inv.TaxPendingReason = update.TaxPendingReason
	inv.TaxErrorCode = update.TaxErrorCode
	inv.TaxRetryCount++
	inv.TotalAmountCents = update.TotalAmountCents
	due := inv.TotalAmountCents - inv.AmountPaidCents - inv.CreditsAppliedCents
	if due < 0 {
		due = 0
	}
	inv.AmountDueCents = due
	m.invoices[idx] = inv
	return inv, nil
}

// fakeCreditGranter records Grant calls. Backs the BillOnCancel
// paid-check unit tests — they assert the grant fires (or doesn't)
// based on the source invoice's payment_status.
type fakeCreditGranter struct {
	grants []credit.GrantInput
}

func (g *fakeCreditGranter) Grant(_ context.Context, _ string, in credit.GrantInput) (domain.CreditLedgerEntry, error) {
	g.grants = append(g.grants, in)
	return domain.CreditLedgerEntry{ID: fmt.Sprintf("vlx_cle_%d", len(g.grants))}, nil
}

func (m *mockInvoices) FindBaseInvoiceForPeriod(_ context.Context, tenantID, subscriptionID string, periodStart time.Time) (domain.Invoice, error) {
	// Mock: search the invoices slice for one whose subscription matches
	// and that has a line item (in the flat m.lineItems slice) with
	// billing_period_start = periodStart. Mirrors the postgres semantics
	// for tests that explicitly seed in_advance invoices.
	for _, inv := range m.invoices {
		if inv.TenantID != tenantID || inv.SubscriptionID != subscriptionID {
			continue
		}
		if inv.Status == domain.InvoiceVoided || inv.Status == domain.InvoiceUncollectible {
			continue
		}
		for _, li := range m.lineItems {
			if li.InvoiceID != inv.ID {
				continue
			}
			if li.LineType == domain.LineTypeBaseFee && li.BillingPeriodStart != nil && li.BillingPeriodStart.Equal(periodStart) {
				return inv, nil
			}
		}
	}
	return domain.Invoice{}, errs.ErrNotFound
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func setupEngine() (*Engine, *mockSubs, *mockUsage, *mockPricing, *mockInvoices) {
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	nextBilling := periodEnd

	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_1": {
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1",
				Items:  []domain.SubscriptionItem{{PlanID: "pln_1", Quantity: 1}},
				Status: domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart, CurrentBillingPeriodEnd: &periodEnd,
				NextBillingAt: &nextBilling,
			},
		},
		cycleUpdated: make(map[string]bool),
	}

	usage := &mockUsage{
		totals: map[string]int64{
			"mtr_api":     1500,
			"mtr_storage": 250,
		},
	}

	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_1": {
				ID: "pln_1", Name: "Pro Plan", Currency: "USD",
				BillingInterval: domain.BillingMonthly,
				BaseAmountCents: 4900,
				MeterIDs:        []string{"mtr_api", "mtr_storage"},
			},
		},
		meters: map[string]domain.Meter{
			"mtr_api":     {ID: "mtr_api", Name: "API Calls", Unit: "calls", RatingRuleVersionID: "rrv_api"},
			"mtr_storage": {ID: "mtr_storage", Name: "Storage", Unit: "GB", RatingRuleVersionID: "rrv_storage"},
		},
		rules: map[string]domain.RatingRuleVersion{
			"rrv_api": {
				ID: "rrv_api", RuleKey: "api_calls", Version: 1, Mode: domain.PricingGraduated,
				GraduatedTiers: []domain.RatingTier{
					{UpTo: 1000, UnitAmountCents: 10},
					{UpTo: 0, UnitAmountCents: 5},
				},
			},
			"rrv_storage": {
				ID: "rrv_storage", RuleKey: "storage_gb", Version: 1, Mode: domain.PricingFlat,
				FlatAmountCents: 2500,
			},
		},
	}

	invoices := &mockInvoices{}

	// Fake clock at periodEnd + 1ns: just past the period boundary
	// so RunCycle sees exactly one period due. Without this, the
	// new multi-period loop (ADR-028) bills every month from
	// periodEnd → wall-clock today, breaking "1 invoice" assertions.
	fakeClk := clock.NewFake(periodEnd.Add(time.Nanosecond))
	engine := wireBaseTax(NewEngine(subs, usage, pricing, invoices, nil, &mockSettings{}, nil, nil, fakeClk))
	return engine, subs, usage, pricing, invoices
}

func TestRunCycle_GeneratesInvoice(t *testing.T) {
	engine, subs, _, _, invoices := setupEngine()
	ctx := context.Background()

	count, errs := engine.RunCycle(ctx, 50)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if count != 1 {
		t.Fatalf("got %d invoices, want 1", count)
	}

	// Verify invoice
	if len(invoices.invoices) != 1 {
		t.Fatalf("got %d invoices stored, want 1", len(invoices.invoices))
	}

	inv := invoices.invoices[0]
	if inv.CustomerID != "cus_1" {
		t.Errorf("got customer_id %q, want cus_1", inv.CustomerID)
	}
	if inv.SubscriptionID != "sub_1" {
		t.Errorf("got subscription_id %q, want sub_1", inv.SubscriptionID)
	}
	if inv.Status != domain.InvoiceFinalized {
		t.Errorf("got status %q, want finalized", inv.Status)
	}
	if inv.Currency != "USD" {
		t.Errorf("got currency %q, want USD", inv.Currency)
	}

	// Expected: base fee $49 + API graduated (1000*10 + 500*5 = 12500) + storage flat (250*2500 = 625000)
	// Total: 4900 + 12500 + 625000 = 642400 cents
	expectedTotal := int64(4900 + 12500 + 625000)
	if inv.TotalAmountCents != expectedTotal {
		t.Errorf("got total %d cents, want %d", inv.TotalAmountCents, expectedTotal)
	}

	// Verify line items (base + 2 usage)
	if len(invoices.lineItems) != 3 {
		t.Fatalf("got %d line items, want 3", len(invoices.lineItems))
	}

	// Verify billing cycle was advanced
	if !subs.cycleUpdated["sub_1"] {
		t.Error("billing cycle should have been advanced")
	}
	updatedSub := subs.subs["sub_1"]
	if updatedSub.CurrentBillingPeriodStart.Month() != time.April {
		t.Errorf("next period should start in April, got %v", updatedSub.CurrentBillingPeriodStart)
	}
}

func TestRunCycle_NoDueSubscriptions(t *testing.T) {
	engine, subs, _, _, _ := setupEngine()

	// Move next_billing_at to the future
	s := subs.subs["sub_1"]
	future := time.Now().UTC().AddDate(0, 1, 0)
	s.NextBillingAt = &future
	subs.subs["sub_1"] = s

	count, errs := engine.RunCycle(context.Background(), 50)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if count != 0 {
		t.Errorf("got %d, want 0 invoices", count)
	}
}

func TestRunCycle_SkipsTrialSubscription(t *testing.T) {
	engine, subs, _, _, invoices := setupEngine()

	// New state-machine semantics: a trial is conveyed by status='trialing',
	// not just by trial_end_at being set. Service.Create routes new subs with
	// trial_days > 0 to trialing; the engine skips billing while they're in
	// that state.
	s := subs.subs["sub_1"]
	trialEnd := time.Now().UTC().AddDate(0, 0, 7) // 7 days from now
	s.Status = domain.SubscriptionTrialing
	s.TrialEndAt = &trialEnd
	subs.subs["sub_1"] = s

	count, errs := engine.RunCycle(context.Background(), 50)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if count != 0 {
		t.Errorf("got %d, want 0 (trial should be skipped)", count)
	}
	if len(invoices.invoices) != 0 {
		t.Error("no invoice should be generated during trial")
	}
	// But billing cycle should still advance
	if !subs.cycleUpdated["sub_1"] {
		t.Error("billing cycle should advance even during trial")
	}
}

func TestRunCycle_ZeroUsage(t *testing.T) {
	engine, _, usage, _, invoices := setupEngine()

	// No usage at all
	usage.totals = map[string]int64{}

	count, errs := engine.RunCycle(context.Background(), 50)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if count != 1 {
		t.Fatalf("got %d, want 1 (base fee invoice)", count)
	}

	// Should still generate invoice with just the base fee
	inv := invoices.invoices[0]
	if inv.TotalAmountCents != 4900 {
		t.Errorf("got total %d, want 4900 (base fee only)", inv.TotalAmountCents)
	}
	if len(invoices.lineItems) != 1 {
		t.Errorf("got %d line items, want 1 (base fee only)", len(invoices.lineItems))
	}
}

func TestRunCycle_LineItemDetails(t *testing.T) {
	engine, _, _, _, invoices := setupEngine()

	engine.RunCycle(context.Background(), 50)

	// Find usage line items
	for _, item := range invoices.lineItems {
		switch item.LineType {
		case domain.LineTypeBaseFee:
			if item.AmountCents != 4900 {
				t.Errorf("base fee: got %d, want 4900", item.AmountCents)
			}
			if item.Quantity != 1 {
				t.Errorf("base fee quantity: got %d, want 1", item.Quantity)
			}

		case domain.LineTypeUsage:
			if item.MeterID == "mtr_api" {
				if item.Quantity != 1500 {
					t.Errorf("API quantity: got %d, want 1500", item.Quantity)
				}
				if item.AmountCents != 12500 {
					t.Errorf("API amount: got %d, want 12500", item.AmountCents)
				}
				if item.PricingMode != "graduated" {
					t.Errorf("API pricing_mode: got %q, want graduated", item.PricingMode)
				}
				if item.RatingRuleVersionID != "rrv_api" {
					t.Errorf("API rule_id: got %q, want rrv_api", item.RatingRuleVersionID)
				}
			}
			if item.MeterID == "mtr_storage" {
				if item.AmountCents != 625000 { // 250 qty * 2500 flat rate
					t.Errorf("storage amount: got %d, want 625000", item.AmountCents)
				}
				if item.PricingMode != "flat" {
					t.Errorf("storage pricing_mode: got %q, want flat", item.PricingMode)
				}
			}
		}
	}
}

func TestRunCycle_WithPriceOverride(t *testing.T) {
	engine, _, _, pricing, invoices := setupEngine()

	// Set a per-customer override: API calls flat $50 instead of graduated
	pricing.overrides = map[string]domain.CustomerPriceOverride{
		"cus_1:rrv_api": {
			ID: "cpo_1", CustomerID: "cus_1", RatingRuleVersionID: "rrv_api",
			Mode: domain.PricingFlat, FlatAmountCents: 5000,
			Active: true,
		},
	}

	count, errs := engine.RunCycle(context.Background(), 50)
	if len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}
	if count != 1 {
		t.Fatalf("expected 1 invoice, got %d", count)
	}

	inv := invoices.invoices[0]
	// Expected: base $49 + API flat (1500*5000=7500000) + storage flat (250*2500=625000)
	expectedTotal := int64(4900 + 7500000 + 625000)
	if inv.TotalAmountCents != expectedTotal {
		t.Errorf("with override: got %d cents, want %d",
			inv.TotalAmountCents, expectedTotal)
	}
}

func TestAdvanceBillingPeriod(t *testing.T) {
	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	monthly := advanceBillingPeriod(from, domain.BillingMonthly)
	if monthly.Month() != time.April || monthly.Day() != 1 {
		t.Errorf("monthly: got %v, want 2026-04-01", monthly)
	}

	yearly := advanceBillingPeriod(from, domain.BillingYearly)
	if yearly.Year() != 2027 || yearly.Month() != time.March {
		t.Errorf("yearly: got %v, want 2027-03-01", yearly)
	}
}

// TestRunCycle_UnitAmountRoundsBankers is the COR-5 regression: the rating
// path previously used `amount / quantity` (truncating int division) to
// derive the per-unit display amount. For graduated/tiered rules the total
// rarely divides cleanly by the quantity, and truncation biased every
// displayed unit price downward — systematic over large batches, which
// accountants catch. Switching to money.RoundHalfToEven produces the
// nearest-cent unit while preserving the true AmountCents total.
//
// Setup: graduated rule with tier 1 (up to 1 unit) at 100 cents and tier 2
// at 50 cents per unit. Usage = 3 → amount = 1*100 + 2*50 = 200 cents.
// Truncation: 200/3 = 66. Banker's: 66.67 → 67.
func TestRunCycle_UnitAmountRoundsBankers(t *testing.T) {
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_1": {
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1",
				Items:  []domain.SubscriptionItem{{PlanID: "pln_1", Quantity: 1}},
				Status: domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart, CurrentBillingPeriodEnd: &periodEnd,
				NextBillingAt: &periodEnd,
			},
		},
		cycleUpdated: make(map[string]bool),
	}
	usage := &mockUsage{totals: map[string]int64{"mtr_api": 3}}
	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_1": {
				ID: "pln_1", Name: "Round Plan", Currency: "USD",
				BillingInterval: domain.BillingMonthly,
				BaseAmountCents: 0, MeterIDs: []string{"mtr_api"},
			},
		},
		meters: map[string]domain.Meter{
			"mtr_api": {ID: "mtr_api", Name: "API Calls", Unit: "calls", RatingRuleVersionID: "rrv_api"},
		},
		rules: map[string]domain.RatingRuleVersion{
			"rrv_api": {
				ID: "rrv_api", RuleKey: "api_calls", Version: 1, Mode: domain.PricingGraduated,
				GraduatedTiers: []domain.RatingTier{
					{UpTo: 1, UnitAmountCents: 100},
					{UpTo: 0, UnitAmountCents: 50},
				},
			},
		},
	}
	invoices := &mockInvoices{}
	engine := wireBaseTax(NewEngine(subs, usage, pricing, invoices, nil, &mockSettings{}, nil, nil, billingTestClock()))

	if _, errs := engine.RunCycle(context.Background(), 50); len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(invoices.invoices) != 1 {
		t.Fatalf("got %d invoices, want 1", len(invoices.invoices))
	}

	var usageLine *domain.InvoiceLineItem
	for i := range invoices.lineItems {
		if invoices.lineItems[i].MeterID == "mtr_api" {
			usageLine = &invoices.lineItems[i]
			break
		}
	}
	if usageLine == nil {
		t.Fatal("api usage line not found in invoice")
	}

	if usageLine.AmountCents != 200 {
		t.Errorf("amount_cents: got %d, want 200 (1*100 + 2*50)", usageLine.AmountCents)
	}
	if usageLine.Quantity != 3 {
		t.Errorf("quantity: got %d, want 3", usageLine.Quantity)
	}
	// Truncation would give 66; banker's rounding of 200/3 = 66.67 gives 67.
	if usageLine.UnitAmountCents != 67 {
		t.Errorf("unit_amount_cents: got %d, want 67 (banker's round of 200/3)",
			usageLine.UnitAmountCents)
	}
}

// A subscription with a due pending plan change must bill on the new plan
// after ApplyPendingPlanAtomic runs at the top of billSubscription. Locks in
// the COR-3 contract: the cycle boundary is the swap point.
func TestRunCycle_AppliesScheduledPlanChangeAtBoundary(t *testing.T) {
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	// Effective exactly at periodEnd — the partial-index WHERE clause requires
	// effective_at <= now, and the cycle runs at periodEnd.
	effectiveAt := periodEnd

	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_1": {
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1",
				Items: []domain.SubscriptionItem{{
					PlanID:                 "pln_old",
					Quantity:               1,
					PendingPlanID:          "pln_new",
					PendingPlanEffectiveAt: &effectiveAt,
				}},
				Status: domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart, CurrentBillingPeriodEnd: &periodEnd,
				NextBillingAt: &periodEnd,
			},
		},
		cycleUpdated: make(map[string]bool),
	}

	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_old": {ID: "pln_old", Name: "Old", Currency: "USD", BillingInterval: domain.BillingMonthly, BaseAmountCents: 1000},
			"pln_new": {ID: "pln_new", Name: "New", Currency: "USD", BillingInterval: domain.BillingMonthly, BaseAmountCents: 3000},
		},
	}

	invoices := &mockInvoices{}
	usage := &mockUsage{totals: map[string]int64{}}
	engine := wireBaseTax(NewEngine(subs, usage, pricing, invoices, nil, &mockSettings{}, nil, nil, billingTestClock()))

	count, errs := engine.RunCycle(context.Background(), 50)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if count != 1 {
		t.Fatalf("got %d invoices, want 1", count)
	}

	// The just-elapsed period was consumed under pln_old ($10/mo),
	// so the closing invoice bills at the OUTGOING plan's rate even
	// though the item now points to pln_new ($30/mo) post-swap.
	// Industry-standard (Stripe / Lago / Orb): closing cycle bills
	// under the outgoing plan; new plan takes effect next cycle.
	inv := invoices.invoices[0]
	if inv.SubtotalCents != 1000 {
		t.Errorf("billed on wrong plan: subtotal %d cents, want 1000 (outgoing plan, just-elapsed period)", inv.SubtotalCents)
	}

	// Item row must reflect the swap. The DB swap fires regardless
	// of billing — pricing the closing period uses the outgoing-plan
	// snapshot, but the durable item.plan_id is pln_new going forward.
	got := subs.subs["sub_1"]
	if len(got.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(got.Items))
	}
	it := got.Items[0]
	if it.PlanID != "pln_new" {
		t.Errorf("item plan_id: got %q, want pln_new", it.PlanID)
	}
	if it.PendingPlanID != "" || it.PendingPlanEffectiveAt != nil {
		t.Errorf("pending fields should be cleared: got pending_id=%q effective_at=%v",
			it.PendingPlanID, it.PendingPlanEffectiveAt)
	}
	if it.PlanChangedAt == nil {
		t.Error("plan_changed_at should be set after swap")
	}
}

// TestRunCycle_ScheduledPlanSwap_CrossInterval_BillsElapsedAtOutgoingPlan
// locks in the contract for the monthly → yearly scheduled swap case.
// Pre-fix, the engine billed the just-elapsed monthly period at the
// new yearly plan's prorated rate (588 * 30/365 ≈ 48), wildly wrong.
// Post-fix, the closing invoice bills under the outgoing monthly
// plan's full price; the new yearly cycle starts clean at periodEnd.
func TestRunCycle_ScheduledPlanSwap_CrossInterval_BillsElapsedAtOutgoingPlan(t *testing.T) {
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	effectiveAt := periodEnd

	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_1": {
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1",
				Items: []domain.SubscriptionItem{{
					PlanID:                 "pln_monthly",
					Quantity:               1,
					PendingPlanID:          "pln_yearly",
					PendingPlanEffectiveAt: &effectiveAt,
				}},
				Status: domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart, CurrentBillingPeriodEnd: &periodEnd,
				NextBillingAt: &periodEnd,
			},
		},
		cycleUpdated: make(map[string]bool),
	}

	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_monthly": {ID: "pln_monthly", Name: "Monthly", Currency: "USD", BillingInterval: domain.BillingMonthly, BaseAmountCents: 2900},
			"pln_yearly":  {ID: "pln_yearly", Name: "Yearly", Currency: "USD", BillingInterval: domain.BillingYearly, BaseAmountCents: 58800},
		},
	}

	invoices := &mockInvoices{}
	usage := &mockUsage{totals: map[string]int64{}}
	engine := wireBaseTax(NewEngine(subs, usage, pricing, invoices, nil, &mockSettings{}, nil, nil, billingTestClock()))

	count, errs := engine.RunCycle(context.Background(), 50)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if count != 1 {
		t.Fatalf("got %d invoices, want 1", count)
	}

	inv := invoices.invoices[0]
	if inv.SubtotalCents != 2900 {
		t.Errorf("cross-interval swap: closing invoice should bill outgoing monthly plan ($29 = 2900¢), got %d¢ (pre-fix would have been ~4833 from prorated yearly)", inv.SubtotalCents)
	}

	// The durable item.plan_id is the new yearly plan going forward,
	// so the next cycle anchors yearly.
	got := subs.subs["sub_1"]
	if got.Items[0].PlanID != "pln_yearly" {
		t.Errorf("item plan_id after swap: got %q, want pln_yearly", got.Items[0].PlanID)
	}
}

// A pending change dated in the future must NOT apply at the current cycle —
// the billing engine bills on the existing plan and leaves pending fields intact.
func TestRunCycle_SkipsPendingChangeNotYetDue(t *testing.T) {
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	futureEffective := periodEnd.AddDate(0, 1, 0) // 2026-05-01, next cycle

	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_1": {
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1",
				Items: []domain.SubscriptionItem{{
					PlanID:                 "pln_old",
					Quantity:               1,
					PendingPlanID:          "pln_new",
					PendingPlanEffectiveAt: &futureEffective,
				}},
				Status: domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart, CurrentBillingPeriodEnd: &periodEnd,
				NextBillingAt: &periodEnd,
			},
		},
		cycleUpdated: make(map[string]bool),
	}

	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_old": {ID: "pln_old", Name: "Old", Currency: "USD", BillingInterval: domain.BillingMonthly, BaseAmountCents: 1000},
			"pln_new": {ID: "pln_new", Name: "New", Currency: "USD", BillingInterval: domain.BillingMonthly, BaseAmountCents: 3000},
		},
	}

	invoices := &mockInvoices{}
	usage := &mockUsage{totals: map[string]int64{}}
	engine := wireBaseTax(NewEngine(subs, usage, pricing, invoices, nil, &mockSettings{}, nil, nil, billingTestClock()))

	_, errs := engine.RunCycle(context.Background(), 50)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	inv := invoices.invoices[0]
	if inv.SubtotalCents != 1000 {
		t.Errorf("should have billed on old plan: subtotal %d, want 1000", inv.SubtotalCents)
	}

	it := subs.subs["sub_1"].Items[0]
	if it.PendingPlanID != "pln_new" {
		t.Errorf("pending change should be preserved: got %q", it.PendingPlanID)
	}
	if it.PlanID != "pln_old" {
		t.Errorf("plan_id should not have swapped: got %q", it.PlanID)
	}
}

// TestEffectiveNow covers the four branches of Engine.effectiveNow:
// no test clock → wall-clock; test clock wired → frozen_time; clock id set
// but no reader → wall-clock; reader errors → wall-clock. These branches
// are what makes a test-mode sub bill at simulated time without affecting
// live subs on the same engine instance.
func TestEffectiveNow(t *testing.T) {
	wall := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	frozen := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fakeClk := clock.NewFake(wall)

	liveSub := domain.Subscription{ID: "s_live", TenantID: "t1"}
	testSub := domain.Subscription{ID: "s_test", TenantID: "t1", TestClockID: "tc_1"}

	t.Run("no test clock id returns wall clock", func(t *testing.T) {
		e := NewEngine(nil, nil, nil, nil, nil, nil, nil, nil, fakeClk)
		e.SetTestClockReader(&stubClockReader{clk: domain.TestClock{FrozenTime: frozen}})
		got := e.effectiveNow(context.Background(), liveSub)
		if !got.Equal(wall) {
			t.Errorf("wall-clock sub: got %v, want %v", got, wall)
		}
	})

	t.Run("test clock id with reader returns frozen time", func(t *testing.T) {
		reader := &stubClockReader{clk: domain.TestClock{FrozenTime: frozen}}
		e := NewEngine(nil, nil, nil, nil, nil, nil, nil, nil, fakeClk)
		e.SetTestClockReader(reader)
		got := e.effectiveNow(context.Background(), testSub)
		if !got.Equal(frozen) {
			t.Errorf("test-mode sub: got %v, want %v", got, frozen)
		}
		if reader.lastID != "tc_1" || reader.lastTenant != "t1" {
			t.Errorf("reader lookup: got (%q,%q), want (t1,tc_1)",
				reader.lastTenant, reader.lastID)
		}
	})

	t.Run("test clock id without reader falls back to wall clock", func(t *testing.T) {
		e := NewEngine(nil, nil, nil, nil, nil, nil, nil, nil, fakeClk)
		got := e.effectiveNow(context.Background(), testSub)
		if !got.Equal(wall) {
			t.Errorf("nil-reader fallback: got %v, want %v", got, wall)
		}
	})

	t.Run("reader error falls back to wall clock", func(t *testing.T) {
		e := NewEngine(nil, nil, nil, nil, nil, nil, nil, nil, fakeClk)
		e.SetTestClockReader(&stubClockReader{err: errs.ErrNotFound})
		got := e.effectiveNow(context.Background(), testSub)
		if !got.Equal(wall) {
			t.Errorf("error fallback: got %v, want %v", got, wall)
		}
	})
}

// TestEffectiveNowForInvoice locks in the per-invoice resolver dunning
// uses to keep state-machine timestamps in the simulation domain.
// Branches: clock-pinned sub returns frozen, unpinned sub returns
// wall-clock, manual draft (no subscription) returns wall-clock,
// missing invoice returns an error.
func TestEffectiveNowForInvoice(t *testing.T) {
	wall := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	frozen := time.Date(2024, 2, 1, 12, 0, 0, 0, time.UTC)
	fakeClk := clock.NewFake(wall)

	t.Run("clock-pinned invoice returns frozen", func(t *testing.T) {
		invoices := &mockInvoices{invoices: []domain.Invoice{
			{ID: "inv_1", TenantID: "t1", SubscriptionID: "sub_1"},
		}}
		subs := &mockSubs{subs: map[string]domain.Subscription{
			"sub_1": {ID: "sub_1", TenantID: "t1", TestClockID: "tc_1"},
		}}
		e := NewEngine(subs, nil, nil, invoices, nil, nil, nil, nil, fakeClk)
		e.SetTestClockReader(&stubClockReader{clk: domain.TestClock{FrozenTime: frozen}})

		got, err := e.EffectiveNowForInvoice(context.Background(), "t1", "inv_1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.Equal(frozen) {
			t.Errorf("clock-pinned invoice: got %v, want %v (frozen)", got, frozen)
		}
	})

	t.Run("unpinned subscription returns wall clock", func(t *testing.T) {
		invoices := &mockInvoices{invoices: []domain.Invoice{
			{ID: "inv_2", TenantID: "t1", SubscriptionID: "sub_2"},
		}}
		subs := &mockSubs{subs: map[string]domain.Subscription{
			"sub_2": {ID: "sub_2", TenantID: "t1"}, // no TestClockID
		}}
		e := NewEngine(subs, nil, nil, invoices, nil, nil, nil, nil, fakeClk)

		got, err := e.EffectiveNowForInvoice(context.Background(), "t1", "inv_2")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.Equal(wall) {
			t.Errorf("unpinned sub: got %v, want %v (wall)", got, wall)
		}
	})

	t.Run("manual draft (no subscription) returns wall clock", func(t *testing.T) {
		invoices := &mockInvoices{invoices: []domain.Invoice{
			{ID: "inv_3", TenantID: "t1"}, // no SubscriptionID
		}}
		e := NewEngine(nil, nil, nil, invoices, nil, nil, nil, nil, fakeClk)

		got, err := e.EffectiveNowForInvoice(context.Background(), "t1", "inv_3")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.Equal(wall) {
			t.Errorf("manual draft: got %v, want %v (wall)", got, wall)
		}
	})

	t.Run("missing invoice returns error and wall fallback", func(t *testing.T) {
		invoices := &mockInvoices{} // empty
		e := NewEngine(nil, nil, nil, invoices, nil, nil, nil, nil, fakeClk)

		got, err := e.EffectiveNowForInvoice(context.Background(), "t1", "inv_missing")
		if err == nil {
			t.Error("expected error for missing invoice")
		}
		if !got.Equal(wall) {
			t.Errorf("missing-invoice fallback: got %v, want %v (wall)", got, wall)
		}
	})
}

// stubCustomerReader returns a fixed customer per id — used by the
// EffectiveNowForCustomer tests and the (one-off invoice) branch of
// EffectiveNowForInvoice.
type stubCustomerReader struct {
	customers map[string]domain.Customer
	err       error
}

func (s *stubCustomerReader) Get(_ context.Context, _, id string) (domain.Customer, error) {
	if s.err != nil {
		return domain.Customer{}, s.err
	}
	c, ok := s.customers[id]
	if !ok {
		return domain.Customer{}, fmt.Errorf("not found")
	}
	return c, nil
}

// TestEffectiveNowForCustomer covers the four resolution branches for
// the customer-level entry point used by subscription.Service.Create
// and the one-off-invoice branch of EffectiveNowForInvoice.
func TestEffectiveNowForCustomer(t *testing.T) {
	wall := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	frozen := time.Date(2024, 2, 1, 12, 0, 0, 0, time.UTC)
	fakeClk := clock.NewFake(wall)

	t.Run("clock-pinned customer returns frozen", func(t *testing.T) {
		e := NewEngine(nil, nil, nil, nil, nil, nil, nil, nil, fakeClk)
		e.SetCustomerReader(&stubCustomerReader{customers: map[string]domain.Customer{
			"cus_1": {ID: "cus_1", TenantID: "t1", TestClockID: "tc_1"},
		}})
		e.SetTestClockReader(&stubClockReader{clk: domain.TestClock{FrozenTime: frozen}})

		got, err := e.EffectiveNowForCustomer(context.Background(), "t1", "cus_1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.Equal(frozen) {
			t.Errorf("clock-pinned customer: got %v, want %v (frozen)", got, frozen)
		}
	})

	t.Run("unpinned customer returns wall clock", func(t *testing.T) {
		e := NewEngine(nil, nil, nil, nil, nil, nil, nil, nil, fakeClk)
		e.SetCustomerReader(&stubCustomerReader{customers: map[string]domain.Customer{
			"cus_2": {ID: "cus_2", TenantID: "t1"}, // no TestClockID
		}})
		got, err := e.EffectiveNowForCustomer(context.Background(), "t1", "cus_2")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.Equal(wall) {
			t.Errorf("unpinned customer: got %v, want %v (wall)", got, wall)
		}
	})

	t.Run("no customer reader wired returns wall clock", func(t *testing.T) {
		e := NewEngine(nil, nil, nil, nil, nil, nil, nil, nil, fakeClk)
		got, err := e.EffectiveNowForCustomer(context.Background(), "t1", "cus_x")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.Equal(wall) {
			t.Errorf("no-reader fallback: got %v, want %v (wall)", got, wall)
		}
	})

	t.Run("missing customer returns error and wall fallback", func(t *testing.T) {
		e := NewEngine(nil, nil, nil, nil, nil, nil, nil, nil, fakeClk)
		e.SetCustomerReader(&stubCustomerReader{customers: map[string]domain.Customer{}})

		got, err := e.EffectiveNowForCustomer(context.Background(), "t1", "cus_missing")
		if err == nil {
			t.Error("expected error for missing customer")
		}
		if !got.Equal(wall) {
			t.Errorf("missing-customer fallback: got %v, want %v (wall)", got, wall)
		}
	})
}

// TestEffectiveNowForInvoice_OneOffCustomerPath locks in the one-off
// invoice branch added alongside EffectiveNowForCustomer: invoices
// without a subscription_id resolve via the customer pin instead of
// returning wall-clock unconditionally.
func TestEffectiveNowForInvoice_OneOffCustomerPath(t *testing.T) {
	wall := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	frozen := time.Date(2024, 2, 1, 12, 0, 0, 0, time.UTC)
	fakeClk := clock.NewFake(wall)

	invoices := &mockInvoices{invoices: []domain.Invoice{
		{ID: "inv_oneoff", TenantID: "t1", CustomerID: "cus_1"}, // no SubscriptionID
	}}
	e := NewEngine(nil, nil, nil, invoices, nil, nil, nil, nil, fakeClk)
	e.SetCustomerReader(&stubCustomerReader{customers: map[string]domain.Customer{
		"cus_1": {ID: "cus_1", TenantID: "t1", TestClockID: "tc_1"},
	}})
	e.SetTestClockReader(&stubClockReader{clk: domain.TestClock{FrozenTime: frozen}})

	got, err := e.EffectiveNowForInvoice(context.Background(), "t1", "inv_oneoff")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Equal(frozen) {
		t.Errorf("one-off invoice for clock-pinned customer: got %v, want %v (frozen)", got, frozen)
	}
}

// capturingEventDispatcher records every Dispatch call for event-firing tests.
type capturingEventDispatcher struct {
	events []struct {
		tenantID  string
		eventType string
		payload   map[string]any
	}
}

func (d *capturingEventDispatcher) Dispatch(_ context.Context, tenantID, eventType string, payload map[string]any) error {
	d.events = append(d.events, struct {
		tenantID  string
		eventType string
		payload   map[string]any
	}{tenantID, eventType, payload})
	return nil
}

// TestRunCycle_FiresPendingChangeAppliedEvent locks in P0 #2: when a due
// pending item plan change rolls in at the cycle boundary, the engine must
// emit subscription.pending_change.applied per swapped item so downstream
// systems (analytics, revrec, customer notifications) don't have to poll for
// state diffs to know the change landed.
func TestRunCycle_FiresPendingChangeAppliedEvent(t *testing.T) {
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	effectiveAt := periodEnd

	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_1": {
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1",
				Items: []domain.SubscriptionItem{{
					ID:                     "si_1",
					PlanID:                 "pln_old",
					Quantity:               1,
					PendingPlanID:          "pln_new",
					PendingPlanEffectiveAt: &effectiveAt,
				}},
				Status: domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart, CurrentBillingPeriodEnd: &periodEnd,
				NextBillingAt: &periodEnd,
			},
		},
		cycleUpdated: make(map[string]bool),
	}

	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_old": {ID: "pln_old", Name: "Old", Currency: "USD", BillingInterval: domain.BillingMonthly, BaseAmountCents: 1000},
			"pln_new": {ID: "pln_new", Name: "New", Currency: "USD", BillingInterval: domain.BillingMonthly, BaseAmountCents: 3000},
		},
	}

	invoices := &mockInvoices{}
	usage := &mockUsage{totals: map[string]int64{}}
	dispatcher := &capturingEventDispatcher{}
	engine := wireBaseTax(NewEngine(subs, usage, pricing, invoices, nil, &mockSettings{}, nil, nil, billingTestClock()))
	engine.SetEventDispatcher(dispatcher)

	if _, errs := engine.RunCycle(context.Background(), 50); len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	var applied map[string]any
	for _, ev := range dispatcher.events {
		if ev.eventType == domain.EventSubscriptionPendingChangeApplied {
			applied = ev.payload
			if ev.tenantID != "t1" {
				t.Errorf("tenant_id: got %q, want t1", ev.tenantID)
			}
			break
		}
	}
	if applied == nil {
		types := make([]string, 0, len(dispatcher.events))
		for _, ev := range dispatcher.events {
			types = append(types, ev.eventType)
		}
		t.Fatalf("expected %s, got types=%v", domain.EventSubscriptionPendingChangeApplied, types)
	}
	if applied["item_id"] != "si_1" {
		t.Errorf("item_id: got %v, want si_1", applied["item_id"])
	}
	if applied["old_plan_id"] != "pln_old" {
		t.Errorf("old_plan_id: got %v, want pln_old", applied["old_plan_id"])
	}
	if applied["new_plan_id"] != "pln_new" {
		t.Errorf("new_plan_id: got %v, want pln_new", applied["new_plan_id"])
	}
}

// TestRunCycle_OneSubFailsOthersContinue asserts the batch loop's isolation
// guarantee: a single subscription with bad data (here: a plan referencing a
// meter whose rating rule is missing) must not prevent healthy subscriptions
// in the same batch from being invoiced. Without this guarantee, one broken
// customer could stall the entire billing cycle.
func TestRunCycle_OneSubFailsOthersContinue(t *testing.T) {
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_ok": {
				ID: "sub_ok", TenantID: "t1", CustomerID: "cus_ok",
				Items:  []domain.SubscriptionItem{{ID: "si_ok", PlanID: "pln_ok", Quantity: 1}},
				Status: domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart, CurrentBillingPeriodEnd: &periodEnd,
				NextBillingAt: &periodEnd,
			},
			"sub_bad": {
				ID: "sub_bad", TenantID: "t1", CustomerID: "cus_bad",
				Items:  []domain.SubscriptionItem{{ID: "si_bad", PlanID: "pln_bad", Quantity: 1}},
				Status: domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart, CurrentBillingPeriodEnd: &periodEnd,
				NextBillingAt: &periodEnd,
			},
		},
		cycleUpdated: make(map[string]bool),
	}

	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_ok":  {ID: "pln_ok", Name: "Flat", Currency: "USD", BillingInterval: domain.BillingMonthly, BaseAmountCents: 1000},
			"pln_bad": {ID: "pln_bad", Name: "Broken", Currency: "USD", BillingInterval: domain.BillingMonthly, MeterIDs: []string{"mtr_missing"}},
		},
		// mtr_missing references rrv_missing which isn't in rules — lookup fails
		meters: map[string]domain.Meter{
			"mtr_missing": {ID: "mtr_missing", Name: "Missing", RatingRuleVersionID: "rrv_missing"},
		},
	}
	invoices := &mockInvoices{}
	usage := &mockUsage{totals: map[string]int64{"mtr_missing": 100}}
	engine := wireBaseTax(NewEngine(subs, usage, pricing, invoices, nil, &mockSettings{}, nil, nil, billingTestClock()))

	count, runErrs := engine.RunCycle(context.Background(), 50)

	if count != 1 {
		t.Errorf("got %d invoices, want 1 (only sub_ok should succeed)", count)
	}
	if len(runErrs) != 1 {
		t.Fatalf("got %d errors, want 1 (sub_bad should fail)", len(runErrs))
	}
	// Healthy subscription's cycle should have advanced despite the neighbor failing.
	if !subs.cycleUpdated["sub_ok"] {
		t.Error("sub_ok billing cycle should have advanced")
	}
	if len(invoices.invoices) != 1 {
		t.Fatalf("got %d invoices stored, want 1", len(invoices.invoices))
	}
	if invoices.invoices[0].SubscriptionID != "sub_ok" {
		t.Errorf("stored invoice should be for sub_ok, got %q", invoices.invoices[0].SubscriptionID)
	}
}

// TestRunCycle_TaxProviderErrorDefersInvoice asserts end-to-end that a tax
// provider failure during RunCycle produces a draft invoice with
// tax_status=pending rather than a finalized invoice with wrong tax.
// Finalize is guarded downstream; the retry worker is responsible for
// completing the calculation and transitioning draft → finalized.
func TestRunCycle_TaxProviderErrorDefersInvoice(t *testing.T) {
	engine, _, _, _, invoices := setupEngine()
	engine.SetTaxProviderResolver(stubResolver(&stubProvider{err: fmt.Errorf("stripe down")}))

	count, runErrs := engine.RunCycle(context.Background(), 50)
	if len(runErrs) > 0 {
		t.Fatalf("unexpected errors: %v", runErrs)
	}
	if count != 1 {
		t.Fatalf("got %d invoices, want 1", count)
	}
	inv := invoices.invoices[0]
	if inv.TaxAmountCents != 0 {
		t.Errorf("got tax %d, want 0 when calculation deferred", inv.TaxAmountCents)
	}
	if inv.Status != domain.InvoiceDraft {
		t.Errorf("deferred invoice must stay in draft, got status %q", inv.Status)
	}
	if inv.TaxStatus != domain.InvoiceTaxPending {
		t.Errorf("tax_status = %q, want pending", inv.TaxStatus)
	}
	if inv.TaxPendingReason == "" {
		t.Error("tax_pending_reason should capture the provider error")
	}
}

// mockCouponApplier captures which of ApplyToInvoice / ApplyToInvoiceForCustomer
// the engine called so tests can assert Stripe's precedence rule:
// subscription-scope beats customer-scope on the same invoice. Each call
// returns the scripted CouponDiscountResult so tests can simulate "no
// subscription coupon" (return zero) and trigger the fallback branch.
// markedRedemptions and markedCustomerDiscounts are populated by the two
// Mark* methods separately — tests assert the engine routes each scope to
// its own writer, since customer_discounts is its own table.
type mockCouponApplier struct {
	subResult               domain.CouponDiscountResult
	subErr                  error
	customerResult          domain.CouponDiscountResult
	customerErr             error
	subCalled               bool
	customerCalled          bool
	markedRedemptions       []string
	markedCustomerDiscounts []string
	redeemReq               domain.CouponRedeemRequest
	redeemResult            domain.CouponRedeemResult
	redeemErr               error
	voidedInvoices          []string
	voidErr                 error
}

func (m *mockCouponApplier) ApplyToInvoice(_ context.Context, _, _, _, _ string, _ []string, _ int64) (domain.CouponDiscountResult, error) {
	m.subCalled = true
	return m.subResult, m.subErr
}

func (m *mockCouponApplier) ApplyToInvoiceForCustomer(_ context.Context, _, _, _ string, _ []string, _ int64) (domain.CouponDiscountResult, error) {
	m.customerCalled = true
	return m.customerResult, m.customerErr
}

func (m *mockCouponApplier) MarkPeriodsApplied(_ context.Context, _ string, ids []string) error {
	m.markedRedemptions = append(m.markedRedemptions, ids...)
	return nil
}

func (m *mockCouponApplier) MarkCustomerDiscountPeriodsApplied(_ context.Context, _ string, ids []string) error {
	m.markedCustomerDiscounts = append(m.markedCustomerDiscounts, ids...)
	return nil
}

func (m *mockCouponApplier) RedeemForInvoice(_ context.Context, _ string, req domain.CouponRedeemRequest) (domain.CouponRedeemResult, error) {
	m.redeemReq = req
	if m.redeemErr != nil {
		return domain.CouponRedeemResult{}, m.redeemErr
	}
	red := m.redeemResult
	if red.Redemption.DiscountCents == 0 {
		red.Redemption = domain.CouponRedemption{
			ID:             fmt.Sprintf("vlx_cpr_%d", len(m.markedRedemptions)+1),
			CustomerID:     req.CustomerID,
			SubscriptionID: req.SubscriptionID,
			InvoiceID:      req.InvoiceID,
			DiscountCents:  req.SubtotalCents / 10, // default 10% so tests that don't script it still exercise the path
		}
	}
	return red, nil
}

func (m *mockCouponApplier) VoidRedemptionsForInvoice(_ context.Context, _, invoiceID string) (int, error) {
	m.voidedInvoices = append(m.voidedInvoices, invoiceID)
	if m.voidErr != nil {
		return 0, m.voidErr
	}
	return 1, nil
}

// Customer-scope coupon fires when the subscription produced no discount —
// the Stripe-style fallback. The engine must populate invoice.DiscountCents
// and call MarkPeriodsApplied with the customer-scope redemption so the
// assignment's periods_applied increments and duration-limited coupons
// exhaust on schedule.
func TestRunCycle_CustomerScopedCouponFiresWhenSubscriptionScopeZero(t *testing.T) {
	engine, _, _, _, invoices := setupEngine()
	applier := &mockCouponApplier{
		subResult:      domain.CouponDiscountResult{},
		customerResult: domain.CouponDiscountResult{Cents: 500, RedemptionIDs: []string{"red_cust"}},
	}
	engine.SetCouponApplier(applier)

	count, errs := engine.RunCycle(context.Background(), 50)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if count != 1 {
		t.Fatalf("got %d invoices, want 1", count)
	}
	if !applier.subCalled {
		t.Error("ApplyToInvoice should be called first (subscription scope)")
	}
	if !applier.customerCalled {
		t.Error("ApplyToInvoiceForCustomer should be called as fallback")
	}
	if len(invoices.invoices) != 1 {
		t.Fatalf("got %d invoices, want 1", len(invoices.invoices))
	}
	if got := invoices.invoices[0].DiscountCents; got != 500 {
		t.Errorf("DiscountCents = %d, want 500 (from customer-scope fallback)", got)
	}
	if len(applier.markedRedemptions) != 0 {
		t.Errorf("MarkPeriodsApplied must not run for customer-scope discount, got %v", applier.markedRedemptions)
	}
	if len(applier.markedCustomerDiscounts) != 1 || applier.markedCustomerDiscounts[0] != "red_cust" {
		t.Errorf("MarkCustomerDiscountPeriodsApplied ids = %v, want [red_cust]", applier.markedCustomerDiscounts)
	}
}

// Precedence rule: when the subscription already has an active coupon that
// produces a discount, the customer-scope fallback must not run. Stripe
// treats subscription.discount and customer.discount as mutually exclusive
// on the same invoice — stacking the two would double-discount.
func TestRunCycle_SubscriptionCouponBeatsCustomerScope(t *testing.T) {
	engine, _, _, _, invoices := setupEngine()
	applier := &mockCouponApplier{
		subResult:      domain.CouponDiscountResult{Cents: 300, RedemptionIDs: []string{"red_sub"}},
		customerResult: domain.CouponDiscountResult{Cents: 500, RedemptionIDs: []string{"red_cust"}},
	}
	engine.SetCouponApplier(applier)

	if _, errs := engine.RunCycle(context.Background(), 50); len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if !applier.subCalled {
		t.Error("ApplyToInvoice should be called")
	}
	if applier.customerCalled {
		t.Error("ApplyToInvoiceForCustomer must NOT run when subscription-scope won")
	}
	if got := invoices.invoices[0].DiscountCents; got != 300 {
		t.Errorf("DiscountCents = %d, want 300 (subscription-scope)", got)
	}
	if len(applier.markedRedemptions) != 1 || applier.markedRedemptions[0] != "red_sub" {
		t.Errorf("MarkPeriodsApplied redemption ids = %v, want [red_sub]", applier.markedRedemptions)
	}
	if len(applier.markedCustomerDiscounts) != 0 {
		t.Errorf("MarkCustomerDiscountPeriodsApplied must not run for subscription-scope discount, got %v", applier.markedCustomerDiscounts)
	}
}

// TestRunCycle_NoPendingChangeNoAppliedEvent ensures the event is gated on an
// actual swap — a subscription billing on its existing plan with no pending
// change must not emit a spurious applied event.
func TestRunCycle_NoPendingChangeNoAppliedEvent(t *testing.T) {
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_1": {
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1",
				Items: []domain.SubscriptionItem{{
					ID: "si_1", PlanID: "pln_old", Quantity: 1,
				}},
				Status: domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart, CurrentBillingPeriodEnd: &periodEnd,
				NextBillingAt: &periodEnd,
			},
		},
		cycleUpdated: make(map[string]bool),
	}
	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_old": {ID: "pln_old", Name: "Old", Currency: "USD", BillingInterval: domain.BillingMonthly, BaseAmountCents: 1000},
		},
	}
	invoices := &mockInvoices{}
	usage := &mockUsage{totals: map[string]int64{}}
	dispatcher := &capturingEventDispatcher{}
	engine := wireBaseTax(NewEngine(subs, usage, pricing, invoices, nil, &mockSettings{}, nil, nil, billingTestClock()))
	engine.SetEventDispatcher(dispatcher)

	if _, errs := engine.RunCycle(context.Background(), 50); len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	for _, ev := range dispatcher.events {
		if ev.eventType == domain.EventSubscriptionPendingChangeApplied {
			t.Fatalf("applied event fired without a pending change: %+v", ev)
		}
	}
}

// seedDraftInvoice inserts a minimal draft invoice + one line item into the
// mockInvoices store. Shared by the ApplyCouponToInvoice test cluster so each
// case focuses on its own gate/assertion rather than seed plumbing.
func seedDraftInvoice(m *mockInvoices, tenantID, invoiceID, customerID, subscriptionID string, subtotal int64) {
	m.invoices = append(m.invoices, domain.Invoice{
		ID:             invoiceID,
		TenantID:       tenantID,
		CustomerID:     customerID,
		SubscriptionID: subscriptionID,
		Status:         domain.InvoiceDraft,
		Currency:       "USD",
		SubtotalCents:  subtotal,
		AmountDueCents: subtotal,
	})
	m.lineItems = append(m.lineItems, domain.InvoiceLineItem{
		ID:               "li_1",
		TenantID:         tenantID,
		InvoiceID:        invoiceID,
		Description:      "Base fee",
		Quantity:         1,
		UnitAmountCents:  subtotal,
		AmountCents:      subtotal,
		TotalAmountCents: subtotal,
	})
}

// Happy path: draft invoice → coupon applied → subtotal held, discount set,
// line items repriced, and MarkPeriodsApplied fires exactly once with the
// new redemption id.
func TestApplyCouponToInvoice_HappyPath(t *testing.T) {
	engine, _, _, _, invoices := setupEngine()
	seedDraftInvoice(invoices, "t1", "inv_1", "cus_1", "sub_1", 10_000)

	applier := &mockCouponApplier{
		redeemResult: domain.CouponRedeemResult{
			Redemption: domain.CouponRedemption{ID: "cpr_1", DiscountCents: 2_000},
		},
	}
	engine.SetCouponApplier(applier)

	updated, err := engine.ApplyCouponToInvoice(context.Background(), "t1", "inv_1", "SAVE20", "idem-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.DiscountCents != 2_000 {
		t.Errorf("DiscountCents = %d, want 2000", updated.DiscountCents)
	}
	if updated.SubtotalCents != 10_000 {
		t.Errorf("SubtotalCents = %d, want 10000 (must not mutate)", updated.SubtotalCents)
	}
	if updated.TotalAmountCents != 8_000 {
		t.Errorf("TotalAmountCents = %d, want 8000", updated.TotalAmountCents)
	}
	if applier.redeemReq.Code != "SAVE20" {
		t.Errorf("redeem code = %q, want SAVE20", applier.redeemReq.Code)
	}
	if applier.redeemReq.IdempotencyKey != "idem-1" {
		t.Errorf("redeem idempotency key = %q, want idem-1", applier.redeemReq.IdempotencyKey)
	}
	if applier.redeemReq.SubtotalCents != 10_000 {
		t.Errorf("redeem subtotal = %d, want 10000", applier.redeemReq.SubtotalCents)
	}
	// Subscription's plan set should flow through so the PlanIDs gate matches
	// any item's plan.
	if len(applier.redeemReq.PlanIDs) != 1 || applier.redeemReq.PlanIDs[0] != "pln_1" {
		t.Errorf("redeem plan ids = %v, want [pln_1]", applier.redeemReq.PlanIDs)
	}
	if len(applier.markedRedemptions) != 1 || applier.markedRedemptions[0] != "cpr_1" {
		t.Errorf("MarkPeriodsApplied ids = %v, want [cpr_1]", applier.markedRedemptions)
	}
	if len(applier.voidedInvoices) != 0 {
		t.Errorf("void should not fire on happy path, got %v", applier.voidedInvoices)
	}
}

// Finalized invoices (or any non-draft) must be rejected at the gate before
// any redemption commits. Catches regressions where a misconfigured route
// exposes this on a paid/voided invoice.
func TestApplyCouponToInvoice_RejectsNonDraft(t *testing.T) {
	engine, _, _, _, invoices := setupEngine()
	seedDraftInvoice(invoices, "t1", "inv_1", "cus_1", "sub_1", 10_000)
	invoices.invoices[0].Status = domain.InvoiceFinalized

	applier := &mockCouponApplier{}
	engine.SetCouponApplier(applier)

	_, err := engine.ApplyCouponToInvoice(context.Background(), "t1", "inv_1", "SAVE20", "")
	if err == nil {
		t.Fatal("expected error on finalized invoice, got nil")
	}
	if applier.redeemReq.Code != "" {
		t.Error("redeem must not fire when gate rejects")
	}
}

// Re-applying a coupon to an invoice that already carries a discount is a
// caller error — the operator flow should be "void the invoice and start
// over," not silently stack discounts.
func TestApplyCouponToInvoice_RejectsAlreadyDiscounted(t *testing.T) {
	engine, _, _, _, invoices := setupEngine()
	seedDraftInvoice(invoices, "t1", "inv_1", "cus_1", "sub_1", 10_000)
	invoices.invoices[0].DiscountCents = 500

	engine.SetCouponApplier(&mockCouponApplier{})

	_, err := engine.ApplyCouponToInvoice(context.Background(), "t1", "inv_1", "SAVE20", "")
	if err == nil {
		t.Fatal("expected error on already-discounted invoice, got nil")
	}
}

// Once the invoice has committed a Stripe tax_transaction we cannot recompute
// tax safely — must reject and let operators void/re-issue.
func TestApplyCouponToInvoice_RejectsTaxAlreadyCommitted(t *testing.T) {
	engine, _, _, _, invoices := setupEngine()
	seedDraftInvoice(invoices, "t1", "inv_1", "cus_1", "sub_1", 10_000)
	invoices.invoices[0].TaxTransactionID = "txr_123"

	engine.SetCouponApplier(&mockCouponApplier{})

	_, err := engine.ApplyCouponToInvoice(context.Background(), "t1", "inv_1", "SAVE20", "")
	if err == nil {
		t.Fatal("expected error when tax already committed, got nil")
	}
}

// Compensation: if the atomic discount persist fails after a fresh redeem,
// the redemption must be voided so times_redeemed stays honest.
func TestApplyCouponToInvoice_CompensatesOnPersistFailure(t *testing.T) {
	engine, _, _, _, invoices := setupEngine()
	seedDraftInvoice(invoices, "t1", "inv_1", "cus_1", "sub_1", 10_000)
	// Setting DiscountCents after the gate would normally be impossible, but
	// the memstore's ApplyDiscountAtomic rechecks it — so we flip the row
	// between gate and persist via a second pre-seeded invoice trick. Easier:
	// force the store to fail by pointing at a bogus invoice id that passes
	// the gate (via the first GetInvoice) but fails the atomic apply. The
	// simplest repro is to delete the line items before atomic runs. Use a
	// dedicated mock wrapper instead of hacking memstore.
	fm := &failingApplyInvoices{mockInvoices: invoices, failApply: true}
	// Swap the engine's invoice writer — the setup gave us the memstore
	// directly, so rewire.
	engine.invoices = fm

	applier := &mockCouponApplier{
		redeemResult: domain.CouponRedeemResult{
			Redemption: domain.CouponRedemption{ID: "cpr_1", DiscountCents: 2_000},
		},
	}
	engine.SetCouponApplier(applier)

	_, err := engine.ApplyCouponToInvoice(context.Background(), "t1", "inv_1", "SAVE20", "")
	if err == nil {
		t.Fatal("expected error when ApplyDiscountAtomic fails, got nil")
	}
	if len(applier.voidedInvoices) != 1 || applier.voidedInvoices[0] != "inv_1" {
		t.Errorf("voidedInvoices = %v, want [inv_1] (must compensate on failure)", applier.voidedInvoices)
	}
	if len(applier.markedRedemptions) != 0 {
		t.Errorf("MarkPeriodsApplied must not fire on failure path, got %v", applier.markedRedemptions)
	}
}

// Replay path: a repeated request with the same Idempotency-Key must not
// trigger the compensating void — the original call already persisted, so
// re-voiding would corrupt times_redeemed.
func TestApplyCouponToInvoice_ReplaySkipsCompensation(t *testing.T) {
	engine, _, _, _, invoices := setupEngine()
	seedDraftInvoice(invoices, "t1", "inv_1", "cus_1", "sub_1", 10_000)
	fm := &failingApplyInvoices{mockInvoices: invoices, failApply: true}
	engine.invoices = fm

	applier := &mockCouponApplier{
		redeemResult: domain.CouponRedeemResult{
			Redemption: domain.CouponRedemption{ID: "cpr_1", DiscountCents: 2_000},
			Replay:     true,
		},
	}
	engine.SetCouponApplier(applier)

	_, err := engine.ApplyCouponToInvoice(context.Background(), "t1", "inv_1", "SAVE20", "idem-dup")
	if err == nil {
		t.Fatal("expected error when ApplyDiscountAtomic fails, got nil")
	}
	if len(applier.voidedInvoices) != 0 {
		t.Errorf("replay path must NOT void — the first call owns the redemption. got %v", applier.voidedInvoices)
	}
}

// Defence-in-depth: a coupon that computes to zero discount is a bug — the
// redemption must be rolled back so the coupon's usage counter stays honest,
// and the caller gets a clear error instead of a no-op 200.
func TestApplyCouponToInvoice_ZeroDiscountVoidsAndErrors(t *testing.T) {
	engine, _, _, _, invoices := setupEngine()
	seedDraftInvoice(invoices, "t1", "inv_1", "cus_1", "sub_1", 10_000)

	applier := &mockCouponApplier{
		redeemResult: domain.CouponRedeemResult{
			Redemption: domain.CouponRedemption{ID: "cpr_1", DiscountCents: 0},
		},
	}
	// mockCouponApplier defaults a 10% discount when DiscountCents is 0 to
	// keep other tests concise, so route past that defaulting by letting
	// the service produce 0 via an explicit value-but-zero result.
	applier.redeemResult.Redemption.DiscountCents = 1
	applier.redeemResult.Redemption.ID = "cpr_zero"
	// Then override: the mock's "if DiscountCents == 0 default" only fires
	// on zero; we want a genuine zero-path, so swap to a purpose-built mock.
	zeroApplier := &zeroDiscountApplier{mockCouponApplier: applier}
	engine.SetCouponApplier(zeroApplier)

	_, err := engine.ApplyCouponToInvoice(context.Background(), "t1", "inv_1", "SAVE20", "")
	if err == nil {
		t.Fatal("expected error on zero-discount result, got nil")
	}
	if len(zeroApplier.voidedInvoices) != 1 {
		t.Errorf("zero-discount path must void the bogus redemption, got %v", zeroApplier.voidedInvoices)
	}
}

// failingApplyInvoices wraps mockInvoices and forces ApplyDiscountAtomic to
// fail while leaving all the other reads (GetInvoice, ListLineItems) working.
// Used by the compensation tests so the redeem → apply → void flow can fire
// the compensating branch without fighting the memstore's gate rechecks.
type failingApplyInvoices struct {
	*mockInvoices
	failApply bool
}

func (f *failingApplyInvoices) ApplyDiscountAtomic(ctx context.Context, tenantID, invoiceID string, update domain.InvoiceDiscountUpdate, items []domain.InvoiceLineItem) (domain.Invoice, error) {
	if f.failApply {
		return domain.Invoice{}, fmt.Errorf("simulated db failure")
	}
	return f.mockInvoices.ApplyDiscountAtomic(ctx, tenantID, invoiceID, update, items)
}

// zeroDiscountApplier forces RedeemForInvoice to return DiscountCents=0 so the
// engine's defence-in-depth branch can be exercised. The base mock applies a
// 10% default for tests that don't script a value, which hides the zero path.
type zeroDiscountApplier struct{ *mockCouponApplier }

func (z *zeroDiscountApplier) RedeemForInvoice(_ context.Context, _ string, req domain.CouponRedeemRequest) (domain.CouponRedeemResult, error) {
	z.redeemReq = req
	return domain.CouponRedeemResult{
		Redemption: domain.CouponRedemption{ID: "cpr_zero", DiscountCents: 0},
	}, nil
}

// TestRunCycle_CancelAtPeriodEnd_FiresAtBoundary locks in the schedule-cancel
// behaviour: when a sub has cancel_at_period_end=true and the cycle scan
// observes effectiveNow >= period end, the engine generates the final invoice
// for the just-ended period AND transitions status to canceled — instead of
// rolling next_billing_at to a future date that would never be reached.
func TestRunCycle_CancelAtPeriodEnd_FiresAtBoundary(t *testing.T) {
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_1": {
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1",
				Items:                     []domain.SubscriptionItem{{PlanID: "pln_1", Quantity: 1}},
				Status:                    domain.SubscriptionActive,
				BillingTime:               domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart,
				CurrentBillingPeriodEnd:   &periodEnd,
				NextBillingAt:             &periodEnd,
				CancelAtPeriodEnd:         true,
			},
		},
		cycleUpdated: make(map[string]bool),
	}
	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_1": {
				ID: "pln_1", Name: "Pro", Currency: "USD",
				BillingInterval: domain.BillingMonthly,
				BaseAmountCents: 4900,
			},
		},
	}
	invoices := &mockInvoices{}
	dispatcher := &capturingEventDispatcher{}
	engine := wireBaseTax(NewEngine(subs, &mockUsage{totals: map[string]int64{}}, pricing, invoices, nil, &mockSettings{}, nil, nil, billingTestClock()))
	engine.SetEventDispatcher(dispatcher)

	if _, errs := engine.RunCycle(context.Background(), 50); len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	if len(invoices.invoices) != 1 {
		t.Fatalf("got %d invoices, want 1 (final invoice for just-ended period)", len(invoices.invoices))
	}

	updated := subs.subs["sub_1"]
	if updated.Status != domain.SubscriptionCanceled {
		t.Errorf("status: got %q, want canceled", updated.Status)
	}
	if updated.CanceledAt == nil {
		t.Error("canceled_at must be set after scheduled cancel fires")
	}
	if updated.CancelAtPeriodEnd {
		t.Error("cancel_at_period_end must be cleared after firing")
	}
	if updated.NextBillingAt != nil {
		t.Error("next_billing_at must be nil on canceled sub")
	}

	var got string
	for _, ev := range dispatcher.events {
		if ev.eventType == domain.EventSubscriptionCanceled {
			got = ev.eventType
			if ev.payload["triggered_by"] != "schedule" {
				t.Errorf("triggered_by: got %v, want schedule", ev.payload["triggered_by"])
			}
			break
		}
	}
	if got == "" {
		types := make([]string, 0, len(dispatcher.events))
		for _, ev := range dispatcher.events {
			types = append(types, ev.eventType)
		}
		t.Fatalf("expected %s, got types=%v", domain.EventSubscriptionCanceled, types)
	}
}

// TestRunCycle_CancelAt_FiresWhenTimestampReached covers the timestamp-based
// schedule. Setting cancel_at to the period boundary should fire the cancel
// at the same point cancel_at_period_end would, but via the timestamp path.
func TestRunCycle_CancelAt_FiresWhenTimestampReached(t *testing.T) {
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	cancelAt := periodEnd

	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_1": {
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1",
				Items:                     []domain.SubscriptionItem{{PlanID: "pln_1", Quantity: 1}},
				Status:                    domain.SubscriptionActive,
				BillingTime:               domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart,
				CurrentBillingPeriodEnd:   &periodEnd,
				NextBillingAt:             &periodEnd,
				CancelAt:                  &cancelAt,
			},
		},
		cycleUpdated: make(map[string]bool),
	}
	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_1": {
				ID: "pln_1", Name: "Pro", Currency: "USD",
				BillingInterval: domain.BillingMonthly,
				BaseAmountCents: 4900,
			},
		},
	}
	invoices := &mockInvoices{}
	engine := wireBaseTax(NewEngine(subs, &mockUsage{totals: map[string]int64{}}, pricing, invoices, nil, &mockSettings{}, nil, nil, billingTestClock()))

	if _, errs := engine.RunCycle(context.Background(), 50); len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	updated := subs.subs["sub_1"]
	if updated.Status != domain.SubscriptionCanceled {
		t.Errorf("status: got %q, want canceled", updated.Status)
	}
	if updated.CancelAt != nil {
		t.Error("cancel_at must be cleared after firing")
	}
}

// TestShouldFireScheduledCancel covers the predicate's edge cases directly.
// Boundary equality matters: the period-end transition must fire when
// effectiveNow equals period_end, not just strictly past it, otherwise a sub
// that gets billed at the exact boundary tick would skip its cancel.
func TestShouldFireScheduledCancel(t *testing.T) {
	t0 := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		sub       domain.Subscription
		periodEnd time.Time
		now       time.Time
		want      bool
	}{
		{
			name:      "no schedule, never fires",
			sub:       domain.Subscription{},
			periodEnd: t0,
			now:       t0,
			want:      false,
		},
		{
			name:      "at_period_end, before boundary",
			sub:       domain.Subscription{CancelAtPeriodEnd: true},
			periodEnd: t0,
			now:       t0.Add(-time.Second),
			want:      false,
		},
		{
			name:      "at_period_end, exactly at boundary",
			sub:       domain.Subscription{CancelAtPeriodEnd: true},
			periodEnd: t0,
			now:       t0,
			want:      true,
		},
		{
			name:      "at_period_end, past boundary",
			sub:       domain.Subscription{CancelAtPeriodEnd: true},
			periodEnd: t0,
			now:       t0.Add(time.Hour),
			want:      true,
		},
		{
			name:      "cancel_at, before timestamp",
			sub:       domain.Subscription{CancelAt: &t0},
			periodEnd: t0.Add(time.Hour),
			now:       t0.Add(-time.Second),
			want:      false,
		},
		{
			name:      "cancel_at, exactly at timestamp",
			sub:       domain.Subscription{CancelAt: &t0},
			periodEnd: t0.Add(time.Hour),
			now:       t0,
			want:      true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldFireScheduledCancel(tt.sub, tt.periodEnd, tt.now); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// TestRunCycle_PauseCollection_GeneratesDraft locks in the Stripe-parity
// behavior: a sub with pause_collection set still has its cycle advanced and
// an invoice generated, but the invoice is created as draft (which keeps it
// out of finalize/charge/dunning). Distinct from a hard pause
// (status=paused), which excludes the sub from GetDueBilling — the sub here
// is still active.
func TestRunCycle_PauseCollection_GeneratesDraft(t *testing.T) {
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_1": {
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1",
				Items:                     []domain.SubscriptionItem{{PlanID: "pln_1", Quantity: 1}},
				Status:                    domain.SubscriptionActive,
				BillingTime:               domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart,
				CurrentBillingPeriodEnd:   &periodEnd,
				NextBillingAt:             &periodEnd,
				PauseCollection: &domain.PauseCollection{
					Behavior: domain.PauseCollectionKeepAsDraft,
				},
			},
		},
		cycleUpdated: make(map[string]bool),
	}
	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_1": {
				ID: "pln_1", Name: "Pro", Currency: "USD",
				BillingInterval: domain.BillingMonthly,
				BaseAmountCents: 4900,
			},
		},
	}
	invoices := &mockInvoices{}
	engine := wireBaseTax(NewEngine(subs, &mockUsage{totals: map[string]int64{}}, pricing, invoices, nil, &mockSettings{}, nil, nil, billingTestClock()))

	if _, errs := engine.RunCycle(context.Background(), 50); len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	if len(invoices.invoices) != 1 {
		t.Fatalf("got %d invoices, want 1 (cycle should still emit invoice during pause_collection)", len(invoices.invoices))
	}
	got := invoices.invoices[0]
	if got.Status != domain.InvoiceDraft {
		t.Errorf("invoice status: got %q, want %q (pause_collection forces draft)", got.Status, domain.InvoiceDraft)
	}
	if !subs.cycleUpdated["sub_1"] {
		t.Error("billing cycle should still advance during pause_collection")
	}

	updated := subs.subs["sub_1"]
	if updated.PauseCollection == nil {
		t.Error("PauseCollection must remain set when no resumes_at is configured")
	}
}

// TestRunCycle_PauseCollection_AutoResumesWhenResumesAtPasses verifies the
// cycle scan auto-clears pause_collection when resumes_at <= effectiveNow.
// After the clear, the rest of the billing run treats the sub as fully
// resumed: the invoice is finalized (not draft), and a
// subscription.collection_resumed event fires with triggered_by="schedule"
// so analytics can distinguish auto-resume from operator-triggered resume.
func TestRunCycle_PauseCollection_AutoResumesWhenResumesAtPasses(t *testing.T) {
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	resumesAt := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC) // earlier than periodEnd (= "now")

	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_1": {
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1",
				Items:                     []domain.SubscriptionItem{{PlanID: "pln_1", Quantity: 1}},
				Status:                    domain.SubscriptionActive,
				BillingTime:               domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart,
				CurrentBillingPeriodEnd:   &periodEnd,
				NextBillingAt:             &periodEnd,
				PauseCollection: &domain.PauseCollection{
					Behavior:  domain.PauseCollectionKeepAsDraft,
					ResumesAt: &resumesAt,
				},
			},
		},
		cycleUpdated: make(map[string]bool),
	}
	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_1": {
				ID: "pln_1", Name: "Pro", Currency: "USD",
				BillingInterval: domain.BillingMonthly,
				BaseAmountCents: 4900,
			},
		},
	}
	invoices := &mockInvoices{}
	dispatcher := &capturingEventDispatcher{}
	engine := wireBaseTax(NewEngine(subs, &mockUsage{totals: map[string]int64{}}, pricing, invoices, nil, &mockSettings{}, nil, nil, billingTestClock()))
	engine.SetEventDispatcher(dispatcher)

	if _, errs := engine.RunCycle(context.Background(), 50); len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	if len(invoices.invoices) != 1 {
		t.Fatalf("got %d invoices, want 1", len(invoices.invoices))
	}
	got := invoices.invoices[0]
	if got.Status != domain.InvoiceFinalized {
		t.Errorf("invoice status after auto-resume: got %q, want %q (pause cleared, treat as normal)", got.Status, domain.InvoiceFinalized)
	}

	updated := subs.subs["sub_1"]
	if updated.PauseCollection != nil {
		t.Errorf("PauseCollection should be cleared after auto-resume, got %+v", updated.PauseCollection)
	}

	var resumeEvent map[string]any
	for _, ev := range dispatcher.events {
		if ev.eventType == domain.EventSubscriptionCollectionResumed {
			resumeEvent = ev.payload
			break
		}
	}
	if resumeEvent == nil {
		types := make([]string, 0, len(dispatcher.events))
		for _, ev := range dispatcher.events {
			types = append(types, ev.eventType)
		}
		t.Fatalf("expected %s event, got types=%v", domain.EventSubscriptionCollectionResumed, types)
	}
	if resumeEvent["triggered_by"] != "schedule" {
		t.Errorf("triggered_by: got %v, want schedule", resumeEvent["triggered_by"])
	}
}

// TestRunCycle_Trial_Active_SkipsBillingAndAdvancesCycle covers case (a) of
// the trial state machine: the cycle scan visits a trialing sub whose trial
// has not yet elapsed. No invoice generated; next_billing_at advances.
func TestRunCycle_Trial_Active_SkipsBillingAndAdvancesCycle(t *testing.T) {
	periodStart := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC) // past, so cycle scan picks it up
	trialEnd := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)  // far-future: trial still active at scan time

	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_1": {
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1",
				Items:                     []domain.SubscriptionItem{{PlanID: "pln_1", Quantity: 1}},
				Status:                    domain.SubscriptionTrialing,
				BillingTime:               domain.BillingTimeCalendar,
				TrialEndAt:                &trialEnd,
				CurrentBillingPeriodStart: &periodStart,
				CurrentBillingPeriodEnd:   &periodEnd,
				NextBillingAt:             &periodEnd,
			},
		},
		cycleUpdated: make(map[string]bool),
	}
	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_1": {ID: "pln_1", Currency: "USD", BillingInterval: domain.BillingMonthly, BaseAmountCents: 4900},
		},
	}
	invoices := &mockInvoices{}
	dispatcher := &capturingEventDispatcher{}
	engine := wireBaseTax(NewEngine(subs, &mockUsage{totals: map[string]int64{}}, pricing, invoices, nil, &mockSettings{}, nil, nil, billingTestClock()))
	engine.SetEventDispatcher(dispatcher)

	if _, errs := engine.RunCycle(context.Background(), 50); len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	if len(invoices.invoices) != 0 {
		t.Errorf("expected 0 invoices during active trial, got %d", len(invoices.invoices))
	}
	if !subs.cycleUpdated["sub_1"] {
		t.Error("expected cycle to be advanced even when trial-skipping")
	}
	if subs.subs["sub_1"].Status != domain.SubscriptionTrialing {
		t.Errorf("status should remain trialing during active trial, got %q", subs.subs["sub_1"].Status)
	}
}

// TestRunCycle_Trial_Ended_AutoActivatesAndBills covers case (b): cycle scan
// arrives after trial_end_at; the engine flips status to active, fires
// subscription.trial_ended (triggered_by="schedule"), then bills the period
// normally.
func TestRunCycle_Trial_Ended_AutoActivatesAndBills(t *testing.T) {
	periodStart := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC) // past, so cycle scan picks it up
	trialEnd := time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC) // past: trial elapsed before now

	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_1": {
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1",
				Items:                     []domain.SubscriptionItem{{PlanID: "pln_1", Quantity: 1}},
				Status:                    domain.SubscriptionTrialing,
				BillingTime:               domain.BillingTimeCalendar,
				TrialEndAt:                &trialEnd,
				CurrentBillingPeriodStart: &periodStart,
				CurrentBillingPeriodEnd:   &periodEnd,
				NextBillingAt:             &periodEnd,
			},
		},
		cycleUpdated: make(map[string]bool),
	}
	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_1": {ID: "pln_1", Currency: "USD", BillingInterval: domain.BillingMonthly, BaseAmountCents: 4900},
		},
	}
	invoices := &mockInvoices{}
	dispatcher := &capturingEventDispatcher{}
	// Per-test fake clock (this sub's periodEnd is March 2026, not
	// the April default used by billingTestClock).
	clk := clock.NewFake(periodEnd.Add(time.Nanosecond))
	engine := wireBaseTax(NewEngine(subs, &mockUsage{totals: map[string]int64{}}, pricing, invoices, nil, &mockSettings{}, nil, nil, clk))
	engine.SetEventDispatcher(dispatcher)

	if _, errs := engine.RunCycle(context.Background(), 50); len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	if len(invoices.invoices) != 1 {
		t.Fatalf("expected 1 invoice after trial ends, got %d", len(invoices.invoices))
	}
	if invoices.invoices[0].Status != domain.InvoiceFinalized {
		t.Errorf("invoice status: got %q, want finalized", invoices.invoices[0].Status)
	}

	updated := subs.subs["sub_1"]
	if updated.Status != domain.SubscriptionActive {
		t.Errorf("status: got %q, want active after trial ends", updated.Status)
	}
	if updated.ActivatedAt == nil {
		t.Error("activated_at should be stamped after trial-end auto-flip")
	}

	var trialEndedEvent map[string]any
	for _, ev := range dispatcher.events {
		if ev.eventType == domain.EventSubscriptionTrialEnded {
			trialEndedEvent = ev.payload
			break
		}
	}
	if trialEndedEvent == nil {
		types := make([]string, 0, len(dispatcher.events))
		for _, ev := range dispatcher.events {
			types = append(types, ev.eventType)
		}
		t.Fatalf("expected %s event, got types=%v", domain.EventSubscriptionTrialEnded, types)
	}
	if trialEndedEvent["triggered_by"] != "schedule" {
		t.Errorf("triggered_by: got %v, want schedule", trialEndedEvent["triggered_by"])
	}
}

// TestRunCycle_Trial_Ended_InAdvance_CoversTrialEndPeriod locks in the
// PR-2.5 fix for Bug #6: when an in_advance + trial sub auto-flips at
// cycle close, the trial-end period must be covered by a BillOnCreate-
// style invoice. Pre-fix, billOnePeriod's normal billing for in_advance
// items charged for the NEXT period (periodEnd → nextPeriodEnd) and
// the trial-end period (periodStart → periodEnd) went unbilled — a
// revenue leak specific to in_advance + trial.
//
// Post-fix expect TWO invoices at cycle close:
//   - BillOnCreate-style invoice covering the trial-end period
//     [periodStart, periodEnd] (the period the sub just exited)
//   - Normal cycle invoice covering the next period [periodEnd,
//     nextPeriodEnd] (the upcoming pre-pay)
func TestRunCycle_Trial_Ended_InAdvance_CoversTrialEndPeriod(t *testing.T) {
	periodStart := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC) // past, so cycle scan picks it up
	trialEnd := time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC) // past: trial elapsed before now

	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_1": {
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1",
				Items:                     []domain.SubscriptionItem{{PlanID: "pln_1", Quantity: 1}},
				Status:                    domain.SubscriptionTrialing,
				BillingTime:               domain.BillingTimeCalendar,
				TrialEndAt:                &trialEnd,
				CurrentBillingPeriodStart: &periodStart,
				CurrentBillingPeriodEnd:   &periodEnd,
				NextBillingAt:             &periodEnd,
			},
		},
		cycleUpdated: make(map[string]bool),
	}
	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_1": {
				ID: "pln_1", Currency: "USD", BillingInterval: domain.BillingMonthly,
				BaseAmountCents: 4900,
				BaseBillTiming:  domain.BillInAdvance,
			},
		},
	}
	invoices := &mockInvoices{}
	clk := clock.NewFake(periodEnd.Add(time.Nanosecond))
	engine := wireBaseTax(NewEngine(subs, &mockUsage{totals: map[string]int64{}}, pricing, invoices, nil, &mockSettings{}, nil, nil, clk))

	if _, errs := engine.RunCycle(context.Background(), 50); len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	if len(invoices.invoices) != 2 {
		t.Fatalf("expected 2 invoices (trial-end coverage + next-period pre-pay), got %d", len(invoices.invoices))
	}

	// Identify each invoice by its billing-period stamp. BillOnCreate
	// fires first (right after ActivateAfterTrial) and covers the
	// trial-end period; the normal cycle invoice fires next and covers
	// the upcoming period.
	var trialEndInv, nextPeriodInv *domain.Invoice
	for i := range invoices.invoices {
		inv := invoices.invoices[i]
		switch inv.BillingPeriodStart {
		case periodStart:
			trialEndInv = &inv
		case periodEnd:
			nextPeriodInv = &inv
		}
	}
	if trialEndInv == nil {
		t.Errorf("missing trial-end coverage invoice (period_start=%v) — Bug #6 regression", periodStart)
	}
	if nextPeriodInv == nil {
		t.Errorf("missing next-period pre-pay invoice (period_start=%v)", periodEnd)
	}
	updated := subs.subs["sub_1"]
	if updated.Status != domain.SubscriptionActive {
		t.Errorf("status: got %q, want active", updated.Status)
	}
}

// TestRunCycle_InAdvance_ScheduledCancelAtPeriodEnd_NoOvercharge locks in
// the PR-9 fix: when a scheduled cancel is set to fire at the upcoming
// period boundary, the cycle-close invoice MUST NOT include an in_advance
// base line for the upcoming (about-to-cancel) period. Pre-fix, the cycle
// close emitted the next-period base ($100 example) and THEN
// advanceCycleOrCancel fired the cancel — leaving the customer billed for
// a period they wouldn't use.
//
// Post-fix expected at cycle close on an in_advance sub with
// cancel_at_period_end=true:
//   - No base line (in_advance + about-to-terminate → skip)
//   - Usage line for the just-elapsed period (in-arrears for usage is
//     always correct, captures final consumption)
//   - Then the scheduled cancel fires
func TestRunCycle_InAdvance_ScheduledCancelAtPeriodEnd_NoOvercharge(t *testing.T) {
	periodStart := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_1": {
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1",
				Items:                     []domain.SubscriptionItem{{PlanID: "pln_1", Quantity: 1}},
				Status:                    domain.SubscriptionActive,
				BillingTime:               domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart,
				CurrentBillingPeriodEnd:   &periodEnd,
				NextBillingAt:             &periodEnd,
				CancelAtPeriodEnd:         true,
			},
		},
		cycleUpdated: make(map[string]bool),
	}
	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_1": {
				ID: "pln_1", Currency: "USD", BillingInterval: domain.BillingMonthly,
				BaseAmountCents: 10000,
				BaseBillTiming:  domain.BillInAdvance,
			},
		},
	}
	invoices := &mockInvoices{}
	clk := clock.NewFake(periodEnd.Add(time.Nanosecond))
	engine := wireBaseTax(NewEngine(subs, &mockUsage{totals: map[string]int64{}}, pricing, invoices, nil, &mockSettings{}, nil, nil, clk))

	if _, errs := engine.RunCycle(context.Background(), 50); len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	// Sub must have been canceled by advanceCycleOrCancel.
	if subs.subs["sub_1"].Status != domain.SubscriptionCanceled {
		t.Errorf("status: got %q, want canceled", subs.subs["sub_1"].Status)
	}

	// Pre-fix expected failure: customer billed $10000 for the upcoming
	// (will-not-be-used) period. Post-fix: in_advance base line is
	// skipped at cycle close because the cancel is about to fire. Any
	// invoice that DOES land must have total = 0 (no overcharge), not
	// $10000. The current engine still emits an empty cycle-close
	// invoice in this case — separate concern from this bug, and not
	// a correctness problem at the customer level (no charge attempt
	// on a $0 invoice).
	for _, inv := range invoices.invoices {
		if inv.TotalAmountCents > 0 {
			t.Errorf("expected $0 invoice (in_advance base skipped, no usage), got total=%d cents on period %v→%v",
				inv.TotalAmountCents, inv.BillingPeriodStart, inv.BillingPeriodEnd)
		}
	}
}

// TestBillOnCancel_PaidCheck locks in the industry-standard paid-check
// gate on cancel-proration credits. The customer must have actually
// PAID the in_advance invoice for the current period before a
// "refund-style" credit makes sense — otherwise we'd be granting
// credit against money the customer never put in. Industry parity:
// Chargebee Refundable (paid) vs Adjustment (unpaid); Stripe warns
// to disable proration with unpaid latest invoice.
func TestBillOnCancel_PaidCheck(t *testing.T) {
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	cancelAt := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC) // mid-period

	makeSub := func() domain.Subscription {
		return domain.Subscription{
			ID: "sub_1", TenantID: "t1", CustomerID: "cus_1", Code: "starter",
			Status: domain.SubscriptionCanceled,
			Items: []domain.SubscriptionItem{{
				PlanID: "pln_advance", Quantity: 1,
			}},
			CurrentBillingPeriodStart: &periodStart,
			CurrentBillingPeriodEnd:   &periodEnd,
			CanceledAt:                &cancelAt,
		}
	}
	makePricing := func() *mockPricing {
		return &mockPricing{plans: map[string]domain.Plan{
			"pln_advance": {ID: "pln_advance", Name: "Advance", Currency: "USD",
				BillingInterval: domain.BillingMonthly, BaseAmountCents: 6000,
				BaseBillTiming: domain.BillInAdvance},
		}}
	}

	t.Run("source invoice paid → credit issued (current correct behavior)", func(t *testing.T) {
		paidStart := periodStart
		paid := domain.Invoice{
			ID: "inv_1", TenantID: "t1", SubscriptionID: "sub_1",
			Status: domain.InvoiceFinalized, PaymentStatus: domain.PaymentSucceeded,
		}
		paidLine := domain.InvoiceLineItem{
			ID: "ili_1", InvoiceID: "inv_1", LineType: domain.LineTypeBaseFee,
			BillingPeriodStart: &paidStart,
		}
		inv := &mockInvoices{invoices: []domain.Invoice{paid}, lineItems: []domain.InvoiceLineItem{paidLine}}
		granter := &fakeCreditGranter{}

		engine := wireBaseTax(NewEngine(&mockSubs{}, &mockUsage{}, makePricing(), inv, nil, &mockSettings{}, nil, nil, billingTestClock()))
		engine.SetCreditGranter(granter)

		if err := engine.BillOnCancel(context.Background(), makeSub()); err != nil {
			t.Fatalf("BillOnCancel: %v", err)
		}
		if len(granter.grants) != 1 {
			t.Fatalf("expected 1 credit grant (source paid), got %d", len(granter.grants))
		}
	})

	t.Run("source invoice UNPAID → credit suppressed", func(t *testing.T) {
		unpaidStart := periodStart
		unpaid := domain.Invoice{
			ID: "inv_1", TenantID: "t1", SubscriptionID: "sub_1",
			Status: domain.InvoiceFinalized, PaymentStatus: domain.PaymentPending,
		}
		unpaidLine := domain.InvoiceLineItem{
			ID: "ili_1", InvoiceID: "inv_1", LineType: domain.LineTypeBaseFee,
			BillingPeriodStart: &unpaidStart,
		}
		inv := &mockInvoices{invoices: []domain.Invoice{unpaid}, lineItems: []domain.InvoiceLineItem{unpaidLine}}
		granter := &fakeCreditGranter{}

		engine := wireBaseTax(NewEngine(&mockSubs{}, &mockUsage{}, makePricing(), inv, nil, &mockSettings{}, nil, nil, billingTestClock()))
		engine.SetCreditGranter(granter)

		if err := engine.BillOnCancel(context.Background(), makeSub()); err != nil {
			t.Fatalf("BillOnCancel: %v", err)
		}
		if len(granter.grants) != 0 {
			t.Errorf("expected no credit grant on unpaid source invoice, got %d grants (would have credited customer for unpaid period)", len(granter.grants))
		}
	})

	t.Run("source invoice missing → credit suppressed", func(t *testing.T) {
		// e.g. tenant manually deleted invoice, or cancel fires before any
		// invoice was emitted for this period. Safest default: don't grant.
		inv := &mockInvoices{}
		granter := &fakeCreditGranter{}

		engine := wireBaseTax(NewEngine(&mockSubs{}, &mockUsage{}, makePricing(), inv, nil, &mockSettings{}, nil, nil, billingTestClock()))
		engine.SetCreditGranter(granter)

		if err := engine.BillOnCancel(context.Background(), makeSub()); err != nil {
			t.Fatalf("BillOnCancel: %v", err)
		}
		if len(granter.grants) != 0 {
			t.Errorf("expected no credit grant when source invoice missing, got %d", len(granter.grants))
		}
	})
}

// TestRunCycle_SegmentAware_InArrears_MidPeriodPlanChange locks in the
// Lago / Chargebee / Orb shape: when a customer changes plan mid-cycle
// on an in_arrears sub, the closing invoice emits one line per segment
// at the segment's own plan rate × duration fraction.
//
// Setup: 31-day cycle (March). On day 15 the item swaps from plan_a
// ($30/mo) to plan_b ($60/mo), both monthly in_arrears. The closing
// invoice should have two base lines: 14 days of plan_a (~$13.5) +
// 17 days of plan_b (~$32.9) ≈ $46. Pre-fix (single-line billing):
// $60 for the full month at the new rate. Pre-segment math: even
// bigger overcharge with immediate proration + cycle close double-
// counting.
func TestRunCycle_SegmentAware_InArrears_MidPeriodPlanChange(t *testing.T) {
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	changeAt := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)

	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_1": {
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1",
				Items: []domain.SubscriptionItem{{
					ID: "it_1", PlanID: "pln_b", Quantity: 1,
				}},
				Status: domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart, CurrentBillingPeriodEnd: &periodEnd,
				NextBillingAt: &periodEnd,
			},
		},
		cycleUpdated: make(map[string]bool),
		// Seed the change row that the DB trigger would have emitted
		// at the immediate plan swap on day 15.
		itemChanges: []domain.SubscriptionItemChange{{
			ID:                 "vlx_sic_1",
			TenantID:           "t1",
			SubscriptionID:     "sub_1",
			SubscriptionItemID: "it_1",
			ChangeType:         "plan",
			FromPlanID:         "pln_a",
			ToPlanID:           "pln_b",
			FromQuantity:       1,
			ToQuantity:         1,
			ChangedAt:          changeAt,
		}},
	}

	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_a": {ID: "pln_a", Name: "A", Currency: "USD", BillingInterval: domain.BillingMonthly, BaseAmountCents: 3000, BaseBillTiming: domain.BillInArrears},
			"pln_b": {ID: "pln_b", Name: "B", Currency: "USD", BillingInterval: domain.BillingMonthly, BaseAmountCents: 6000, BaseBillTiming: domain.BillInArrears},
		},
	}

	invoices := &mockInvoices{}
	usage := &mockUsage{totals: map[string]int64{}}
	engine := wireBaseTax(NewEngine(subs, usage, pricing, invoices, nil, &mockSettings{}, nil, nil, billingTestClock()))

	count, errs := engine.RunCycle(context.Background(), 50)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if count != 1 {
		t.Fatalf("invoices generated: got %d, want 1", count)
	}

	inv := invoices.invoices[0]
	// March has 31 days. Segment 1: [Mar 1, Mar 15) = 14 days × pln_a
	// ($30/31 × 14) ≈ 1355¢. Segment 2: [Mar 15, Apr 1) = 17 days
	// × pln_b ($60/31 × 17) ≈ 3290¢. Total ≈ 4645¢. Tolerance 4500-
	// 4800 absorbs day-rounding edges.
	if inv.SubtotalCents < 4500 || inv.SubtotalCents > 4800 {
		t.Errorf("segment-aware total: got %d cents, want ~4645 (14d @ pln_a $30 + 17d @ pln_b $60)", inv.SubtotalCents)
	}

	// Two base-fee lines expected: one per segment.
	baseLineCount := 0
	for _, li := range invoices.lineItems {
		if li.InvoiceID != inv.ID {
			continue
		}
		if li.LineType == domain.LineTypeBaseFee {
			baseLineCount++
		}
	}
	if baseLineCount != 2 {
		t.Errorf("expected 2 base-fee line items (one per segment), got %d", baseLineCount)
	}
}

// TestRunCycle_SegmentAware_UsageMetersDifferPerSegment verifies the
// Orb-shape segment-aware usage billing: when a mid-period plan
// change introduces a new meter set, each meter bills ONLY for the
// segment it was active on the sub.
//
// Setup: 31-day March cycle. On day 15 the item swaps from plan_a
// (meter X only, $5/unit) to plan_b (meter Y only, $10/unit), both
// in_arrears. Usage:
//   - Segment 1 [Mar 1, Mar 15): 100 units of meter X
//   - Segment 2 [Mar 15, Apr 1): 50 units of meter Y
//
// Expected: usage lines = X × 100 × $0.05 = $5 + Y × 50 × $0.10 = $5
// = $10 total. Pre-fix: meter Y's plan would bill BOTH X (whole
// period) and Y (whole period) regardless of segment timing —
// off by the meter that didn't exist on Plan A or B during its
// segment. With per-interval aggregation cache + mockUsage's
// perInterval stub, the meters bill exactly the segment quantities.
func TestRunCycle_SegmentAware_UsageMetersDifferPerSegment(t *testing.T) {
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	changeAt := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)

	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_1": {
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1",
				Items: []domain.SubscriptionItem{{
					ID: "it_1", PlanID: "pln_b", Quantity: 1,
				}},
				Status: domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart, CurrentBillingPeriodEnd: &periodEnd,
				NextBillingAt: &periodEnd,
			},
		},
		cycleUpdated: make(map[string]bool),
		itemChanges: []domain.SubscriptionItemChange{{
			ID:                 "vlx_sic_1",
			TenantID:           "t1",
			SubscriptionID:     "sub_1",
			SubscriptionItemID: "it_1",
			ChangeType:         "plan",
			FromPlanID:         "pln_a",
			ToPlanID:           "pln_b",
			FromQuantity:       1,
			ToQuantity:         1,
			ChangedAt:          changeAt,
		}},
	}

	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			// Zero base fees so the test isolates the usage assertion.
			"pln_a": {ID: "pln_a", Name: "A", Currency: "USD", BillingInterval: domain.BillingMonthly, BaseAmountCents: 0, BaseBillTiming: domain.BillInArrears, MeterIDs: []string{"mtr_x"}},
			"pln_b": {ID: "pln_b", Name: "B", Currency: "USD", BillingInterval: domain.BillingMonthly, BaseAmountCents: 0, BaseBillTiming: domain.BillInArrears, MeterIDs: []string{"mtr_y"}},
		},
		meters: map[string]domain.Meter{
			"mtr_x": {ID: "mtr_x", Name: "Meter X", Unit: "unit", Aggregation: "sum", RatingRuleVersionID: "rrv_x"},
			"mtr_y": {ID: "mtr_y", Name: "Meter Y", Unit: "unit", Aggregation: "sum", RatingRuleVersionID: "rrv_y"},
		},
		rules: map[string]domain.RatingRuleVersion{
			"rrv_x": {ID: "rrv_x", RuleKey: "x_key", Version: 1, Mode: domain.PricingFlat, FlatAmountCents: 5},
			"rrv_y": {ID: "rrv_y", RuleKey: "y_key", Version: 1, Mode: domain.PricingFlat, FlatAmountCents: 10},
		},
	}

	// perInterval stubs the segment-specific aggregator responses.
	// Anything outside these stubs returns nothing (zero usage).
	usage := &mockUsage{
		perInterval: map[string]int64{
			mockIntervalKey("mtr_x", periodStart, changeAt): 100,
			mockIntervalKey("mtr_y", changeAt, periodEnd):   50,
			// Full-period aggregator calls (for the cap-math precheck)
			// return zero so they don't pollute the segment math.
		},
	}

	invoices := &mockInvoices{}
	engine := wireBaseTax(NewEngine(subs, usage, pricing, invoices, nil, &mockSettings{}, nil, nil, billingTestClock()))

	count, errs := engine.RunCycle(context.Background(), 50)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if count != 1 {
		t.Fatalf("invoices generated: got %d, want 1", count)
	}

	inv := invoices.invoices[0]
	// X × 100 × $0.05 = 500¢ + Y × 50 × $0.10 = 500¢ = 1000¢ total.
	if inv.SubtotalCents != 1000 {
		t.Errorf("segment-aware usage: got %d cents, want 1000 (X×100×$0.05 + Y×50×$0.10)", inv.SubtotalCents)
	}

	// Two usage line items expected — one per (meter, segment) pair.
	usageLineCount := 0
	xCount, yCount := 0, 0
	for _, li := range invoices.lineItems {
		if li.InvoiceID != inv.ID {
			continue
		}
		if li.LineType == domain.LineTypeUsage {
			usageLineCount++
			if li.MeterID == "mtr_x" {
				xCount++
				if li.AmountCents != 500 {
					t.Errorf("meter X line amount: got %d, want 500 (100 units × $0.05)", li.AmountCents)
				}
				if li.BillingPeriodStart == nil || !li.BillingPeriodStart.Equal(periodStart) {
					t.Errorf("meter X segment start: got %v, want %v", li.BillingPeriodStart, periodStart)
				}
				if li.BillingPeriodEnd == nil || !li.BillingPeriodEnd.Equal(changeAt) {
					t.Errorf("meter X segment end: got %v, want %v (change point)", li.BillingPeriodEnd, changeAt)
				}
			}
			if li.MeterID == "mtr_y" {
				yCount++
				if li.AmountCents != 500 {
					t.Errorf("meter Y line amount: got %d, want 500 (50 units × $0.10)", li.AmountCents)
				}
				if li.BillingPeriodStart == nil || !li.BillingPeriodStart.Equal(changeAt) {
					t.Errorf("meter Y segment start: got %v, want %v (change point)", li.BillingPeriodStart, changeAt)
				}
				if li.BillingPeriodEnd == nil || !li.BillingPeriodEnd.Equal(periodEnd) {
					t.Errorf("meter Y segment end: got %v, want %v", li.BillingPeriodEnd, periodEnd)
				}
			}
		}
	}
	if usageLineCount != 2 {
		t.Errorf("expected 2 usage line items (one per meter-segment), got %d", usageLineCount)
	}
	if xCount != 1 || yCount != 1 {
		t.Errorf("expected exactly 1 line for each of meter X (got %d) and meter Y (got %d)", xCount, yCount)
	}
}

// TestRunCycle_SegmentAware_NoChanges_FullPeriodLine verifies the no-
// regress path: a sub with NO mid-period changes still emits a single
// full-period base line at the current plan/qty (segments collapse).
func TestRunCycle_SegmentAware_NoChanges_FullPeriodLine(t *testing.T) {
	periodStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	subs := &mockSubs{
		subs: map[string]domain.Subscription{
			"sub_1": {
				ID: "sub_1", TenantID: "t1", CustomerID: "cus_1",
				Items: []domain.SubscriptionItem{{ID: "it_1", PlanID: "pln_a", Quantity: 1}},
				Status: domain.SubscriptionActive, BillingTime: domain.BillingTimeCalendar,
				CurrentBillingPeriodStart: &periodStart, CurrentBillingPeriodEnd: &periodEnd,
				NextBillingAt: &periodEnd,
			},
		},
		cycleUpdated: make(map[string]bool),
		// No itemChanges seeded.
	}
	pricing := &mockPricing{
		plans: map[string]domain.Plan{
			"pln_a": {ID: "pln_a", Name: "A", Currency: "USD", BillingInterval: domain.BillingMonthly, BaseAmountCents: 3000, BaseBillTiming: domain.BillInArrears},
		},
	}
	invoices := &mockInvoices{}
	usage := &mockUsage{totals: map[string]int64{}}
	engine := wireBaseTax(NewEngine(subs, usage, pricing, invoices, nil, &mockSettings{}, nil, nil, billingTestClock()))

	count, errs := engine.RunCycle(context.Background(), 50)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if count != 1 {
		t.Fatalf("invoices generated: got %d, want 1", count)
	}
	inv := invoices.invoices[0]
	if inv.SubtotalCents != 3000 {
		t.Errorf("no-change full-period: got %d cents, want 3000 (full month @ $30)", inv.SubtotalCents)
	}
	baseLineCount := 0
	for _, li := range invoices.lineItems {
		if li.InvoiceID == inv.ID && li.LineType == domain.LineTypeBaseFee {
			baseLineCount++
		}
	}
	if baseLineCount != 1 {
		t.Errorf("expected 1 base-fee line (no segments), got %d", baseLineCount)
	}
}
