package billing

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// PreviewLine represents one line in an invoice preview.
type PreviewLine struct {
	LineType        string `json:"line_type"`
	Description     string `json:"description"`
	MeterID         string `json:"meter_id,omitempty"`
	Quantity        int64  `json:"quantity"`
	UnitAmountCents int64  `json:"unit_amount_cents"`
	AmountCents     int64  `json:"amount_cents"`
	PricingMode     string `json:"pricing_mode,omitempty"`
}

// PreviewResult is the result of an invoice preview. PlanName is a joined
// list of the subscription's item plans (comma-separated) — the previous
// single-plan shape can't represent a multi-item sub, and exposing the raw
// item list here would leak more than preview consumers need.
type PreviewResult struct {
	CustomerID         string        `json:"customer_id"`
	SubscriptionID     string        `json:"subscription_id"`
	PlanName           string        `json:"plan_name"`
	Currency           string        `json:"currency"`
	BillingPeriodStart time.Time     `json:"billing_period_start"`
	BillingPeriodEnd   time.Time     `json:"billing_period_end"`
	Lines              []PreviewLine `json:"lines"`
	SubtotalCents      int64         `json:"subtotal_cents"`
	GeneratedAt        time.Time     `json:"generated_at"`
}

// Preview generates a dry-run invoice for a subscription without persisting
// anything. Walks every item, resolves its plan, and emits a base_fee line
// per item plus deduped usage lines across the union of meter_ids.
func (e *Engine) Preview(ctx context.Context, sub domain.Subscription) (PreviewResult, error) {
	if sub.CurrentBillingPeriodStart == nil || sub.CurrentBillingPeriodEnd == nil {
		return PreviewResult{}, fmt.Errorf("subscription has no billing period set")
	}
	if len(sub.Items) == 0 {
		return PreviewResult{}, fmt.Errorf("subscription has no items")
	}

	periodStart := *sub.CurrentBillingPeriodStart
	periodEnd := *sub.CurrentBillingPeriodEnd

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

	currency := plans[sub.Items[0].PlanID].Currency

	usageTotals, err := e.usage.AggregateForBillingPeriod(ctx, sub.TenantID, sub.ID, allMeterIDs, periodStart, periodEnd)
	if err != nil {
		return PreviewResult{}, fmt.Errorf("aggregate usage: %w", err)
	}

	result := PreviewResult{
		CustomerID:         sub.CustomerID,
		SubscriptionID:     sub.ID,
		PlanName:           strings.Join(planNames, ", "),
		Currency:           currency,
		BillingPeriodStart: periodStart,
		BillingPeriodEnd:   periodEnd,
		GeneratedAt:        e.clock.Now(),
	}

	for _, it := range sub.Items {
		plan := plans[it.PlanID]
		if plan.BaseAmountCents <= 0 {
			continue
		}
		baseFee := plan.BaseAmountCents * it.Quantity
		result.Lines = append(result.Lines, PreviewLine{
			LineType:        "base_fee",
			Description:     fmt.Sprintf("%s - base fee (qty %d)", plan.Name, it.Quantity),
			Quantity:        it.Quantity,
			UnitAmountCents: plan.BaseAmountCents,
			AmountCents:     baseFee,
		})
		result.SubtotalCents += baseFee
	}

	rendered := make(map[string]struct{})
	for _, meterID := range allMeterIDs {
		if _, ok := rendered[meterID]; ok {
			continue
		}
		rendered[meterID] = struct{}{}

		quantity := usageTotals[meterID]
		if quantity.IsZero() {
			continue
		}

		meter, err := e.pricing.GetMeter(ctx, sub.TenantID, meterID)
		if err != nil || meter.RatingRuleVersionID == "" {
			continue
		}

		rule, err := e.pricing.GetRatingRule(ctx, sub.TenantID, meter.RatingRuleVersionID)
		if err != nil {
			continue
		}

		override, overrideErr := e.pricing.GetOverride(ctx, sub.TenantID, sub.CustomerID, meter.RatingRuleVersionID)
		if overrideErr == nil && override.Active {
			rule = override.ToRatingRule()
		}

		amount, err := domain.ComputeAmountCents(rule, quantity)
		if err != nil {
			continue
		}

		// quantity is decimal here; the IsZero short-circuit above
		// guarantees it is non-zero so unit-amount division is safe.
		unitAmount := decimal.NewFromInt(amount).Div(quantity).RoundBank(0).IntPart()

		result.Lines = append(result.Lines, PreviewLine{
			LineType: "usage",
			// Quantity column on previews is integer for now — fractional
			// values are truncated in the human description but the
			// AmountCents above is computed from the full decimal quantity.
			Description:     fmt.Sprintf("%s - %s %s", meter.Name, quantity.String(), meter.Unit),
			MeterID:         meterID,
			Quantity:        quantity.IntPart(),
			UnitAmountCents: unitAmount,
			AmountCents:     amount,
			PricingMode:     string(rule.Mode),
		})
		result.SubtotalCents += amount
	}

	return result, nil
}
