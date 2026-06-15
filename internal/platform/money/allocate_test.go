package money

import "testing"

// TestAllocateByWeight pins the largest-remainder partition. The critical
// property: the parts ALWAYS sum to the input total exactly — this is what
// stops a credit fanned across funding invoices from over-crediting (an
// independent per-bucket recompute would double-count; a partition cannot).
func TestAllocateByWeight(t *testing.T) {
	sum := func(xs []int64) int64 {
		var s int64
		for _, x := range xs {
			s += x
		}
		return s
	}

	t.Run("two equal weights split evenly", func(t *testing.T) {
		got := AllocateByWeight(4000, []int64{4000, 4000})
		if got[0] != 2000 || got[1] != 2000 {
			t.Fatalf("got %v, want [2000 2000]", got)
		}
	})

	t.Run("over-credit guard: raw weights summing to 2x total still partition to total", func(t *testing.T) {
		// upgrade→downgrade→cancel shape: engine total $40, each funding invoice
		// independently looks like $40 unused. A naive recompute credits $80; the
		// partition must scale both down so the sum is exactly $40.
		got := AllocateByWeight(4000, []int64{4000, 4000})
		if s := sum(got); s != 4000 {
			t.Fatalf("sum=%d, want 4000 (no over-credit)", s)
		}
	})

	t.Run("uneven weights, residual cent to largest remainder", func(t *testing.T) {
		got := AllocateByWeight(100, []int64{1, 2})
		if sum(got) != 100 {
			t.Fatalf("sum=%d, want 100", sum(got))
		}
		if got[1] < got[0] {
			t.Fatalf("got %v, want larger share in bucket 1", got)
		}
	})

	t.Run("realistic upgrade→cancel: $153.33 across base+upgrade unused weights", func(t *testing.T) {
		got := AllocateByWeight(15333, []int64{7667, 7666})
		if sum(got) != 15333 {
			t.Fatalf("sum=%d, want 15333", sum(got))
		}
		if got[0] <= 0 || got[1] <= 0 {
			t.Fatalf("both funding invoices must receive a share, got %v", got)
		}
	})

	t.Run("zero weights → all to bucket 0, never negative", func(t *testing.T) {
		got := AllocateByWeight(500, []int64{0, 0})
		if got[0] != 500 || got[1] != 0 {
			t.Fatalf("got %v, want [500 0]", got)
		}
	})

	t.Run("one zero weight gets nothing", func(t *testing.T) {
		got := AllocateByWeight(900, []int64{0, 3})
		if got[0] != 0 || got[1] != 900 {
			t.Fatalf("got %v, want [0 900]", got)
		}
	})

	t.Run("single source receives the whole total", func(t *testing.T) {
		got := AllocateByWeight(12345, []int64{999})
		if len(got) != 1 || got[0] != 12345 {
			t.Fatalf("got %v, want [12345]", got)
		}
	})
}
