package subscription

import (
	"math/big"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// TestFullBillingCycleDays pins the proration denominator to the full
// anniversary cycle from periodStart, regardless of how short the current
// (stub) period is. Month length varies, so the value depends on periodStart.
func TestFullBillingCycleDays(t *testing.T) {
	utc := time.UTC
	cases := []struct {
		name     string
		start    time.Time
		interval domain.BillingInterval
		want     int64
	}{
		{"monthly anchored mid-month (Apr 16 → May 16 = 30)", time.Date(2027, 4, 16, 18, 30, 0, 0, utc), domain.BillingMonthly, 30},
		{"monthly anchored Jan 1 (→ Feb 1 = 31)", time.Date(2026, 1, 1, 0, 0, 0, 0, utc), domain.BillingMonthly, 31},
		{"monthly anchored Feb 1 non-leap (→ Mar 1 = 28)", time.Date(2026, 2, 1, 0, 0, 0, 0, utc), domain.BillingMonthly, 28},
		{"monthly anchored Feb 1 leap (→ Mar 1 = 29)", time.Date(2028, 2, 1, 0, 0, 0, 0, utc), domain.BillingMonthly, 29},
		{"yearly anchored Jan 1 (→ Jan 1 = 365)", time.Date(2026, 1, 1, 0, 0, 0, 0, utc), domain.BillingYearly, 365},
		{"yearly anchored in a leap year (366)", time.Date(2028, 1, 1, 0, 0, 0, 0, utc), domain.BillingYearly, 366},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fullBillingCycleDays(tc.start, tc.interval); got != tc.want {
				t.Errorf("fullBillingCycleDays(%v, %s) = %d, want %d", tc.start, tc.interval, got, tc.want)
			}
		})
	}
}

// TestProration_StubPeriod_DividesByFullCycle is the regression guard for the
// stub-period overcharge: an upgrade on a 14-day stub of a 30-day monthly
// cycle must prorate the delta against the FULL 30-day cycle, not the 14-day
// stub. Models the exact real-data case (Start $20 → Pro $50, 13 of 14 stub
// days remaining): correct $13.00, NOT the buggy $27.86.
func TestProration_StubPeriod_DividesByFullCycle(t *testing.T) {
	periodStart := time.Date(2027, 4, 16, 18, 30, 0, 0, time.UTC) // 14-day stub to Apr 30
	fullCycle := fullBillingCycleDays(periodStart, domain.BillingMonthly)
	if fullCycle != 30 {
		t.Fatalf("fullCycle = %d, want 30", fullCycle)
	}
	const oldAmt, newAmt, remaining = int64(2000), int64(5000), int64(13)

	got := prorationCents(oldAmt, newAmt, remaining, fullCycle)
	if got != 1300 {
		t.Errorf("stub upgrade proration = %d, want 1300 ((5000-2000)×13/30)", got)
	}
	// Document the bug being prevented: dividing by the 14-day stub gives $27.86.
	if buggy := prorationCents(oldAmt, newAmt, remaining, 14); buggy != 2786 {
		t.Errorf("sanity: stub-denominator value = %d, want 2786 (the overcharge this fix removes)", buggy)
	}
}

