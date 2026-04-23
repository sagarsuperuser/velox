package coupon

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/api/middleware"
)

func TestBuildListFilter_EmptyQueryYieldsDefaults(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest("GET", "/coupons", nil)
	got, err := buildListFilter(req, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Limit != 25 {
		t.Errorf("Limit: got %d, want 25", got.Limit)
	}
	if got.AfterID != "" || !got.AfterCreatedAt.IsZero() {
		t.Errorf("seek fields leaked: %+v", got)
	}
	if got.IncludeArchived {
		t.Error("IncludeArchived: got true, want false")
	}
}

func TestBuildListFilter_HonorsLimitAndArchived(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest("GET", "/coupons?limit=10", nil)
	got, err := buildListFilter(req, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Limit != 10 {
		t.Errorf("Limit: got %d, want 10", got.Limit)
	}
	if !got.IncludeArchived {
		t.Error("IncludeArchived: got false, want true")
	}
}

func TestBuildListFilter_DecodesCursor(t *testing.T) {
	t.Parallel()
	ts := time.Date(2025, 3, 14, 15, 9, 26, 0, time.UTC)
	token := middleware.EncodeCursor("cpn_xyz", ts)

	req := httptest.NewRequest("GET", "/coupons?after="+token, nil)
	got, err := buildListFilter(req, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.AfterID != "cpn_xyz" {
		t.Errorf("AfterID: got %q, want cpn_xyz", got.AfterID)
	}
	if !got.AfterCreatedAt.Equal(ts) {
		t.Errorf("AfterCreatedAt: got %v, want %v", got.AfterCreatedAt, ts)
	}
}

func TestBuildListFilter_BadCursorReturnsError(t *testing.T) {
	t.Parallel()
	// A garbage base64 token must be rejected rather than silently
	// dropped — the UI needs a loud failure so it can clear its
	// stored cursor on the rare corruption case.
	req := httptest.NewRequest("GET", "/coupons?after=not.a.valid.cursor", nil)
	if _, err := buildListFilter(req, false); err == nil {
		t.Fatal("expected error on malformed cursor, got nil")
	}
}

func TestNewCouponPage_OmitsCursorOnLastPage(t *testing.T) {
	t.Parallel()
	resp := newCouponPage(nil, false)
	if resp.HasMore {
		t.Error("HasMore: got true, want false")
	}
	if resp.NextCursor != "" {
		t.Errorf("NextCursor: got %q, want empty", resp.NextCursor)
	}
}
