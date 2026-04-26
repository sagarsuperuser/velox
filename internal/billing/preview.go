package billing

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// PreviewLine is one line of an invoice preview. Mirrors the line shape the
// cycle scan persists onto invoice_line_items, with two differences:
//
//   - Quantity is a decimal string on the wire (NUMERIC(38,12) precision
//     preserved end-to-end — fractional AI-usage primitives like GPU-hours
//     and cached-token ratios round-trip without precision loss).
//   - Multi-rule meters emit one line per (meter, rule) pair with
//     DimensionMatch echoed from the meter pricing rule (the canonical
//     pricing identity). Single-rule meters keep the one-line-per-meter
//     shape with no DimensionMatch.
//
// See docs/design-create-preview.md.
type PreviewLine struct {
	LineType            string          `json:"line_type"`
	Description         string          `json:"description"`
	MeterID             string          `json:"meter_id,omitempty"`
	RatingRuleVersionID string          `json:"rating_rule_version_id,omitempty"`
	RuleKey             string          `json:"rule_key,omitempty"`
	DimensionMatch      map[string]any  `json:"dimension_match,omitempty"`
	Currency            string          `json:"currency"`
	Quantity            decimal.Decimal `json:"quantity"`
	UnitAmountCents     int64           `json:"unit_amount_cents"`
	AmountCents         int64           `json:"amount_cents"`
	PricingMode         string          `json:"pricing_mode,omitempty"`
}

// PreviewTotal is one currency's roll-up across the preview's lines. We
// always emit a list (one entry per distinct currency) even when there's
// only one currency — consistent shape lets clients read totals[0]
// without branching on cardinality, and lines up with the customer-usage
// endpoint's totals shape so a single TS type covers both surfaces.
type PreviewTotal struct {
	Currency    string `json:"currency"`
	AmountCents int64  `json:"amount_cents"`
}

// PreviewResult is the response shape for both the in-app
// GET /v1/billing/preview/{subscription_id} debug route and the public
// POST /v1/invoices/create_preview surface. PlanName is a joined list of
// the subscription's item plans (comma-separated) — the previous
// single-plan shape can't represent a multi-item sub.
type PreviewResult struct {
	CustomerID         string         `json:"customer_id"`
	SubscriptionID     string         `json:"subscription_id"`
	PlanName           string         `json:"plan_name"`
	BillingPeriodStart time.Time      `json:"billing_period_start"`
	BillingPeriodEnd   time.Time      `json:"billing_period_end"`
	Lines              []PreviewLine  `json:"lines"`
	Totals             []PreviewTotal `json:"totals"`
	Warnings           []string       `json:"warnings"`
	GeneratedAt        time.Time      `json:"generated_at"`
}

// Preview generates a dry-run invoice for a subscription without persisting
// anything. Walks every item, resolves its plan, and emits a base_fee line
// per item plus per-(meter, rule) usage lines via the priority+claim
// LATERAL JOIN — same code path the cycle scan uses, so preview math ==
// invoice math.
func (e *Engine) Preview(ctx context.Context, sub domain.Subscription) (PreviewResult, error) {
	if sub.CurrentBillingPeriodStart == nil || sub.CurrentBillingPeriodEnd == nil {
		return PreviewResult{}, fmt.Errorf("subscription has no billing period set")
	}
	if len(sub.Items) == 0 {
		return PreviewResult{}, fmt.Errorf("subscription has no items")
	}
	periodStart := *sub.CurrentBillingPeriodStart
	periodEnd := *sub.CurrentBillingPeriodEnd
	return e.previewWithWindow(ctx, sub, periodStart, periodEnd)
}

