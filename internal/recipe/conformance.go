package recipe

import (
	"fmt"
	"strings"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// Recipe adoption is gated ONLY on reference data the recipe fully declares and
// that never drifts (ADR-084). Meters are such data: their aggregation
// (sum/max/count/last) is recipe-declared and billing-consulted (engine
// deferredBucket/mapMeterAggregation), so adopting a same-key meter with a
// divergent aggregation silently mis-rolls-up usage → refuse. The PLAN is NOT
// gated: it is operator-owned business config the recipe holds no standing
// claim on, so a pre-existing plan is left untouched and reported, never
// conform-checked (ADR-084 supersedes ADR-083's plan gate). Rating-rule
// adoption stays divergence-tolerant (a reinstall is not a price change, ADR-070).

// fieldDiff is one money/behavior-affecting field where an existing object
// diverges from what the recipe declares. Want is the recipe's value, Got the
// existing object's.
type fieldDiff struct {
	Field string
	Want  string
	Got   string
}

// meterConformanceDiff returns money-affecting divergence between an existing
// meter (matched by key) and the recipe's declared meter. Only Aggregation is
// compared: Name/Unit are display labels, not billing-consulted.
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
