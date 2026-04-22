package coupon_test

import (
	"context"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/coupon"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// TestCoupon_DurationAndStacking_E2E exercises FEAT-6 end-to-end against a
// real Postgres. The unit tests in service_test.go use an in-memory mock
// store and therefore can't catch two things only the real schema can:
//
//  1. The CHECK constraint on coupons.duration_periods actually enforces
//     the "repeating requires positive periods, once/forever requires
//     NULL" rule — a bug in service-layer validation would still land a
//     broken row in the DB.
//  2. IncrementPeriodsApplied is visible to the next ApplyToInvoice call.
//     The mock maintains shared state trivially; SQL has to round-trip
//     through the row, honouring RLS bypass (TxTenant) and the
//     coupon_redemptions.periods_applied default of 0.
//
// Scenario exercised:
//
//   - tenantA gets one subscription. Redeem two stackable coupons against it:
//     10% repeating for 3 periods, and $5 forever fixed.
//   - Cycle 1-3: both coupons apply. Combined discount = 10% + $5 = $15 on
//     a $100 invoice. After each ApplyToInvoice, MarkPeriodsApplied bumps
//     the repeating redemption's periods_applied counter.
//   - Cycle 4: the repeating coupon is exhausted; only the forever $5
//     applies. Combined discount = $5.
//   - A separate subscription on the same tenant gets a non-stackable 20%
//     coupon plus a stackable 5% + $3. Because one is non-stackable, the
//     apply path falls back to "best single wins" — the 20% coupon
//     dominates, returning a single redemption ID.
func TestCoupon_DurationAndStacking_E2E(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	tenant := testutil.CreateTestTenant(t, db, "Coupon FEAT-6 E2E")

	store := coupon.NewPostgresStore(db)
	svc := coupon.NewService(store)

	// ----------------------------------------------------------------------
	// Scenario 1: stackable repeating + stackable forever on sub_stack
	// ----------------------------------------------------------------------
	three := 3
	repCpn, err := svc.Create(ctx, tenant, coupon.CreateInput{
		Code:            "REP3MO10",
		Name:            "10% for 3 months",
		Type:            domain.CouponTypePercentage,
		PercentOff:      10,
		Duration:        domain.CouponDurationRepeating,
		DurationPeriods: &three,
		Stackable:       true,
	})
	if err != nil {
		t.Fatalf("create repeating coupon: %v", err)
	}
	forCpn, err := svc.Create(ctx, tenant, coupon.CreateInput{
		Code:      "FOREVER5",
		Name:      "$5 forever",
		Type:      domain.CouponTypeFixedAmount,
		AmountOff: 500,
		Currency:  "USD",
		Duration:  domain.CouponDurationForever,
		Stackable: true,
	})
	if err != nil {
		t.Fatalf("create forever coupon: %v", err)
	}

	// Attach both coupons to sub_stack. CreateRedemption writes the row
	// with periods_applied=0 (schema default) — that's what ApplyToInvoice
	// reads back through ListRedemptionsBySubscription.
	subStack := "sub_stack_" + tenant
	repRed, err := store.CreateRedemption(ctx, tenant, domain.CouponRedemption{
		CouponID:       repCpn.ID,
		CustomerID:     "cust_stack",
		SubscriptionID: subStack,
		DiscountCents:  1000,
	})
	if err != nil {
		t.Fatalf("create rep redemption: %v", err)
	}
	forRed, err := store.CreateRedemption(ctx, tenant, domain.CouponRedemption{
		CouponID:       forCpn.ID,
		CustomerID:     "cust_stack",
		SubscriptionID: subStack,
		DiscountCents:  500,
	})
	if err != nil {
		t.Fatalf("create forever redemption: %v", err)
	}
	_ = forRed

	// Cycles 1..3: both coupons still eligible, both stackable → combine.
	for cycle := 1; cycle <= 3; cycle++ {
		got, err := svc.ApplyToInvoice(ctx, tenant, subStack, "", []string{"plan_x"}, 10000)
		if err != nil {
			t.Fatalf("cycle %d ApplyToInvoice: %v", cycle, err)
		}
		// 10% of 10000 = 1000; + $5 fixed = $15 total discount.
		if got.Cents != 1500 {
			t.Errorf("cycle %d: expected combined discount 1500, got %d", cycle, got.Cents)
		}
		if len(got.RedemptionIDs) != 2 {
			t.Errorf("cycle %d: expected 2 redemption IDs, got %v", cycle, got.RedemptionIDs)
		}
		if err := svc.MarkPeriodsApplied(ctx, tenant, got.RedemptionIDs); err != nil {
			t.Fatalf("cycle %d MarkPeriodsApplied: %v", cycle, err)
		}
	}

	// After 3 periods the repeating coupon's periods_applied reaches its
	// duration_periods and falls out of the eligible pool. Verify the
	// counter actually made it to the DB — not just an in-memory side
	// effect — by reading the redemption back.
	reds, err := store.ListRedemptionsBySubscription(ctx, tenant, subStack)
	if err != nil {
		t.Fatalf("list redemptions: %v", err)
	}
	var repPersisted domain.CouponRedemption
	for _, r := range reds {
		if r.ID == repRed.ID {
			repPersisted = r
			break
		}
	}
	if repPersisted.PeriodsApplied != 3 {
		t.Errorf("after 3 cycles, repeating redemption periods_applied=%d, want 3",
			repPersisted.PeriodsApplied)
	}

	// Cycle 4: repeating is exhausted; only the $5 forever applies.
	got, err := svc.ApplyToInvoice(ctx, tenant, subStack, "", []string{"plan_x"}, 10000)
	if err != nil {
		t.Fatalf("cycle 4 ApplyToInvoice: %v", err)
	}
	if got.Cents != 500 {
		t.Errorf("cycle 4: expected only forever discount 500, got %d", got.Cents)
	}
	if len(got.RedemptionIDs) != 1 {
		t.Errorf("cycle 4: expected 1 redemption ID, got %v", got.RedemptionIDs)
	}

	// ----------------------------------------------------------------------
	// Scenario 2: non-stackable present → best single wins
	// ----------------------------------------------------------------------
	nsCpn, err := svc.Create(ctx, tenant, coupon.CreateInput{
		Code:       "BIG20",
		Name:       "20% off",
		Type:       domain.CouponTypePercentage,
		PercentOff: 20,
		Duration:   domain.CouponDurationForever,
		Stackable:  false,
	})
	if err != nil {
		t.Fatalf("create non-stackable coupon: %v", err)
	}
	sCpn, err := svc.Create(ctx, tenant, coupon.CreateInput{
		Code:       "SMALL5",
		Name:       "5% off",
		Type:       domain.CouponTypePercentage,
		PercentOff: 5,
		Duration:   domain.CouponDurationForever,
		Stackable:  true,
	})
	if err != nil {
		t.Fatalf("create small stackable coupon: %v", err)
	}
	sfCpn, err := svc.Create(ctx, tenant, coupon.CreateInput{
		Code:      "FIX3",
		Name:      "$3 off",
		Type:      domain.CouponTypeFixedAmount,
		AmountOff: 300,
		Currency:  "USD",
		Duration:  domain.CouponDurationForever,
		Stackable: true,
	})
	if err != nil {
		t.Fatalf("create small fixed coupon: %v", err)
	}

	subMixed := "sub_mixed_" + tenant
	_, _ = store.CreateRedemption(ctx, tenant, domain.CouponRedemption{
		CouponID: sCpn.ID, CustomerID: "cust_mixed", SubscriptionID: subMixed,
	})
	bigRed, err := store.CreateRedemption(ctx, tenant, domain.CouponRedemption{
		CouponID: nsCpn.ID, CustomerID: "cust_mixed", SubscriptionID: subMixed,
	})
	if err != nil {
		t.Fatalf("create big redemption: %v", err)
	}
	_, _ = store.CreateRedemption(ctx, tenant, domain.CouponRedemption{
		CouponID: sfCpn.ID, CustomerID: "cust_mixed", SubscriptionID: subMixed,
	})

	got, err = svc.ApplyToInvoice(ctx, tenant, subMixed, "", []string{"plan_y"}, 10000)
	if err != nil {
		t.Fatalf("mixed ApplyToInvoice: %v", err)
	}
	// 20% of 10000 = 2000; non-stackable wins alone, no combination.
	if got.Cents != 2000 {
		t.Errorf("mixed: expected best single 2000, got %d", got.Cents)
	}
	if len(got.RedemptionIDs) != 1 || got.RedemptionIDs[0] != bigRed.ID {
		t.Errorf("mixed: expected only bigRed (%s), got %v", bigRed.ID, got.RedemptionIDs)
	}

	// ----------------------------------------------------------------------
	// Scenario 3: DB-level CHECK constraint catches a malformed coupon
	// ----------------------------------------------------------------------
	// Service.Create rejects duration='repeating' without periods, but the
	// on-disk CHECK is the backstop. Exercise it via a direct INSERT so we
	// know the constraint is wired, not just that service validation
	// runs. If the CHECK is missing the INSERT succeeds and this test
	// flips from pass to fail.
	_, err = db.Pool.ExecContext(ctx, `
		INSERT INTO coupons (id, tenant_id, code, name, type, amount_off, percent_off,
			currency, times_redeemed, active, duration, duration_periods, stackable,
			plan_ids)
		VALUES ('cpn_bad_' || $1, $1, 'BADREP', 'bad', 'percentage', 0, 10, '', 0,
			true, 'repeating', NULL, false, '{}')
	`, tenant)
	if err == nil {
		t.Error("expected DB CHECK constraint to reject repeating coupon with NULL duration_periods")
	}
}
