package money

import "sort"

// AllocateByWeight splits `total` across len(weights) buckets in proportion to
// weights using the largest-remainder method, so the parts sum to `total`
// EXACTLY (no rounding drift) and each part is >= 0.
//
// This is the primitive that lets a single authoritative figure (e.g. a
// period's unused-prepayment credit) be fanned across multiple targets (the
// invoices that funded the period) without over- or under-counting: the one
// authoritative total is PARTITIONED, never recomputed independently per
// bucket. Independent per-bucket recompute double-counts after a reversing
// change (e.g. upgrade→downgrade→cancel); a partition cannot.
//
// Zero-weight buckets receive nothing; if every weight is zero the whole total
// lands in bucket 0. `total` is assumed non-negative.
func AllocateByWeight(total int64, weights []int64) []int64 {
	out := make([]int64, len(weights))
	if len(weights) == 0 {
		return out
	}
	var sum int64
	for _, w := range weights {
		if w > 0 {
			sum += w
		}
	}
	if sum <= 0 {
		out[0] = total
		return out
	}
	type rem struct {
		idx int
		r   int64
	}
	rems := make([]rem, 0, len(weights))
	var allocated int64
	for i, w := range weights {
		if w <= 0 {
			rems = append(rems, rem{i, -1}) // never receives a remainder cent
			continue
		}
		num := total * w
		out[i] = num / sum
		allocated += out[i]
		rems = append(rems, rem{i, num % sum})
	}
	// Largest-remainder: hand the leftover cents to the biggest remainders
	// (ties: lower index, via stable sort). leftover < count of positive-weight
	// buckets, so it never reaches a zero-weight (r==-1) bucket.
	leftover := total - allocated
	sort.SliceStable(rems, func(a, b int) bool { return rems[a].r > rems[b].r })
	for k := int64(0); k < leftover && int(k) < len(rems); k++ {
		out[rems[k].idx]++
	}
	return out
}

// AllocateByWeightCapped is AllocateByWeight with a per-bucket ceiling: it
// partitions `total` across buckets in proportion to `weights` but never lets a
// bucket exceed its `caps[i]`, spilling the overflow (water-filling) onto the
// buckets that still have slack — again in weight proportion. Returns the
// allocation and the **unallocatable remainder** (0 when total <= sum(caps);
// positive when the caps genuinely can't absorb `total`, which the caller must
// treat as a loud failure, never a silent drop).
//
// This is what makes the multi-invoice credit fan-out headroom-aware: each
// funding invoice's cap is its REMAINING creditable amount (total minus prior
// non-voided credit notes), so a prior downgrade credit note that shrank one
// invoice's headroom pushes the cancel/clawback share onto the invoices that
// still have room, instead of overrunning a single invoice's credit-note cap.
func AllocateByWeightCapped(total int64, weights, caps []int64) ([]int64, int64) {
	n := len(weights)
	out := make([]int64, n)
	if n == 0 || total <= 0 {
		rem := total
		if rem < 0 {
			rem = 0
		}
		return out, rem
	}
	remaining := total
	// Each round fully distributes `remaining` UNLESS a bucket clamps at its
	// cap; a clamped bucket drops out of the next round, so at most n rounds run
	// before either remaining hits 0 or every bucket is capped.
	for round := 0; round <= n && remaining > 0; round++ {
		var wsum int64
		for i := 0; i < n; i++ {
			if weights[i] > 0 && out[i] < caps[i] {
				wsum += weights[i]
			}
		}
		if wsum <= 0 {
			break // no weighted bucket with slack left
		}
		type rem struct {
			idx int
			r   int64
		}
		rems := make([]rem, 0, n)
		var dealt int64
		for i := 0; i < n; i++ {
			if weights[i] <= 0 || out[i] >= caps[i] {
				continue
			}
			num := remaining * weights[i]
			give := num / wsum
			if slack := caps[i] - out[i]; give > slack {
				give = slack
			}
			out[i] += give
			dealt += give
			rems = append(rems, rem{i, num % wsum})
		}
		remaining -= dealt
		// Largest-remainder leftover cents → active buckets that still have slack.
		sort.SliceStable(rems, func(a, b int) bool { return rems[a].r > rems[b].r })
		for k := 0; k < len(rems) && remaining > 0; k++ {
			i := rems[k].idx
			if out[i] < caps[i] {
				out[i]++
				remaining--
			}
		}
	}
	return out, remaining
}

// AllocateLIFO fills `total` into buckets in the order given — caller passes
// them newest-first — taking up to each bucket's `caps[i]` before spilling to
// the next. Returns the allocation and the unallocatable remainder. This is the
// per-debit / LIFO attribution a plan-downgrade clawback needs: it undoes the
// most-recently-applied price level first (reversing that funding invoice's own
// tax), spilling onto older invoices only when the credited slice genuinely
// exceeds the newest invoice's headroom. (Fungible changes — quantity decrease,
// item removal — use AllocateByWeightCapped instead; there is no principled
// "newest unit".)
func AllocateLIFO(total int64, caps []int64) ([]int64, int64) {
	out := make([]int64, len(caps))
	remaining := total
	for i := 0; i < len(caps) && remaining > 0; i++ {
		give := caps[i]
		if give < 0 {
			give = 0
		}
		if give > remaining {
			give = remaining
		}
		out[i] = give
		remaining -= give
	}
	if remaining < 0 {
		remaining = 0
	}
	return out, remaining
}