// TestProrationCents_ExactCents pins the immediate plan-change proration amount
// to exact cents (no tolerance ranges) — including the B7.4 reference case:
// a 30-day period with 18 days remaining must charge (new-old) × 18 / 30,
// banker's-rounded.
func TestProrationCents_ExactCents(t *testing.T) {
	cases := []struct {
		name                                   string
		oldAmount, newAmount, remaining, total int64
		want                                   int64
	}{
		// B7.4: $20.00 -> $50.00 base, 18 of 30 days remaining.
		// (5000-2000)*18/30 = 54000/30 = 1800 exactly.
		{"B7.4 upgrade $20->$50, 18/30", 2000, 5000, 18, 30, 1800},
		{"no change is zero", 5000, 5000, 18, 30, 0},
		{"full period remaining charges the whole delta", 2000, 5000, 30, 30, 3000},

		// Rounding: 18/30 can never produce a .5 tie (delta*18 is always even,
		// never ≡ 15 mod 30), so these land cleanly above/below half.
		{"rounds up (0.6 -> 1)", 2000, 5001, 18, 30, 1801},   // 3001*18/30 = 1800.6
		{"rounds down (0.4 -> 0)", 2000, 4999, 18, 30, 1799}, // 2999*18/30 = 1799.4

		// Downgrade / quantity reduction -> negative (credit), symmetric.
		{"downgrade $50->$20, 18/30 -> credit", 5000, 2000, 18, 30, -1800},
		{"downgrade rounds toward even on magnitude", 5001, 2000, 18, 30, -1801},

		// totalDays <= 0 guard.
		{"zero total days yields zero", 2000, 5000, 18, 0, 0},

		// Large amount: $0 -> $36,000,000.00 base. 3.6e9 * 18 / 30 = 2.16e9.
		// No int64 overflow (numerator 6.48e10 << 9.2e18), no float drift.
		{"$36M delta stays exact", 0, 3_600_000_000, 18, 30, 2_160_000_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := prorationCents(tc.oldAmount, tc.newAmount, tc.remaining, tc.total); got != tc.want {
				t.Errorf("prorationCents(%d, %d, %d, %d) = %d, want %d",
					tc.oldAmount, tc.newAmount, tc.remaining, tc.total, got, tc.want)
			}
		})
	}
}

// TestProrationCents_BankersTiesFlowThrough verifies half-to-even rounding is
// actually applied by the proration formula (not just by the helper in
// isolation). 18/30 can't tie, so use ratios whose numerator lands exactly on
// a half-cent.
func TestProrationCents_BankersTiesFlowThrough(t *testing.T) {
	cases := []struct {
		name                                   string
		oldAmount, newAmount, remaining, total int64
		want                                   int64
	}{
		{"2.5 -> 2 (even)", 0, 5, 1, 2, 2},
		{"7.5 -> 8 (even)", 0, 15, 1, 2, 8},
		{"3.5 -> 4 (even)", 0, 7, 1, 2, 4},
		{"-2.5 -> -2 (even)", 0, -5, 1, 2, -2},
		{"-7.5 -> -8 (even)", 0, -15, 1, 2, -8},
		// 15*17/30 = 8.5 -> 8 (even); a tie reachable on a real 30-day period.
		{"8.5 -> 8 on a 30-day period", 0, 15, 17, 30, 8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := prorationCents(tc.oldAmount, tc.newAmount, tc.remaining, tc.total); got != tc.want {
				t.Errorf("prorationCents(%d, %d, %d, %d) = %d, want %d (half-to-even)",
					tc.oldAmount, tc.newAmount, tc.remaining, tc.total, got, tc.want)
			}
		})
	}
}

// oracleProration is an independent, overflow-proof reference using math/big.
// It mirrors money.RoundHalfToEven's sign-magnitude half-to-even semantics:
// round the magnitude of (delta*remaining)/total to the nearest integer, ties
// to even, then reapply the sign. Because it computes the numerator in big.Int,
// it can never overflow — so any disagreement with the int64 prorationCents
// signals either a rounding bug or an int64 overflow in the production path.
func oracleProration(delta, remaining, total int64) int64 {
	num := new(big.Int).Mul(big.NewInt(delta), big.NewInt(remaining))
	neg := num.Sign() < 0
	num.Abs(num)
	d := big.NewInt(total)

	q := new(big.Int)
	r := new(big.Int)
	q.QuoRem(num, d, r) // num, d >= 0

	twoR := new(big.Int).Lsh(r, 1) // 2*remainder
	switch twoR.Cmp(d) {
	case 1: // > denom: round up
		q.Add(q, big.NewInt(1))
	case 0: // == denom: tie -> round to even
		if q.Bit(0) == 1 {
			q.Add(q, big.NewInt(1))
		}
	}
	out := q.Int64()
	if neg {
		out = -out
	}
	return out
}

