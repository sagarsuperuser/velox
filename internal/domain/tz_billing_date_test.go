package domain

import (
	"testing"
	"time"
)

// These pin the ADR-058 fix: every month/year billing advance is anchored in
// the tenant timezone, so the result depends ONLY on the tenant loc — never on
// the input time.Time's ambient Location (which, for a pgx-scanned timestamptz,
// is the host time.Local). The pre-fix bug gave 30 vs 31 days for the SAME
// instant depending on whether it was UTC-located or Local-located. The
// existing suite builds every input in time.UTC, so it never exercised this.
func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return loc
}

// TestAddBillingInterval_ProvenanceIndependent: the SAME instant, presented in
// UTC vs IST Location, must advance to the SAME tenant-anchored result.
func TestAddBillingInterval_ProvenanceIndependent(t *testing.T) {
	ist := mustLoc(t, "Asia/Kolkata")
	// 2026-05-31 18:30 UTC == 2026-06-01 00:00 IST — an IST anniversary anchored
	// on the 1st, stored as a month-end instant in UTC. The trap case.
	inst := time.Date(2026, 5, 31, 18, 30, 0, 0, time.UTC)
	// Correct: a June (IST) anniversary cycle is 30 days → Jul 1 00:00 IST.
	want := time.Date(2026, 7, 1, 0, 0, 0, 0, ist).UTC() // = 2026-06-30 18:30 UTC

	for _, in := range []struct {
		name string
		t    time.Time
	}{
		{"UTC-located (fresh build / UTC host)", inst},
		{"IST-located (DB-scanned on IST host)", inst.In(ist)},
	} {
		t.Run(in.name, func(t *testing.T) {
			got := AddBillingInterval(in.t, BillingMonthly, ist, 0)
			if !got.Equal(want) {
				t.Errorf("AddBillingInterval = %s, want %s (must depend only on loc, not input Location)",
					got.UTC(), want.UTC())
			}
		})
	}

	// Guard the pre-fix behavior is what we moved away from: with loc=UTC the
	// same instant overflows May-31 -> Jul-1 (31 days). This documents WHY the
	// loc matters — not the desired result for an IST tenant.
	utcResult := AddBillingInterval(inst, BillingMonthly, time.UTC, 0)
	if utcResult.Equal(want) {
		t.Error("UTC-loc advance unexpectedly matched the IST anniversary — the test instant no longer isolates the bug")
	}
}

// TestNextBillingPeriodEnd_TenantTZAnchored covers the anniversary + calendar +
// yearly branches under a non-UTC tenant.
func TestNextBillingPeriodEnd_TenantTZAnchored(t *testing.T) {
	ist := mustLoc(t, "Asia/Kolkata")
	cases := []struct {
		name      string
		periodEnd time.Time
		billing   SubscriptionBillingTime
		interval  BillingInterval
		want      time.Time
	}{
		{
			// Anniversary monthly, Jun 1 IST anchor → Jul 1 IST (30-day June cycle).
			name:      "anniversary monthly Jun1 IST -> Jul1 IST",
			periodEnd: time.Date(2026, 6, 1, 0, 0, 0, 0, ist).UTC(),
			billing:   BillingTimeAnniversary, interval: BillingMonthly,
			want: time.Date(2026, 7, 1, 0, 0, 0, 0, ist).UTC(),
		},
		{
			// Calendar month-end (Root C): a Jan 31 IST boundary must roll to
			// Feb 1 IST, NOT skip February to Mar 1 (the snap-after-overflow bug).
			name:      "calendar monthly Jan31 IST -> Feb1 IST (no Feb skip)",
			periodEnd: time.Date(2026, 1, 31, 0, 0, 0, 0, ist).UTC(),
			billing:   BillingTimeCalendar, interval: BillingMonthly,
			want: time.Date(2026, 2, 1, 0, 0, 0, 0, ist).UTC(),
		},
		{
			// Yearly anniversary, Mar 1 2027 IST anchor (stored Feb 28 UTC) →
			// Mar 1 2028 IST, not a day-early Feb 29 IST.
			name:      "yearly Mar1-2027 IST -> Mar1-2028 IST (leap-safe)",
			periodEnd: time.Date(2027, 3, 1, 0, 0, 0, 0, ist).UTC(),
			billing:   BillingTimeAnniversary, interval: BillingYearly,
			want: time.Date(2028, 3, 1, 0, 0, 0, 0, ist).UTC(),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := NextBillingPeriodEnd(c.periodEnd, c.billing, c.interval, ist, 0)
			if !got.Equal(c.want) {
				t.Errorf("got %s, want %s", got.UTC(), c.want.UTC())
			}
		})
	}
}

// TestFormatInclusivePeriod pins the inclusive last-day display (ADR-058
// follow-up): the exclusive period_end is shown as the previous CALENDAR day in
// the tenant TZ — NOT a 24h instant subtraction (which mis-lands across DST /
// non-midnight ends), and tenant-TZ-anchored so an offset tenant and a UTC
// tenant legitimately differ on the same instant.
func TestFormatInclusivePeriod(t *testing.T) {
	ist := mustLoc(t, "Asia/Kolkata")
	// Period [Jun 1 00:00 IST, Jul 1 00:00 IST) — stored end = 2026-06-30 18:30 UTC.
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, ist).UTC()
	end := time.Date(2026, 7, 1, 0, 0, 0, 0, ist).UTC()

	if got := FormatInclusivePeriod(start, end, ist); got != "Jun 1, 2026 – Jun 30, 2026" {
		t.Errorf("IST inclusive period = %q, want \"Jun 1, 2026 – Jun 30, 2026\"", got)
	}
	// Same instants under a UTC tenant: start civil = May 31 18:30 (snap May 31),
	// end civil = Jun 30 18:30 (snap Jun 30, minus one = Jun 29).
	if got := FormatInclusivePeriod(start, end, time.UTC); got != "May 31, 2026 – Jun 29, 2026" {
		t.Errorf("UTC inclusive period = %q, want \"May 31, 2026 – Jun 29, 2026\" (tenant-TZ-anchored divergence)", got)
	}
	// One-off / no-period invoice (start == end): omit entirely.
	now := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	if got := FormatInclusivePeriod(now, now, ist); got != "" {
		t.Errorf("one-off period = %q, want \"\" (omit)", got)
	}
	// Single covered day [Jun 1 00:00, Jun 2 00:00) IST → "Jun 1 – Jun 1".
	d1 := time.Date(2026, 6, 1, 0, 0, 0, 0, ist).UTC()
	d2 := time.Date(2026, 6, 2, 0, 0, 0, 0, ist).UTC()
	if got := FormatInclusivePeriod(d1, d2, ist); got != "Jun 1, 2026 – Jun 1, 2026" {
		t.Errorf("single-day period = %q, want \"Jun 1, 2026 – Jun 1, 2026\"", got)
	}
	// Sub-day stub (start != end but < 1 day) must clamp, never invert.
	s := time.Date(2026, 6, 1, 9, 0, 0, 0, ist).UTC()
	e := time.Date(2026, 6, 1, 18, 0, 0, 0, ist).UTC()
	if got := FormatInclusivePeriod(s, e, ist); got != "Jun 1, 2026 – Jun 1, 2026" {
		t.Errorf("sub-day stub = %q, want clamped \"Jun 1, 2026 – Jun 1, 2026\" (no inversion)", got)
	}
}
