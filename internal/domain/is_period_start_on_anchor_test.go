package domain

import (
	"testing"
	"time"
)

// TestIsPeriodStartOnAnchor pins the off-anchor detector the in_advance base-fee
// proration uses to tell a FULL anniversary cycle apart from a re-anchor SEAM
// (ADR-091, the anniversary/yearly overbill fix). Getting this wrong either
// re-opens the seam overbill (false negative on a seam) or spuriously prorates a
// normal month-end period (false positive on a clean boundary).
func TestIsPeriodStartOnAnchor(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	ist := mustLoc(t, "Asia/Kolkata")

	cases := []struct {
		name      string
		t         time.Time
		anchorDay int
		loc       *time.Location
		want      bool
	}{
		// Calendar billing (anchorDay 0) is always reported on-anchor — it snaps
		// to a fixed grid and must never trigger the anniversary override.
		{"calendar anchorDay 0 — always on-anchor (mid-month)", date(2027, 6, 20, 0, ist), 0, ist, true},
		{"calendar anchorDay 0 — always on-anchor (1st)", date(2027, 6, 1, 0, ist), 0, ist, true},

		// Anniversary on-anchor.
		{"anniversary day-1, on the 1st", date(2027, 6, 1, 0, ist), 1, ist, true},
		{"anniversary day-15, on the 15th", date(2027, 6, 15, 0, ny), 15, ny, true},

		// Anniversary off-anchor — the seam.
		{"anniversary day-1, on the 30th (seam)", date(2027, 6, 30, 0, ny), 1, ny, false},
		{"anniversary day-15, on the 14th (seam)", date(2027, 6, 14, 0, ny), 15, ny, false},

		// Month-end clamp (ADR-055): a day-31 anchor is ON-anchor at Feb 28 / Jun 30.
		{"day-31 anchor at Jan 31 — on-anchor", date(2027, 1, 31, 0, ny), 31, ny, true},
		{"day-31 anchor at Feb 28 — on-anchor (clamped)", date(2027, 2, 28, 0, ny), 31, ny, true},
		{"day-31 anchor at Jun 30 — on-anchor (clamped)", date(2027, 6, 30, 0, ny), 31, ny, true},
		{"day-31 anchor at Feb 27 — off-anchor", date(2027, 2, 27, 0, ny), 31, ny, false},

		// The real seam instant: Jul 1 00:00 IST == Jun 30 18:30 UTC, resolved in
		// New_York (Jun 30 14:30 EDT), anchor day 1 → off the anchor → seam.
		{"stored IST-midnight boundary re-resolved in NY, day-1 anchor → seam",
			time.Date(2027, 7, 1, 0, 0, 0, 0, ist).UTC(), 1, ny, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsPeriodStartOnAnchor(c.t, c.anchorDay, c.loc); got != c.want {
				t.Errorf("IsPeriodStartOnAnchor(%s, anchor=%d, %s) = %v, want %v",
					c.t.In(c.loc).Format("2006-01-02 15:04 MST"), c.anchorDay, c.loc, got, c.want)
			}
		})
	}
}

func date(y int, m time.Month, d, h int, loc *time.Location) time.Time {
	return time.Date(y, m, d, h, 0, 0, 0, loc).UTC()
}
