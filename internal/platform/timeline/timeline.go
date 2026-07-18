// Package timeline carries the two ordering invariants every activity /
// timeline surface must satisfy, mechanized after the same two bugs
// shipped on three separate surfaces each (2026-07-19 audit):
//
//  1. SORT AXIS == DISPLAY AXIS, AT FULL PRECISION. A surface orders by
//     the same instant it renders — the simulated effect time on
//     clock-pinned rows, wall time otherwise — and compares the
//     time.Time, never a serialized copy (RFC3339 strings truncate to
//     seconds, which inverted same-second pairs on the subscription
//     timeline, #518, and the invoice timeline, #521).
//  2. EXACT TIES ORDER CAUSALLY. On a frozen test clock an entire close
//     cascade (finalize → dunning start → retries → escalate →
//     write-off → resolve) legitimately shares ONE instant; insertion
//     order there is source-major, which rendered "Marked
//     uncollectible" above the escalation that caused it. Ties need an
//     explicit causal rule — a rank, a creation-ordered id, a
//     full-precision recorded-at — chosen by the surface.
//
// SortStable is deliberately shaped so a new surface cannot adopt it
// without answering both questions: what instant do I display, and how
// do exact ties order?
package timeline

import (
	"sort"
	"time"
)

// SortStable orders events ascending by their displayed instant `at`
// (full precision), consulting `tieLess` ONLY on exact equality. The
// sort is stable, so rows equal under both axes keep insertion order.
//
// `at` must return the instant the surface DISPLAYS for the row — if
// these diverge, the rendered order contradicts the rendered
// timestamps, which is the class this package exists to prevent.
func SortStable[E any](events []E, at func(E) time.Time, tieLess func(a, b E) bool) {
	sort.SliceStable(events, func(i, j int) bool {
		ai, aj := at(events[i]), at(events[j])
		if !ai.Equal(aj) {
			return ai.Before(aj)
		}
		return tieLess(events[i], events[j])
	})
}
