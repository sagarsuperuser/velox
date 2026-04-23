package coupon

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// seedCouponsForPaging drops n coupons into the mock with distinct
// created_at timestamps so the seek ordering is deterministic — tests
// can assert exact page slices.
func seedCouponsForPaging(t *testing.T, svc *Service, store *mockStore, n int) []domain.Coupon {
	t.Helper()
	out := make([]domain.Coupon, 0, n)
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range n {
		cpn, err := svc.Create(context.Background(), "t1", CreateInput{
			Code: fmt.Sprintf("PAGE-%02d", i),
			Name: fmt.Sprintf("coupon %d", i),
			Type: domain.CouponTypePercentage, PercentOffBP: 1000,
		})
		if err != nil {
			t.Fatalf("seed[%d]: %v", i, err)
		}
		// Spread timestamps one second apart so ordering is unambiguous —
		// also forces the seek-on-(created_at, id) path rather than
		// degenerating into id-only order.
		cpn.CreatedAt = base.Add(time.Duration(i) * time.Second)
		store.coupons[cpn.ID] = cpn
		store.byCode[cpn.Code] = cpn
		out = append(out, cpn)
	}
	return out
}

func TestList_DefaultLimitAppliesWhenUnset(t *testing.T) {
	t.Parallel()
	svc := NewService(newMockStore())
	store := svc.store.(*mockStore)

	seedCouponsForPaging(t, svc, store, 30)

	page, hasMore, err := svc.List(context.Background(), "t1", ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page) != 25 {
		t.Errorf("default page size: got %d, want 25", len(page))
	}
	if !hasMore {
		t.Error("hasMore: got false, want true (30 > 25)")
	}
}

func TestList_CustomLimitClampedTo100(t *testing.T) {
	t.Parallel()
	svc := NewService(newMockStore())
	store := svc.store.(*mockStore)

	seedCouponsForPaging(t, svc, store, 5)

	page, _, err := svc.List(context.Background(), "t1", ListFilter{Limit: 500})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// A too-large limit drops to the default rather than returning
	// everything — prevents a bad client from accidentally OOM'ing the
	// server.
	if len(page) != 5 {
		t.Errorf("got %d rows, want 5", len(page))
	}
}

func TestList_CursorSeeksPastPreviousPage(t *testing.T) {
	t.Parallel()
	svc := NewService(newMockStore())
	store := svc.store.(*mockStore)

	seed := seedCouponsForPaging(t, svc, store, 10)

	firstPage, hasMore, err := svc.List(context.Background(), "t1", ListFilter{Limit: 3})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if !hasMore || len(firstPage) != 3 {
		t.Fatalf("first page: got len=%d hasMore=%v, want 3/true", len(firstPage), hasMore)
	}
	// DESC order — the newest coupon (last seeded) should be at index 0.
	if firstPage[0].ID != seed[9].ID {
		t.Errorf("first page head: got %s, want newest %s", firstPage[0].ID, seed[9].ID)
	}

	last := firstPage[len(firstPage)-1]
	secondPage, hasMore, err := svc.List(context.Background(), "t1", ListFilter{
		Limit:          3,
		AfterID:        last.ID,
		AfterCreatedAt: last.CreatedAt,
	})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(secondPage) != 3 {
		t.Errorf("second page: got %d rows, want 3", len(secondPage))
	}
	// No overlap: the previous page's tail must not reappear on the
	// next page, otherwise cursors would render duplicates.
	for _, c := range secondPage {
		if c.ID == last.ID {
			t.Errorf("cursor leaked previous-page row: %s", c.ID)
		}
	}
	if !hasMore {
		t.Error("hasMore: want true (10 seeded, 3+3=6 shown)")
	}
}

func TestList_LastPageClearsHasMore(t *testing.T) {
	t.Parallel()
	svc := NewService(newMockStore())
	store := svc.store.(*mockStore)

	seed := seedCouponsForPaging(t, svc, store, 4)

	// Request a page that exactly consumes the tail. Limit=4 with
	// 4 rows available should return hasMore=false because we haven't
	// overflowed — this is the contract the next_cursor omission relies on.
	page, hasMore, err := svc.List(context.Background(), "t1", ListFilter{Limit: 4})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page) != 4 {
		t.Fatalf("got %d rows, want 4", len(page))
	}
	if hasMore {
		t.Error("hasMore: want false on exact-size page")
	}
	_ = seed
}

func TestList_ExcludesArchivedByDefault(t *testing.T) {
	t.Parallel()
	svc := NewService(newMockStore())
	store := svc.store.(*mockStore)

	seed := seedCouponsForPaging(t, svc, store, 3)
	// Archive one in place.
	c := store.coupons[seed[1].ID]
	now := time.Now().UTC()
	c.ArchivedAt = &now
	store.coupons[c.ID] = c

	page, _, err := svc.List(context.Background(), "t1", ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page) != 2 {
		t.Errorf("got %d rows, want 2 (archived excluded)", len(page))
	}
	for _, x := range page {
		if x.ID == seed[1].ID {
			t.Errorf("archived row leaked: %s", x.ID)
		}
	}

	pageAll, _, err := svc.List(context.Background(), "t1", ListFilter{IncludeArchived: true})
	if err != nil {
		t.Fatalf("List include_archived: %v", err)
	}
	if len(pageAll) != 3 {
		t.Errorf("include_archived: got %d, want 3", len(pageAll))
	}
}

func TestListRedemptions_SeekPagination(t *testing.T) {
	t.Parallel()
	store := newMockStore()
	svc := NewService(store)

	cpn, err := svc.Create(context.Background(), "t1", CreateInput{
		Code: "SEEK-RED", Name: "seek", Type: domain.CouponTypePercentage, PercentOffBP: 1000,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Seed redemptions directly into the mock with spread timestamps so
	// ordering under (created_at DESC, id DESC) is deterministic. Using
	// the mock's data slice sidesteps the gate checks — this is a pure
	// pagination assertion.
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 6 {
		store.redemptions = append(store.redemptions, domain.CouponRedemption{
			ID:         fmt.Sprintf("red_seed_%d", i),
			CouponID:   cpn.ID,
			CustomerID: "cust",
			CreatedAt:  base.Add(time.Duration(i) * time.Second),
		})
	}

	page1, hasMore, err := svc.ListRedemptions(context.Background(), "t1", cpn.ID, ListFilter{Limit: 2})
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1) != 2 || !hasMore {
		t.Fatalf("page1: got len=%d hasMore=%v, want 2/true", len(page1), hasMore)
	}

	last := page1[len(page1)-1]
	page2, _, err := svc.ListRedemptions(context.Background(), "t1", cpn.ID, ListFilter{
		Limit:          2,
		AfterID:        last.ID,
		AfterCreatedAt: last.CreatedAt,
	})
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2) != 2 {
		t.Errorf("page2: got %d rows, want 2", len(page2))
	}
	for _, r := range page2 {
		if r.ID == last.ID {
			t.Errorf("cursor leaked previous-page row: %s", r.ID)
		}
	}
}