// previewWithWindow is the explicit-period variant. Used by the
// create_preview service when the caller passes a custom window; the
// wall-clock Preview entry above defers to the subscription's current
// cycle. Both paths converge here so the per-line composition stays in
// one place.
func (e *Engine) previewWithWindow(ctx context.Context, sub domain.Subscription, periodStart, periodEnd time.Time) (PreviewResult, error) {
	plans := make(map[string]domain.Plan, len(sub.Items))
	planNames := make([]string, 0, len(sub.Items))
	var allMeterIDs []string
	seenMeter := make(map[string]struct{})
	for _, it := range sub.Items {
		if _, ok := plans[it.PlanID]; ok {
			continue
		}
		pl, err := e.pricing.GetPlan(ctx, sub.TenantID, it.PlanID)
		if err != nil {
			return PreviewResult{}, fmt.Errorf("get plan %s: %w", it.PlanID, err)
		}
		plans[it.PlanID] = pl
		planNames = append(planNames, pl.Name)
		for _, mid := range pl.MeterIDs {
			if _, ok := seenMeter[mid]; ok {
				continue
			}
			seenMeter[mid] = struct{}{}
			allMeterIDs = append(allMeterIDs, mid)
		}
	}

	result := PreviewResult{
		CustomerID:         sub.CustomerID,
		SubscriptionID:     sub.ID,
		PlanName:           strings.Join(planNames, ", "),
		BillingPeriodStart: periodStart,
		BillingPeriodEnd:   periodEnd,
		Lines:              []PreviewLine{},
		Warnings:           []string{},
		GeneratedAt:        e.clock.Now(),
	}

	// Base-fee lines from each subscription item's plan. Quantity here is
	// the item's seat / count (always integer); we still expose it as a
	// decimal for shape uniformity.
	for _, it := range sub.Items {
		plan := plans[it.PlanID]
		if plan.BaseAmountCents <= 0 {
			continue
		}
		baseFee := plan.BaseAmountCents * it.Quantity
		result.Lines = append(result.Lines, PreviewLine{
			LineType:        "base_fee",
			Description:     fmt.Sprintf("%s - base fee (qty %d)", plan.Name, it.Quantity),
			Currency:        plan.Currency,
			Quantity:        decimal.NewFromInt(it.Quantity),
			UnitAmountCents: plan.BaseAmountCents,
			AmountCents:     baseFee,
		})
	}

	// Usage lines per (meter, rule). The priority+claim LATERAL JOIN inside
	// AggregateByPricingRules is the canonical pricing identity — the
	// cycle scan calls the same path, so a multi-dim tenant's preview
	// matches what its actual invoice will be.
	for _, meterID := range allMeterIDs {
		meterLines, warnings, err := e.previewMeter(ctx, sub.TenantID, sub.CustomerID, meterID, periodStart, periodEnd)
		if err != nil {
			// Soft-fail: a missing meter / rule shouldn't abort the
			// whole preview. Surface as a warning and continue —
			// matches the existing behaviour of the legacy preview
			// path which silently skipped on errors here.
			result.Warnings = append(result.Warnings, fmt.Sprintf("meter %q: %s", meterID, err.Error()))
			continue
		}
		result.Lines = append(result.Lines, meterLines...)
		result.Warnings = append(result.Warnings, warnings...)
	}

	result.Totals = computePreviewTotals(result.Lines)
	return result, nil
}

