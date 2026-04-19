package billing

import (
	"context"
	"fmt"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/platform/money"
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

// PreviewResult is the result of an invoice preview.
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

// Preview generates a dry-run invoice for a subscription without persisting anything.
func (e *Engine) Preview(ctx context.Context, sub domain.Subscription) (PreviewResult, error) {
	if sub.CurrentBillingPeriodStart == nil || sub.CurrentBillingPeriodEnd == nil {
		return PreviewResult{}, fmt.Errorf("subscription has no billing period set")
	}

	periodStart := *sub.CurrentBillingPeriodStart
	periodEnd := *sub.CurrentBillingPeriodEnd

	plan, err := e.pricing.GetPlan(ctx, sub.TenantID, sub.PlanID)
	if err != nil {
		return PreviewResult{}, fmt.Errorf("get plan: %w", err)
	}

	usageTotals, err := e.usage.AggregateForBillingPeriod(ctx, sub.TenantID, sub.ID, plan.MeterIDs, periodStart, periodEnd)
	if err != nil {
		return PreviewResult{}, fmt.Errorf("aggregate usage: %w", err)
	}

	result := PreviewResult{
		CustomerID:         sub.CustomerID,
		SubscriptionID:     sub.ID,
		PlanName:           plan.Name,
		Currency:           plan.Currency,
		BillingPeriodStart: periodStart,
		BillingPeriodEnd:   periodEnd,
		GeneratedAt:        e.clock.Now(),
	}

	// Base fee
	if plan.BaseAmountCents > 0 {
		result.Lines = append(result.Lines, PreviewLine{
			LineType:        "base_fee",
			Description:     fmt.Sprintf("%s - base fee", plan.Name),
			Quantity:        1,
			UnitAmountCents: plan.BaseAmountCents,
			AmountCents:     plan.BaseAmountCents,
		})
		result.SubtotalCents += plan.BaseAmountCents
	}

	// Usage lines
	for _, meterID := range plan.MeterIDs {
		quantity := usageTotals[meterID]
		if quantity == 0 {
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

		unitAmount := int64(0)
		if quantity > 0 {
			unitAmount = money.RoundHalfToEven(amount, quantity)
		}

		result.Lines = append(result.Lines, PreviewLine{
			LineType:        "usage",
			Description:     fmt.Sprintf("%s - %d %s", meter.Name, quantity, meter.Unit),
			MeterID:         meterID,
			Quantity:        quantity,
			UnitAmountCents: unitAmount,
			AmountCents:     amount,
			PricingMode:     string(rule.Mode),
		})
		result.SubtotalCents += amount
	}

	return result, nil
}
