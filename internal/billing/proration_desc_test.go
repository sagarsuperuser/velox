package billing

import (
	"testing"
	"time"
)

// TestProrationRefundDesc_RendersBillingTZCivilDays locks MANUAL_TEST FLOW TZ1.14:
// the cancel / plan-swap credit-note line description renders its period + event
// dates as civil days in the tenant BILLING timezone, not UTC. A positive-offset
// zone (Asia/Tokyo, UTC+9) is the trap — an instant at 15:00 UTC is the NEXT
// calendar day in JST, so a raw UTC render would print the prior day. Guards all
// three call sites (prepareCancelCredit + both plan-swap refund paths), which
// share this helper.
func TestProrationRefundDesc_RendersBillingTZCivilDays(t *testing.T) {
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatalf("load Asia/Tokyo: %v", err)
	}
	// Each instant is 15:00 UTC == 00:00 the NEXT day in JST, so JST reads one
	// calendar day ahead of UTC.
	periodStart := time.Date(2026, 1, 31, 15, 0, 0, 0, time.UTC) // Feb 1 JST
	periodEnd := time.Date(2026, 2, 28, 15, 0, 0, 0, time.UTC)   // Mar 1 JST
	at := time.Date(2026, 2, 14, 15, 0, 0, 0, time.UTC)          // Feb 15 JST

	got := prorationRefundDesc("Cancel proration", "pro_monthly", "canceled", periodStart, periodEnd, at, tokyo)
	want := "Cancel proration — unused portion of pro_monthly base fee (period 2026-02-01 to 2026-03-01, canceled 2026-02-15)"
	if got != want {
		t.Errorf("billing-TZ desc:\n got  %q\n want %q", got, want)
	}

	// Regression guard: the SAME instants rendered in UTC print the PRIOR calendar
	// day — so if a call site ever drops `.In(loc)`, the value diverges and is wrong.
	utc := prorationRefundDesc("Cancel proration", "pro_monthly", "canceled", periodStart, periodEnd, at, time.UTC)
	wantUTC := "Cancel proration — unused portion of pro_monthly base fee (period 2026-01-31 to 2026-02-28, canceled 2026-02-14)"
	if utc != wantUTC {
		t.Errorf("UTC control desc:\n got  %q\n want %q", utc, wantUTC)
	}
	if utc == got {
		t.Fatal("UTC and JST renders matched — the test no longer isolates the billing-TZ behavior")
	}

	// Plan-swap lead-in + verb variant (same TZ formatting; the other two sites).
	swap := prorationRefundDesc("Plan-swap refund", "pro_monthly", "swapped", periodStart, periodEnd, at, tokyo)
	wantSwap := "Plan-swap refund — unused portion of pro_monthly base fee (period 2026-02-01 to 2026-03-01, swapped 2026-02-15)"
	if swap != wantSwap {
		t.Errorf("plan-swap desc:\n got  %q\n want %q", swap, wantSwap)
	}
}
