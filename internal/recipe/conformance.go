package recipe

import (
	"fmt"
	"strings"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// This file implements the recipe METER ADOPTION GUARD. When Instantiate finds
// an existing meter by key, it may adopt it only if the existing meter's
// AGGREGATION matches what the recipe declares. Aggregation (sum/count/max/
// last) is the sole billing-consulted meter field — engine.go's deferredBucket/
// mapMeterAggregation drive usage roll-up off it — so adopting a same-key meter
// with a divergent aggregation would silently mis-bill. A divergent match is
// therefore REFUSED, not silently adopted.
//
// There is deliberately NO live-subscription clause: adoption only READS the
// meter (to wire a fresh plan and append disjoint pricing bindings), never
// mutates it, so aggregation-match is the complete safety condition — and it
// lets a second AI recipe adopt the shared ADR-044 `tokens` meter after
// go-live. Plans are never adopted (each install generates a fresh born-unique
// plan, ADR-085); rating-rule adoption is never gated (a re-apply must not
// reprice live subs, ADR-070). Name/Unit are display labels, not
// billing-consulted.

// fieldDiff is one billing-affecting field where an existing object diverges
// from what the recipe declares. Want is the recipe's value, Got the existing.
type fieldDiff struct {
	Field string
	Want  string
	Got   string
}

// meterConformanceDiff returns billing-affecting divergence between an existing
// meter (matched by key) and the recipe's declared meter. Only Aggregation is
// compared — the sole billing-consulted field.
func meterConformanceDiff(existing domain.Meter, rm domain.RecipeMeter) []fieldDiff {
	var diffs []fieldDiff
	if existing.Aggregation != rm.Aggregation {
		diffs = append(diffs, fieldDiff{"usage aggregation", rm.Aggregation, existing.Aggregation})
	}
	return diffs
}

// formatDiffs renders operator-facing "field: recipe wants X, existing is Y" copy.
func formatDiffs(diffs []fieldDiff) string {
	parts := make([]string, 0, len(diffs))
	for _, d := range diffs {
		parts = append(parts, fmt.Sprintf("%s: recipe wants %s, existing is %s", d.Field, d.Want, d.Got))
	}
	return strings.Join(parts, "; ")
}
