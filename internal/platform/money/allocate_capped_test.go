package money

import "testing"

func sumI(xs []int64) int64 {
	var s int64
	for _, x := range xs {
		s += x
	}
	return s
}

func TestAllocateByWeightCapped(t *testing.T) {
	t.Run("no cap binds → behaves like uncapped, sums to total, no remainder", func(t *testing.T) {
		out, rem := AllocateByWeightCapped(15333, []int64{7667, 7666}, []int64{100000, 100000})
		if rem != 0 {
			t.Fatalf("remainder=%d, want 0", rem)
		}
		if sumI(out) != 15333 {
			t.Fatalf("sum=%d, want 15333", sumI(out))
		}
	})

	t.Run("one cap binds → overflow spills to the slack bucket, still sums to total", func(t *testing.T) {
		// Want ~7667 in bucket0 but its cap is 3000 (a prior credit note shrank
		// its headroom); the 4667 overflow must spill to bucket1.
		out, rem := AllocateByWeightCapped(15333, []int64{7667, 7666}, []int64{3000, 100000})
		if rem != 0 {
			t.Fatalf("remainder=%d, want 0 (slack bucket absorbs overflow)", rem)
		}
		if out[0] > 3000 {
			t.Fatalf("bucket0=%d exceeds its cap 3000", out[0])
		}
		if sumI(out) != 15333 {
			t.Fatalf("sum=%d, want 15333", sumI(out))
		}
		if out[0] != 3000 {
			t.Fatalf("bucket0=%d, want it filled to its cap 3000", out[0])
		}
	})

	t.Run("total exceeds sum(caps) → remainder reported, every bucket at cap", func(t *testing.T) {
		out, rem := AllocateByWeightCapped(10000, []int64{1, 1}, []int64{3000, 4000})
		if rem != 3000 {
			t.Fatalf("remainder=%d, want 3000 (10000 - 7000 caps)", rem)
		}
		if out[0] != 3000 || out[1] != 4000 {
			t.Fatalf("got %v, want [3000 4000] (both at cap)", out)
		}
	})

	t.Run("downgrade→cancel: upgrade invoice headroom reduced by a prior CN spills to base", func(t *testing.T) {
		// base subtotal 10000 (full headroom), upgrade subtotal 8333 but only
		// 2000 headroom left after a downgrade CN. Cancel must still place the
		// full credit, spilling onto the base invoice — not loud-fail.
		out, rem := AllocateByWeightCapped(9000, []int64{10000, 8333}, []int64{10000, 2000})
		if rem != 0 {
			t.Fatalf("remainder=%d, want 0 (base absorbs the spill)", rem)
		}
		if out[1] > 2000 {
			t.Fatalf("upgrade bucket=%d exceeds its reduced headroom 2000", out[1])
		}
		if sumI(out) != 9000 {
			t.Fatalf("sum=%d, want 9000", sumI(out))
		}
	})

	t.Run("never exceeds any cap across adversarial ratios", func(t *testing.T) {
		cases := []struct {
			total         int64
			weights, caps []int64
		}{
			{100, []int64{1, 2, 3}, []int64{10, 10, 10}},
			{29, []int64{5, 5, 5}, []int64{10, 10, 10}},
			{30, []int64{1, 1, 1}, []int64{10, 10, 10}},
			{1, []int64{7, 3}, []int64{1, 1}},
		}
		for _, c := range cases {
			out, rem := AllocateByWeightCapped(c.total, c.weights, c.caps)
			for i := range out {
				if out[i] > c.caps[i] || out[i] < 0 {
					t.Fatalf("case %+v: bucket %d = %d violates cap/non-neg", c, i, out[i])
				}
			}
			if sumI(out)+rem != c.total {
				t.Fatalf("case %+v: sum(%d)+rem(%d) != total(%d)", c, sumI(out), rem, c.total)
			}
		}
	})
}

func TestAllocateLIFO(t *testing.T) {
	t.Run("fills newest-first, spills to older", func(t *testing.T) {
		// caps ordered newest-first: [upgrade=5000, base=10000]. Credit 7000 →
		// 5000 to the upgrade invoice (fully reversed), 2000 spills to base.
		out, rem := AllocateLIFO(7000, []int64{5000, 10000})
		if rem != 0 {
			t.Fatalf("remainder=%d, want 0", rem)
		}
		if out[0] != 5000 || out[1] != 2000 {
			t.Fatalf("got %v, want [5000 2000]", out)
		}
	})

	t.Run("fits entirely in the newest bucket → older untouched", func(t *testing.T) {
		out, rem := AllocateLIFO(3000, []int64{5000, 10000})
		if rem != 0 || out[0] != 3000 || out[1] != 0 {
			t.Fatalf("got %v rem=%d, want [3000 0] rem=0 (base untouched)", out, rem)
		}
	})

	t.Run("exceeds all caps → remainder reported", func(t *testing.T) {
		out, rem := AllocateLIFO(20000, []int64{5000, 10000})
		if rem != 5000 {
			t.Fatalf("remainder=%d, want 5000", rem)
		}
		if out[0] != 5000 || out[1] != 10000 {
			t.Fatalf("got %v, want [5000 10000] (both at cap)", out)
		}
	})
}
