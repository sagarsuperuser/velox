package coupon

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/api/middleware"
	"github.com/sagarsuperuser/velox/internal/domain"
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

func TestBuildListFilter_ParsesTypeDurationExpiresBefore(t *testing.T) {
	t.Parallel()
	cutoff := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	url := "/coupons?type=percentage&duration=repeating&expires_before=" +
		cutoff.Format(time.RFC3339)
	req := httptest.NewRequest("GET", url, nil)

	got, err := buildListFilter(req, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Type != domain.CouponTypePercentage {
		t.Errorf("Type: got %q, want percentage", got.Type)
	}
	if got.Duration != domain.CouponDurationRepeating {
		t.Errorf("Duration: got %q, want repeating", got.Duration)
	}
	if !got.ExpiresBefore.Equal(cutoff) {
		t.Errorf("ExpiresBefore: got %v, want %v", got.ExpiresBefore, cutoff)
	}
}

func TestBuildListFilter_RejectsUnknownType(t *testing.T) {
	t.Parallel()
	// Unknown enum values must be loud — a silent accept would filter
	// the entire list to nothing, which the UI would render as "empty
	// state" and the operator would waste time debugging.
	req := httptest.NewRequest("GET", "/coupons?type=mystery", nil)
	if _, err := buildListFilter(req, false); err == nil {
		t.Fatal("expected error on unknown type, got nil")
	}
}

func TestBuildListFilter_RejectsUnknownDuration(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest("GET", "/coupons?duration=annual", nil)
	if _, err := buildListFilter(req, false); err == nil {
		t.Fatal("expected error on unknown duration, got nil")
	}
}

func TestBuildListFilter_RejectsMalformedExpiresBefore(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest("GET", "/coupons?expires_before=yesterday", nil)
	if _, err := buildListFilter(req, false); err == nil {
		t.Fatal("expected error on non-RFC3339 expires_before, got nil")
	}
}