// previewMeter rates one meter's events for the customer over [from, to)
// and emits one PreviewLine per rule bucket. Mirrors usage.rateMeter from
// internal/usage/customer_usage.go — the cycle scan calls the same
// AggregateByPricingRules + ComputeAmountCents path, so each line
// produced here matches the invoice_line_item the cycle scan would
// persist.
func (e *Engine) previewMeter(ctx context.Context, tenantID, customerID, meterID string, from, to time.Time) ([]PreviewLine, []string, error) {
	meter, err := e.pricing.GetMeter(ctx, tenantID, meterID)
	if err != nil {
		return nil, nil, fmt.Errorf("get meter: %w", err)
	}

	defaultMode := mapMeterAggregation(meter.Aggregation)
	aggs, err := e.usage.AggregateByPricingRules(ctx, tenantID, customerID, meterID, defaultMode, from, to)
	if err != nil {
		return nil, nil, fmt.Errorf("aggregate by pricing rules: %w", err)
	}

	rules, err := e.pricing.ListMeterPricingRulesByMeter(ctx, tenantID, meterID)
	if err != nil {
		return nil, nil, fmt.Errorf("list pricing rules: %w", err)
	}
	rulesByID := make(map[string]domain.MeterPricingRule, len(rules))
	for _, rule := range rules {
		rulesByID[rule.ID] = rule
	}

	var lines []PreviewLine
	var warnings []string

	for _, agg := range aggs {
		if agg.Quantity.IsZero() {
			continue
		}

		ratingRuleID := agg.RatingRuleVersionID
		if ratingRuleID == "" {
			ratingRuleID = meter.RatingRuleVersionID
		}
		if ratingRuleID == "" {
			warnings = append(warnings, fmt.Sprintf("meter %q has events with no rating rule binding — skipped", meter.Key))
			continue
		}

		ratingRule, err := e.pricing.GetRatingRule(ctx, tenantID, ratingRuleID)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("meter %q rule %q: rating rule not found", meter.Key, ratingRuleID))
			continue
		}

		// Honour customer price overrides — same precedent as the cycle
		// scan and the existing Preview path. An override returns a
		// patched RatingRuleVersion with the override's tier table /
		// flat amount in place. e.pricing.GetOverride returns an error
		// when no override exists; we silently fall through.
		if override, overrideErr := e.pricing.GetOverride(ctx, tenantID, customerID, ratingRuleID); overrideErr == nil && override.Active {
			ratingRule = override.ToRatingRule()
		}

		amount, err := domain.ComputeAmountCents(ratingRule, agg.Quantity)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("meter %q rule %q: rating failed: %s", meter.Key, ratingRule.RuleKey, err.Error()))
			continue
		}

		// Unit amount is amount / quantity, rounded to nearest cent. The
		// IsZero short-circuit above guarantees the divisor is non-zero.
		unitAmount := decimal.NewFromInt(amount).Div(agg.Quantity).RoundBank(0).IntPart()

		var dimMatch map[string]any
		if agg.RuleID != "" {
			if rule, ok := rulesByID[agg.RuleID]; ok && len(rule.DimensionMatch) > 0 {
				dimMatch = rule.DimensionMatch
			}
		}

		desc := fmt.Sprintf("%s - %s %s", meter.Name, agg.Quantity.String(), meter.Unit)

		lines = append(lines, PreviewLine{
			LineType:            "usage",
			Description:         desc,
			MeterID:             meterID,
			RatingRuleVersionID: ratingRule.ID,
			RuleKey:             ratingRule.RuleKey,
			DimensionMatch:      dimMatch,
			Currency:            ratingRule.Currency,
			Quantity:            agg.Quantity,
			UnitAmountCents:     unitAmount,
			AmountCents:         amount,
			PricingMode:         string(ratingRule.Mode),
		})
	}

	return lines, warnings, nil
}

// computePreviewTotals groups each line's amount by currency and emits
// one PreviewTotal per distinct currency. Order is insertion-stable
// (first currency seen wins position 0) so the response is deterministic
// across calls. Mirrors the customer-usage computeTotals helper.
func computePreviewTotals(lines []PreviewLine) []PreviewTotal {
	bucket := map[string]int64{}
	var order []string
	for _, line := range lines {
		if line.Currency == "" {
			continue
		}
		if _, ok := bucket[line.Currency]; !ok {
			order = append(order, line.Currency)
		}
		bucket[line.Currency] += line.AmountCents
	}
	out := make([]PreviewTotal, 0, len(order))
	for _, cur := range order {
		out = append(out, PreviewTotal{Currency: cur, AmountCents: bucket[cur]})
	}
	return out
}

// mapMeterAggregation translates the meter's stored aggregation string
// ("sum"/"count"/"max"/"last") to the AggregationMode the priority+claim
// resolver accepts. Duplicated from internal/usage/customer_usage.go
// rather than imported to keep the billing package's dependency graph
// flat (no cross-domain imports between peer packages — see CLAUDE.md
// "Architecture").
func mapMeterAggregation(agg string) domain.AggregationMode {
	switch agg {
	case "sum":
		return domain.AggSum
	case "count":
		return domain.AggCount
	case "max":
		return domain.AggMax
	case "last":
		return domain.AggLastDuringPeriod
	default:
		return domain.AggSum
	}
}
