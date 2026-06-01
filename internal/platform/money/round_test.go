package money

import "testing"

func TestRoundHalfToEven(t *testing.T) {
	cases := []struct {
		name       string
		num, denom int64
		want       int64
	}{
		{"clean division", 100, 10, 10},
		{"round down below half", 19, 10, 2},
		{"round up above half", 21, 10, 2},
		{"tie → even (2 stays 2)", 25, 10, 2},
		{"tie → even (3 bumps to 4)", 35, 10, 4},
		{"tie → even (4 stays 4)", 45, 10, 4},
		{"tie → even (5 bumps to 6)", 55, 10, 6},
		{"zero numerator", 0, 100, 0},
		{"denom of 1 is identity", 42, 1, 42},
		// Half-up vs banker's divergence: 25/10 would be 3 under half-up,
		// 2 under banker's. Guards against regression to math.Round-equivalent.
		{"banker's diverges from half-up at 2.5", 25, 10, 2},
		{"banker's diverges from half-up at 4.5", 45, 10, 4},

		// Negative numerators (downgrade / quantity-reduction proration credits).
		// Pre-fix these truncated toward zero, understating the credit by up to
		// 1 cent. Must mirror the positive cases by magnitude with the sign
		// reapplied (half to even on the absolute value).
		{"negative clean division", -100, 10, -10},
		{"negative round down below half", -19, 10, -2},
		{"negative round up above half", -21, 10, -2},
		{"negative tie → even (-2.5 stays -2)", -25, 10, -2},
		{"negative tie → even (-3.5 bumps to -4)", -35, 10, -4},
		{"negative tie → even (-4.5 stays -4)", -45, 10, -4},
		{"negative identity", -42, 1, -42},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RoundHalfToEven(tc.num, tc.denom); got != tc.want {
				t.Errorf("RoundHalfToEven(%d, %d) = %d, want %d", tc.num, tc.denom, got, tc.want)
			}
		})
	}
}
