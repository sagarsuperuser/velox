package coupon_test

import (
	"context"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/coupon"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// TestCoupon_ListFilters_E2E exercises the type / duration / expires_before
// predicates against a real Postgres instance. The unit-level mock tests
// cover the shape of the filter; this one proves the SQL string builder
// numbers its $-placeholders correctly and that the ExpiresBefore
// predicate handles NULL expires_at the way Postgres three-valued logic
// does (NULL < anything → NULL → excluded), not the way a naive Go
// comparison would.
func TestCoupon_ListFilters_E2E(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tenant := testutil.CreateTestTenant(t, db, "Coupon Filters E2E")
	store := coupon.NewPostgresStore(db)
	svc := coupon.NewService(store)

	// Dates relative to now so the "expires_at must be future" gate in
	// the service doesn't reject seed rows as the calendar rolls.
	now := time.Now().UTC()
	soon := now.Add(14 * 24 * time.Hour)
	later := now.AddDate(0, 3, 0)

	// Mix: 1 percentage+forever (no expiry), 1 percentage+repeating+soon,
	// 1 fixed+once+later.
	pctForever, err := svc.Create(ctx, tenant, coupon.CreateInput{
		Code: "FILT-PCT-F", Name: "pct forever",
		Type: domain.CouponTypePercentage, PercentOffBP: 500,
		Duration: domain.CouponDurationForever,
	})
	if err != nil {
		t.Fatalf("seed pct-forever: %v", err)
	}
	pctSoon, err := svc.Create(ctx, tenant, coupon.CreateInput{
		Code: "FILT-PCT-R", Name: "pct repeating",
		Type: domain.CouponTypePercentage, PercentOffBP: 1000,
		Duration: domain.CouponDurationRepeating, DurationPeriods: ptr(3),
		ExpiresAt: ptrTime(soon),
	})
	if err != nil {
		t.Fatalf("seed pct-soon: %v", err)
	}
	fixLater, err := svc.Create(ctx, tenant, coupon.CreateInput{
		Code: "FILT-FIX-O", Name: "fixed once",
		Type: domain.CouponTypeFixedAmount, AmountOff: 1000, Currency: "USD",
		Duration:  domain.CouponDurationOnce,
		ExpiresAt: ptrTime(later),
	})
	if err != nil {
		t.Fatalf("seed fix-later: %v", err)
	}

	t.Run("type=percentage", func(t *testing.T) {
		got, _, err := svc.List(ctx, tenant, coupon.ListFilter{
			Type: domain.CouponTypePercentage,
		})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		found := map[string]bool{}
		for _, c := range got {
			found[c.ID] = true
		}
		if !found[pctForever.ID] || !found[pctSoon.ID] {
			t.Errorf("missing percentage rows: %+v", found)
		}
		if found[fixLater.ID] {
			t.Errorf("fixed-amount leaked into type=percentage")
		}
	})

	t.Run("duration=once", func(t *testing.T) {
		got, _, err := svc.List(ctx, tenant, coupon.ListFilter{
			Duration: domain.CouponDurationOnce,
		})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 1 || got[0].ID != fixLater.ID {
			t.Errorf("want [%s], got %+v", fixLater.ID, got)
		}
	})

	t.Run("expires_before excludes NULL", func(t *testing.T) {
		// Cutoff far in the future — matches both expiring rows but must
		// exclude pctForever (NULL expires_at). This is the Postgres
		// three-valued logic checkpoint that a mock can't confirm.
		got, _, err := svc.List(ctx, tenant, coupon.ListFilter{
			ExpiresBefore: now.AddDate(10, 0, 0),
		})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, c := range got {
			if c.ID == pctForever.ID {
				t.Fatalf("pctForever (NULL expires_at) leaked through ExpiresBefore")
			}
		}
		if len(got) != 2 {
			t.Errorf("want 2 expiring rows, got %d", len(got))
		}
	})

	t.Run("combined AND", func(t *testing.T) {
		// type=percentage + duration=repeating + expires_before=later
		// → only pctSoon satisfies all three. Proves the predicates ALL
		// land in the same WHERE with AND, not OR.
		got, _, err := svc.List(ctx, tenant, coupon.ListFilter{
			Type:          domain.CouponTypePercentage,
			Duration:      domain.CouponDurationRepeating,
			ExpiresBefore: later.Add(time.Second),
		})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 1 || got[0].ID != pctSoon.ID {
			t.Errorf("want [%s], got %+v", pctSoon.ID, got)
		}
	})
}

func ptr(i int) *int                 { return &i }
func ptrTime(t time.Time) *time.Time { return &t }