// TestProrationCents_NoDriftAgainstBigIntOracle is the regression guard for the
// "no float64 ULP drift up to ~$36M" property. It sweeps a grid of deltas
// (including amounts up to $36M and near-tie boundaries) across several day
// ratios and asserts the int64 prorationCents equals the exact big.Int oracle
// for every point. Exact agreement proves both that the rounding is correct and
// that the int64 numerator never overflows in range — the guarantee a tolerance
// range can't give.
func TestProrationCents_NoDriftAgainstBigIntOracle(t *testing.T) {
	ratios := [][2]int64{
		{18, 30}, // B7.4
		{1, 30}, {29, 30}, {15, 30},
		{7, 30}, {17, 30}, {1, 7}, {6, 7},
		{16, 31}, {30, 31}, {1, 31},
	}
	// Structured deltas: dense small range + boundary-ish points + up to $36M.
	deltas := make([]int64, 0, 4096)
	for d := int64(0); d <= 2000; d++ { // every cent through $20 (catches all residues)
		deltas = append(deltas, d)
	}
	for d := int64(0); d <= 3_600_000_000; d += 1_234_567 { // sparse sweep to $36M
		deltas = append(deltas, d, d+1, d+2) // jitter to vary residue mod total
	}
	deltas = append(deltas, 3_600_000_000, 3_599_999_999, 1_799_999_999, 2_700_000_001)

	for _, rt := range ratios {
		remaining, total := rt[0], rt[1]
		for _, d := range deltas {
			for _, delta := range []int64{d, -d} { // upgrades and downgrades
				got := prorationCents(0, delta, remaining, total)
				want := oracleProration(delta, remaining, total)
				if got != want {
					t.Fatalf("drift at delta=%d ratio=%d/%d: prorationCents=%d, big.Int oracle=%d",
						delta, remaining, total, got, want)
				}
			}
		}
	}
}

// TestGrossUpByInvoiceRatio pins the net→gross scaling used by the downgrade
// clawback (ADR-048): identity when the source invoice carried no tax, and the
// invoice's Total/Subtotal ratio applied (with banker's rounding) when it did.
func TestGrossUpByInvoiceRatio(t *testing.T) {
	cases := []struct {
		name            string
		net             int64
		subtotal, total int64
		want            int64
	}{
		{"zero-tax invoice → identity", 2000, 6000, 6000, 2000},
		{"no provider (subtotal 0) → identity", 2000, 0, 0, 2000},
		{"10% tax → +10%", 2000, 6000, 6600, 2200},
		{"qty-decrease 10% slice", 1000, 3000, 3300, 1100},
		// 2001 × 6600 / 6000 = 2201.1 → banker's rounds to 2201.
		{"banker's rounding on the slice", 2001, 6000, 6600, 2201},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := grossUpByInvoiceRatio(c.net, c.subtotal, c.total); got != c.want {
				t.Errorf("grossUpByInvoiceRatio(%d, %d, %d) = %d, want %d", c.net, c.subtotal, c.total, got, c.want)
			}
		})
	}
}

// TestClawbackReason locks the per-change-type credit-note reason strings so a
// downgrade, a quantity decrease, and an item removal stay distinguishable on
// the issued credit note (ADR-048).
func TestClawbackReason(t *testing.T) {
	cases := []struct {
		ct   domain.ItemChangeType
		want string
	}{
		{domain.ItemChangeTypePlan, "subscription_downgrade"},
		{domain.ItemChangeTypeQuantity, "subscription_quantity_decrease"},
		{domain.ItemChangeTypeRemove, "subscription_item_removed"},
	}
	for _, c := range cases {
		if got := clawbackReason(c.ct); got != c.want {
			t.Errorf("clawbackReason(%q) = %q, want %q", c.ct, got, c.want)
		}
	}
}
