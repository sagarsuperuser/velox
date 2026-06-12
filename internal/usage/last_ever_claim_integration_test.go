package usage_test

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/testutil"
	"github.com/sagarsuperuser/velox/internal/usage"
)

// TestAggregateByPricingRules_LastEverDoesNotDoubleClaim locks the H2 fix from
// the periodic-jobs audit: an event claimed by a HIGHER-priority period rule
// (sum/max/count) must NOT also appear in a lower-priority last_ever bucket.
// Pre-fix the last_ever pass ranked only last_ever rules when claiming, so the
// same event was billed twice — once in the sum bucket, once as last_ever
// (reproduced live: sum=52 AND last_ever=42 from the same two events).
func TestAggregateByPricingRules_LastEverDoesNotDoubleClaim(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx, cancel := context.WithTimeout(postgres.WithLivemode(context.Background(), false), 30*time.Second)
	defer cancel()
	store := usage.NewPostgresStore(db)

	t.Run("event won by period rule is excluded from last_ever", func(t *testing.T) {
		tenantID := testutil.CreateTestTenant(t, db, "LastEver NoDouble")
		customerID := insertTestCustomer(t, db, tenantID, "cus_le1")
		meterID := insertTestMeter(t, db, tenantID, "mtr_le1", "tokens_le1")
		sumRRV := insertTestRatingRule(t, db, tenantID, "rate_le_sum")
		leverRRV := insertTestRatingRule(t, db, tenantID, "rate_le_lever")
		// HIGH-priority sum rule and LOW-priority last_ever rule match the
		// SAME dimension — every event's true winner is the sum rule.
		insertTestPricingRule(t, db, tenantID, meterID, sumRRV,
			map[string]any{"model": "gpt-4"}, domain.AggSum, 200)
		insertTestPricingRule(t, db, tenantID, meterID, leverRRV,
			map[string]any{"model": "gpt-4"}, domain.AggLastEver, 100)

		from := time.Now().Add(-1 * time.Hour)
		to := time.Now().Add(1 * time.Hour)
		ingest(t, ctx, store, tenantID, customerID, meterID, decimal.NewFromInt(10), map[string]any{"model": "gpt-4"})
		ingest(t, ctx, store, tenantID, customerID, meterID, decimal.NewFromInt(42), map[string]any{"model": "gpt-4"})

		got, err := store.AggregateByPricingRules(ctx, tenantID, customerID, meterID, domain.AggSum, from, to)
		if err != nil {
			t.Fatalf("aggregate: %v", err)
		}
		// Only the sum bucket: 10+42=52. Pre-fix this ALSO returned a
		// last_ever bucket of 42 — the same event billed twice.
		assertRuleAggregations(t, got, map[string]decimal.Decimal{
			sumRRV: decimal.NewFromInt(52),
		})
	})

	t.Run("event won by last_ever rule lands only in last_ever", func(t *testing.T) {
		tenantID := testutil.CreateTestTenant(t, db, "LastEver Wins")
		customerID := insertTestCustomer(t, db, tenantID, "cus_le2")
		meterID := insertTestMeter(t, db, tenantID, "mtr_le2", "tokens_le2")
		sumRRV := insertTestRatingRule(t, db, tenantID, "rate_le2_sum")
		leverRRV := insertTestRatingRule(t, db, tenantID, "rate_le2_lever")
		// last_ever has the HIGHER priority here — it wins the claim; the
		// period pass must exclude the event (no leak into sum/unclaimed).
		insertTestPricingRule(t, db, tenantID, meterID, leverRRV,
			map[string]any{"model": "gpt-4"}, domain.AggLastEver, 200)
		insertTestPricingRule(t, db, tenantID, meterID, sumRRV,
			map[string]any{"model": "gpt-4"}, domain.AggSum, 100)

		from := time.Now().Add(-1 * time.Hour)
		to := time.Now().Add(1 * time.Hour)
		ingest(t, ctx, store, tenantID, customerID, meterID, decimal.NewFromInt(7), map[string]any{"model": "gpt-4"})
		ingest(t, ctx, store, tenantID, customerID, meterID, decimal.NewFromInt(33), map[string]any{"model": "gpt-4"})

		got, err := store.AggregateByPricingRules(ctx, tenantID, customerID, meterID, domain.AggSum, from, to)
		if err != nil {
			t.Fatalf("aggregate: %v", err)
		}
		// Only the last_ever bucket, with the most recent quantity.
		assertRuleAggregations(t, got, map[string]decimal.Decimal{
			leverRRV: decimal.NewFromInt(33),
		})
	})
}
