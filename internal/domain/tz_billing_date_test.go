package domain

import (
	"testing"
	"time"
)

// These pin the ADR-050 fix: every month/year billing advance is anchored in
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
			got := AddBillingInterval(in.t, BillingMonthly, ist)
			if !got.Equal(want) {
				t.Errorf("AddBillingInterval = %s, want %s (must depend only on loc, not input Location)",
					got.UTC(), want.UTC())
			}
		})
	}

	// Guard the pre-fix behavior is what we moved away from: with loc=UTC the
	// same instant overflows May-31 -> Jul-1 (31 days). This documents WHY the
	// loc matters — not the desired result for an IST tenant.
	utcResult := AddBillingInterval(inst, BillingMonthly, time.UTC)
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
			got := NextBillingPeriodEnd(c.periodEnd, c.billing, c.interval, ist)
			if !got.Equal(c.want) {
				t.Errorf("got %s, want %s", got.UTC(), c.want.UTC())
			}
		})
	}
}
