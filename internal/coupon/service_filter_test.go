package coupon

import (
	"context"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// seedMixed drops a small, deterministic mix of coupons so the filter
// tests can assert the right subset comes back. Timestamps are spaced
// so (created_at, id) ordering is stable.
func seedMixed(t *testing.T, svc *Service, store *mockStore) {
	t.Helper()
	ctx := context.Background()

	// Dates are computed relative to now so the service's "expires_at
	// must be in the future" gate doesn't reject the seed after the
	// calendar rolls forward.
	now := time.Now().UTC()
	farFuture := now.AddDate(0, 6, 0)
	midFuture := now.AddDate(0, 2, 0)
	nearFuture := now.Add(14 * 24 * time.Hour)

	// Two percentage + repeating (one near expiry, one far future).
	pct1, _ := svc.Create(ctx, "t1", CreateInput{
		Code: "PCT-R1", Name: "pct repeating",
		Type: domain.CouponTypePercentage, PercentOffBP: 1000,
		Duration: domain.CouponDurationRepeating, DurationPeriods: intPtr(3),
		ExpiresAt: timePtr(midFuture),
	})
	pct2, _ := svc.Create(ctx, "t1", CreateInput{
		Code: "PCT-R2", Name: "pct repeating 2",
		Type: domain.CouponTypePercentage, PercentOffBP: 2000,
		Duration: domain.CouponDurationRepeating, DurationPeriods: intPtr(6),
		ExpiresAt: timePtr(farFuture),
	})
	// One percentage + forever, no expiry.
	pct3, _ := svc.Create(ctx, "t1", CreateInput{
		Code: "PCT-F", Name: "pct forever",
		Type: domain.CouponTypePercentage, PercentOffBP: 500,
		Duration: domain.CouponDurationForever,
	})
	// One fixed-amount + once — expires sooner than both percentage rows.
	fix1, _ := svc.Create(ctx, "t1", CreateInput{
		Code: "FIX-O", Name: "fixed once",
		Type: domain.CouponTypeFixedAmount, AmountOff: 1000, Currency: "USD",
		Duration: domain.CouponDurationOnce,
		ExpiresAt: timePtr(nearFuture),
	})

	// Force distinct created_at so sort order is unambiguous.
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, id := range []string{pct1.ID, pct2.ID, pct3.ID, fix1.ID} {
		c := store.coupons[id]
		c.CreatedAt = base.Add(time.Duration(i) * time.Second)
		store.coupons[id] = c
		store.byCode[c.Code] = c
	}
}

func TestList_FilterByType(t *testing.T) {
	t.Parallel()
	svc := NewService(newMockStore())
	store := svc.store.(*mockStore)
	seedMixed(t, svc, store)

	got, _, err := svc.List(context.Background(), "t1", ListFilter{
		Type: domain.CouponTypePercentage,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 percentage, got %d: %+v", len(got), codes(got))
	}
	for _, c := range got {
		if c.Type != domain.CouponTypePercentage {
			t.Errorf("leaked non-percentage row: %s (%s)", c.Code, c.Type)
		}
	}
}

func TestList_FilterByDuration(t *testing.T) {
	t.Parallel()
	svc := NewService(newMockStore())
	store := svc.store.(*mockStore)
	seedMixed(t, svc, store)

	got, _, err := svc.List(context.Background(), "t1", ListFilter{
		Duration: domain.CouponDurationRepeating,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 repeating, got %d: %+v", len(got), codes(got))
	}
}

func TestList_FilterByExpiresBefore(t *testing.T) {
	t.Parallel()
	svc := NewService(newMockStore())
	store := svc.store.(*mockStore)
	seedMixed(t, svc, store)

	// Cutoff sits between FIX-O (~2 weeks out) and PCT-R1 (~2 months out)
	// so only FIX-O should come back. Computed relative to now so this
	// holds regardless of when the test runs.
	cutoff := time.Now().UTC().AddDate(0, 1, 0)
	got, _, err := svc.List(context.Background(), "t1", ListFilter{
		ExpiresBefore: cutoff,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Code != "FIX-O" {
		t.Fatalf("want [FIX-O], got %+v", codes(got))
	}
}

func TestList_FilterExpiresBeforeExcludesNullExpiry(t *testing.T) {
	t.Parallel()
	// A coupon with no expiry (forever, no expires_at) must not satisfy
	// "expires before X" — a NULL row isn't about to lapse.
	svc := NewService(newMockStore())
	store := svc.store.(*mockStore)
	seedMixed(t, svc, store)

	// A cutoff far in the future would match every expiring row. The
	// forever/no-expiry row (PCT-F) must still be excluded.
	got, _, err := svc.List(context.Background(), "t1", ListFilter{
		ExpiresBefore: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, c := range got {
		if c.Code == "PCT-F" {
			t.Fatal("never-expiring coupon leaked through ExpiresBefore filter")
		}
	}
}

func TestList_FilterCombined(t *testing.T) {
	t.Parallel()
	// Combining filters is the real-world case: "percentage coupons
	// with repeating duration expiring before July" — must AND together,
	// not OR. Regression guard against a refactor that flips the combiner.
	svc := NewService(newMockStore())
	store := svc.store.(*mockStore)
	seedMixed(t, svc, store)

	// Cutoff between PCT-R1 (~2 months) and PCT-R2 (~6 months) so only
	// PCT-R1 matches the intersection of percentage + repeating + expiring.
	got, _, err := svc.List(context.Background(), "t1", ListFilter{
		Type:          domain.CouponTypePercentage,
		Duration:      domain.CouponDurationRepeating,
		ExpiresBefore: time.Now().UTC().AddDate(0, 3, 0),
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Code != "PCT-R1" {
		t.Fatalf("want [PCT-R1], got %+v", codes(got))
	}
}

// --- helpers ---

func timePtr(t time.Time) *time.Time { return &t }

func codes(cs []domain.Coupon) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Code
	}
	return out
}
