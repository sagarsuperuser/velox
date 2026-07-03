package subscription

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/domain"
)

// fakeTenantLocator stands in for the injected billing-timezone resolver: it
// reports the tenant's CURRENT (live) display timezone, which a period display
// must ignore in favor of the sub's snapshot (ADR-074).
type fakeTenantLocator struct{ loc *time.Location }

func (f fakeTenantLocator) TenantLocation(_ context.Context, _ string) *time.Location {
	return f.loc
}

// TestHandler_Get_PeriodDisplayTZ is the display half of ADR-074: the
// backend-authored current_billing_period_display is anchored in the SUB's
// billing timezone, not the live tenant timezone. A tenant that changes its
// timezone after a sub is running must NOT see the sub's displayed period shift.
//
// The half-open period is [May 2 00:00 IST, Jun 1 00:00 IST). Rendered in the
// snapshot (Asia/Kolkata) it reads "May 2 – May 31"; rendered in the live
// tenant TZ (America/New_York) the same instants land a full calendar day
// earlier — "May 1 – May 30". The two strings differ, so the assertion cannot
// pass vacuously.
//
// Mutation-verify: change stampPeriodDisplay to pass h.tenantLoc(...) instead of
// h.subLoc(...) — the snapshot case then renders the New_York range and fails.
func TestHandler_Get_PeriodDisplayTZ(t *testing.T) {
	ctx := context.Background()
	tenantID := "t1"

	// May 1 18:30 UTC = May 2 00:00 IST; May 31 18:30 UTC = Jun 1 00:00 IST.
	start := time.Date(2026, 5, 1, 18, 30, 0, 0, time.UTC)
	end := time.Date(2026, 5, 31, 18, 30, 0, 0, time.UTC)

	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load NY: %v", err)
	}

	cases := []struct {
		name  string
		subTZ string // the sub's snapshotted BillingTimezone
		want  string
	}{
		{"snapshot anchors the display", "Asia/Kolkata", "May 2, 2026 – May 31, 2026"},
		{"legacy empty-TZ falls back to live tenant TZ", "", "May 1, 2026 – May 30, 2026"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemStore()
			subID, _ := seedSubWithItemAt(t, store, tenantID, "cus_1", "plan_basic", start, end)

			s := store.subs[subID]
			s.BillingTimezone = tc.subTZ
			store.subs[subID] = s

			h := NewHandler(NewService(store, nil))
			// The tenant has SINCE moved its display TZ to New_York.
			h.SetTenantLocator(fakeTenantLocator{loc: ny})

			req := httptest.NewRequest(http.MethodGet, "/subscriptions/"+subID, nil)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", subID)
			req = req.WithContext(context.WithValue(
				context.WithValue(ctx, chi.RouteCtxKey, rctx),
				auth.TestTenantIDKey(), tenantID,
			))

			rr := httptest.NewRecorder()
			h.get(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status: got %d, want 200. body=%s", rr.Code, rr.Body.String())
			}

			var got domain.Subscription
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.CurrentBillingPeriodDisplay != tc.want {
				t.Errorf("current_billing_period_display: got %q, want %q", got.CurrentBillingPeriodDisplay, tc.want)
			}
		})
	}
}
