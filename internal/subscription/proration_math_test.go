package subscription

import (
	"math/big"
	"testing"
)

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
