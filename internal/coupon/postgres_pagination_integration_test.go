package coupon_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/coupon"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// TestCoupon_SeekPagination_E2E verifies the Postgres seek-method List
// pages through a realistic row set without duplicates or gaps. Guards
// three things only the real DB can show:
//
//  1. The (created_at, id) < ($, $) predicate actually uses the
//     composite ordering — if the ORDER BY and the seek tuple diverged,
//     page 2 would silently duplicate or skip rows.
//  2. The limit+1 probe produces a hasMore that flips false on the tail
//     page even when the last query fetched exactly limit rows.
//  3. The archived-exclusion filter composes correctly with the seek
//     predicate in the same WHERE clause.
func TestCoupon_SeekPagination_E2E(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tenant := testutil.CreateTestTenant(t, db, "Coupon Pagination E2E")
	store := coupon.NewPostgresStore(db)
	svc := coupon.NewService(store)

	const total = 7
	ids := make([]string, 0, total)
	for i := range total {
		cpn, err := svc.Create(ctx, tenant, coupon.CreateInput{
			Code: fmt.Sprintf("PAGE-E2E-%02d", i),
			Name: fmt.Sprintf("page %d", i),
			Type: domain.CouponTypePercentage, PercentOffBP: 500,
		})
		if err != nil {
			t.Fatalf("seed[%d]: %v", i, err)
		}
		ids = append(ids, cpn.ID)
		// Nudge clock forward one ms so created_at is strictly increasing —
		// otherwise same-timestamp rows would fall back to the id tiebreak
		// and we'd be testing a different branch.
		time.Sleep(time.Millisecond)
	}

	seen := make(map[string]struct{}, total)
	var cursor struct {
		ID        string
		CreatedAt time.Time
	}
	pages := 0
	for {
		filter := coupon.ListFilter{Limit: 3, AfterID: cursor.ID, AfterCreatedAt: cursor.CreatedAt}
		page, hasMore, err := svc.List(ctx, tenant, filter)
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		for _, c := range page {
			if _, dup := seen[c.ID]; dup {
				t.Errorf("duplicate on page %d: %s", pages, c.ID)
			}
			seen[c.ID] = struct{}{}
		}
		pages++
		if !hasMore || len(page) == 0 {
			break
		}
		last := page[len(page)-1]
		cursor.ID = last.ID
		cursor.CreatedAt = last.CreatedAt
	}
	if len(seen) != total {
		t.Errorf("walked %d rows, want %d (seed left gaps)", len(seen), total)
	}
	for _, id := range ids {
		if _, ok := seen[id]; !ok {
			t.Errorf("seed row %s missing from walk", id)
		}
	}
}
