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
