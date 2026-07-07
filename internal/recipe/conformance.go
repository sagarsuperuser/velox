package recipe

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// This file implements the recipe ADOPTION CONFORMANCE GATE (ADR-083). When
// Instantiate finds an existing plan (by code) or meter (by key), it may only
// ADOPT it if the existing object matches what the recipe DECLARES. Adopting a
// divergent object would silently wire the recipe's meters/pricing rules onto a
// plan/meter the operator never declared — billing under a timing/amount/
// aggregation they didn't ask for. That is a silent-wrong-billing bug on the
// money path, so a divergent match is REFUSED, not silently adopted.
//
// Only MONEY/BEHAVIOR-affecting fields are compared. Cosmetic, operator-owned
// fields (plan Name/Description/Status/TaxCode, meter Name/Unit) are ignored so
// a rename/relabel never blocks a legitimate reinstall. Rating-rule adoption is
// deliberately NOT gated (a reinstall must not reprice live subs — ADR-070).

// fieldDiff is one money-affecting field where an existing object diverges from
// what the recipe declares. Want is the recipe's (effective) value, Got the
// existing object's.
type fieldDiff struct {
	Field string
	Want  string
	Got   string
}

// planConformanceDiff returns the money-affecting fields where an existing plan
// (matched by code) diverges from the recipe's declared plan. Empty => safe to
// adopt (idempotent reinstall). Non-empty => refuse.
//
// The recipe's EFFECTIVE spec is explicit RecipePlan fields PLUS implicit
// defaults: RecipePlan carries no base_bill_timing, and CreatePlanTx defaults
// empty timing to domain.BillInArrears (internal/pricing/postgres.go), so the
// declared timing is unambiguously in_arrears — referenced via the same
// constant so the spec and the create-default cannot drift. wantMeterIDs is the
// set of meter IDs the recipe wires this plan to (resolved by the caller from
// the meter-adoption pass), compared order-insensitively.
func planConformanceDiff(existing domain.Plan, rp domain.RecipePlan, wantMeterIDs []string) []fieldDiff {
	var diffs []fieldDiff
	if existing.Currency != rp.Currency {
		diffs = append(diffs, fieldDiff{"currency", rp.Currency, existing.Currency})
	}
	if existing.BillingInterval != rp.BillingInterval {
		diffs = append(diffs, fieldDiff{"billing interval", string(rp.BillingInterval), string(existing.BillingInterval)})
	}
	if existing.BaseAmountCents != rp.BaseAmountCents {
		diffs = append(diffs, fieldDiff{"base fee (cents)", fmt.Sprintf("%d", rp.BaseAmountCents), fmt.Sprintf("%d", existing.BaseAmountCents)})
	}
	// Recipe declares no timing → effective spec is the CreatePlanTx default.
	if effectiveTiming(existing.BaseBillTiming) != domain.BillInArrears {
		diffs = append(diffs, fieldDiff{"base fee timing", string(domain.BillInArrears), string(effectiveTiming(existing.BaseBillTiming))})
	}
	if !sameStringSet(existing.MeterIDs, wantMeterIDs) {
		diffs = append(diffs, fieldDiff{"metered features", fmt.Sprintf("%v", sortedSet(wantMeterIDs)), fmt.Sprintf("%v", sortedSet(existing.MeterIDs))})
	}
	return diffs
}

// meterConformanceDiff returns money-affecting divergence between an existing
// meter (matched by key) and the recipe's declared meter. Only Aggregation is
// compared: it drives how usage rolls up (sum/count/max/last — engine.go's
// deferredBucket/mapMeterAggregation), so an adopted meter with a different
// aggregation silently mis-bills. Name/Unit are display labels, not
// billing-consulted.
func meterConformanceDiff(existing domain.Meter, rm domain.RecipeMeter) []fieldDiff {
	var diffs []fieldDiff
	if existing.Aggregation != rm.Aggregation {
		diffs = append(diffs, fieldDiff{"usage aggregation", rm.Aggregation, existing.Aggregation})
	}
	return diffs
}

// effectiveTiming normalizes an empty base_bill_timing to the CreatePlanTx
// default, so a legacy/empty existing value doesn't read as a false divergence.
func effectiveTiming(t domain.BillTiming) domain.BillTiming {
	if t == "" {
		return domain.BillInArrears
	}
	return t
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as, bs := sortedSet(a), sortedSet(b)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

func sortedSet(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

// formatDiffs renders operator-facing "field: want=X, got=Y" copy.
func formatDiffs(diffs []fieldDiff) string {
	parts := make([]string, 0, len(diffs))
	for _, d := range diffs {
		parts = append(parts, fmt.Sprintf("%s: recipe wants %s, existing is %s", d.Field, d.Want, d.Got))
	}
	return strings.Join(parts, "; ")
}
