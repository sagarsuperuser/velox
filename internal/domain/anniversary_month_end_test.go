package domain

import (
	"testing"
	"time"
)

// advanceN runs NextBillingPeriodEnd for `cycles` consecutive periods starting
// from `start`, returning each computed boundary's "Jan 2" style label in UTC.
func advanceN(start time.Time, billing SubscriptionBillingTime, interval BillingInterval, anchorDay, cycles int) []string {
	out := make([]string, 0, cycles)
	cur := start
	for range cycles {
		cur = NextBillingPeriodEnd(cur, billing, interval, time.UTC, anchorDay)
		out = append(out, cur.UTC().Format("2006-01-02"))
	}
	return out
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("length: got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("cycle %d: got %s, want %s (full: %v vs %v)", i, got[i], want[i], got, want)
		}
	}
}

// TestAnniversaryMonthEnd_ClampsAndRestores is the ADR-055 fix: an anniversary
// monthly anchor on day 29/30/31 must clamp to the target month's last day AND
// restore the higher day in long months — Jan 31 → Feb 28, Mar 31, Apr 30, …
// (Stripe/Chargebee/Lago parity), NOT ratchet to the 3rd forever.
func TestAnniversaryMonthEnd_ClampsAndRestores(t *testing.T) {
	cases := []struct {
		name      string
		anchorDay int
		start     time.Time
		want      []string
	}{
		{
			name: "day-31 anchor restores to 31 in long months", anchorDay: 31,
			start: time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC),
			want:  []string{"2026-02-28", "2026-03-31", "2026-04-30", "2026-05-31", "2026-06-30", "2026-07-31"},
		},
		{
			name: "day-30 anchor clamps Feb to 28, else 30", anchorDay: 30,
			start: time.Date(2026, 1, 30, 0, 0, 0, 0, time.UTC),
			want:  []string{"2026-02-28", "2026-03-30", "2026-04-30", "2026-05-30", "2026-06-30"},
		},
		{
			name: "day-29 anchor clamps Feb to 28, else 29", anchorDay: 29,
			start: time.Date(2026, 1, 29, 0, 0, 0, 0, time.UTC),
			want:  []string{"2026-02-28", "2026-03-29", "2026-04-29", "2026-05-29"},
		},
		{
			name: "day-28 anchor is stable everywhere (control)", anchorDay: 28,
			start: time.Date(2026, 1, 28, 0, 0, 0, 0, time.UTC),
			want:  []string{"2026-02-28", "2026-03-28", "2026-04-28", "2026-05-28"},
		},
		{
			name: "leap Feb-29 anchor clamps to 28 in non-leap years, restores to 29 in 2028", anchorDay: 29,
			start: time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC), // 2024 leap
			want:  []string{"2024-03-29", "2024-04-29"},         // monthly — exercises the 29 anchor through March/April
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eq(t, advanceN(c.start, BillingTimeAnniversary, BillingMonthly, c.anchorDay, len(c.want)), c.want)
		})
	}
}

// TestYearlyLeapAnchor_ClampsAndRestores: a Feb-29 yearly anchor must bill
// Feb 28 in non-leap years and restore to Feb 29 the next leap year — never
// ratchet to Mar 1 (the pre-ADR-055 Go-overflow behavior).
func TestYearlyLeapAnchor_ClampsAndRestores(t *testing.T) {
	start := time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC) // 2024 leap
	got := advanceN(start, BillingTimeAnniversary, BillingYearly, 29, 4)
	want := []string{"2025-02-28", "2026-02-28", "2027-02-28", "2028-02-29"} // restores in 2028 (leap)
	eq(t, got, want)
}

// TestAnniversaryAnchorDayZero_LegacyFallback: anchorDay 0 (legacy / unset
// rows pre-migration-0120) must preserve the historical addIntervalIn path —
// Jan 31 + 1mo overflows to Mar 3 (Go normalization). Locks the additive
// no-behavior-change contract for the column's zero value.
func TestAnniversaryAnchorDayZero_LegacyFallback(t *testing.T) {
	got := NextBillingPeriodEnd(time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC), BillingTimeAnniversary, BillingMonthly, time.UTC, 0)
	if g := got.UTC().Format("2006-01-02"); g != "2026-03-03" {
		t.Fatalf("anchorDay=0 must be legacy overflow Jan31→Mar3, got %s", g)
	}
}

// TestAnchorDayFor: the persisted anchor is the period-start day-of-month for
// yearly + anniversary-monthly, and 0 for calendar-monthly (whose advance
// ignores it).
func TestAnchorDayFor(t *testing.T) {
	jan31 := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	if d := AnchorDayFor(jan31, BillingTimeAnniversary, BillingMonthly, time.UTC); d != 31 {
		t.Errorf("anniversary monthly: got %d, want 31", d)
	}
	if d := AnchorDayFor(jan31, BillingTimeCalendar, BillingMonthly, time.UTC); d != 0 {
		t.Errorf("calendar monthly: got %d, want 0 (meaningless for calendar)", d)
	}
	if d := AnchorDayFor(jan31, BillingTimeCalendar, BillingYearly, time.UTC); d != 31 {
		t.Errorf("yearly (always anniversary): got %d, want 31", d)
	}
}
