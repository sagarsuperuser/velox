package billing

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// Engine orchestrates the billing cycle: finds subscriptions due for billing,
// aggregates usage, computes charges, and generates invoices with line items.
//
// It coordinates across domain boundaries (subscription, usage, pricing, invoice)
// without those domains knowing about each other.
// SettingsReader reads tenant settings for invoice configuration.
type SettingsReader interface {
	NextInvoiceNumber(ctx context.Context, tenantID string) (string, error)
	Get(ctx context.Context, tenantID string) (domain.TenantSettings, error)
}

type Engine struct {
	subs          SubscriptionReader
	usage         UsageAggregator
	pricing       PricingReader
	invoices      InvoiceWriter
	credits       CreditApplier
	settings      SettingsReader
	paymentSetups PaymentSetupReader
	charger       InvoiceCharger
}

// CreditApplier applies customer credits to an invoice before charging.
type CreditApplier interface {
	ApplyToInvoice(ctx context.Context, tenantID, customerID, invoiceID string, amountCents int64, invoiceNumber ...string) (int64, error)
}

// PaymentSetupReader checks if a customer has a payment method.
type PaymentSetupReader interface {
	GetPaymentSetup(ctx context.Context, tenantID, customerID string) (domain.CustomerPaymentSetup, error)
}

// InvoiceCharger creates a Stripe PaymentIntent for a finalized invoice.
type InvoiceCharger interface {
	ChargeInvoice(ctx context.Context, tenantID string, inv domain.Invoice, stripeCustomerID string) (domain.Invoice, error)
}

// SubscriptionReader reads subscription and plan data for billing.
type SubscriptionReader interface {
	GetDueBilling(ctx context.Context, before time.Time, limit int) ([]domain.Subscription, error)
	Get(ctx context.Context, tenantID, id string) (domain.Subscription, error)
	UpdateBillingCycle(ctx context.Context, tenantID, id string, periodStart, periodEnd, nextBillingAt time.Time) error
}

// UsageAggregator aggregates usage events for a billing period.
type UsageAggregator interface {
	AggregateForBillingPeriod(ctx context.Context, tenantID, subscriptionID string, meterIDs []string, from, to time.Time) (map[string]int64, error)
}

// PricingReader reads plan, rating rule, and override data.
type PricingReader interface {
	GetPlan(ctx context.Context, tenantID, id string) (domain.Plan, error)
	GetMeter(ctx context.Context, tenantID, id string) (domain.Meter, error)
	GetRatingRule(ctx context.Context, tenantID, id string) (domain.RatingRuleVersion, error)
	GetLatestRuleByKey(ctx context.Context, tenantID, ruleKey string) (domain.RatingRuleVersion, error)
	GetOverride(ctx context.Context, tenantID, customerID, ruleID string) (domain.CustomerPriceOverride, error)
}

// InvoiceWriter creates invoices and line items.
type InvoiceWriter interface {
	CreateInvoice(ctx context.Context, tenantID string, inv domain.Invoice) (domain.Invoice, error)
	CreateLineItem(ctx context.Context, tenantID string, item domain.InvoiceLineItem) (domain.InvoiceLineItem, error)
	ApplyCreditAmount(ctx context.Context, tenantID, id string, amountCents int64) (domain.Invoice, error)
	GetInvoice(ctx context.Context, tenantID, id string) (domain.Invoice, error)
}

func NewEngine(subs SubscriptionReader, usage UsageAggregator, pricing PricingReader, invoices InvoiceWriter, credits CreditApplier, settings SettingsReader, paymentSetups PaymentSetupReader, charger InvoiceCharger) *Engine {
	return &Engine{subs: subs, usage: usage, pricing: pricing, invoices: invoices, credits: credits, settings: settings, paymentSetups: paymentSetups, charger: charger}
}

// RunCycle finds all subscriptions due for billing and generates invoices.
// Returns the number of invoices generated and any errors encountered.
func (e *Engine) RunCycle(ctx context.Context, batchSize int) (int, []error) {
	if batchSize <= 0 {
		batchSize = 50
	}

	due, err := e.subs.GetDueBilling(ctx, time.Now().UTC(), batchSize)
	if err != nil {
		return 0, []error{fmt.Errorf("fetch due subscriptions: %w", err)}
	}

	if len(due) == 0 {
		return 0, nil
	}

	slog.Info("billing cycle started", "due_count", len(due))

	generated := 0
	var errs []error

	for _, sub := range due {
		invoiced, err := e.billSubscription(ctx, sub)
		if err != nil {
			slog.Error("bill subscription failed",
				"subscription_id", sub.ID,
				"tenant_id", sub.TenantID,
				"error", err,
			)
			errs = append(errs, fmt.Errorf("subscription %s: %w", sub.ID, err))
			continue
		}
		if invoiced {
			generated++
		}
	}

	slog.Info("billing cycle complete", "generated", generated, "errors", len(errs))
	return generated, errs
}

// billSubscription generates an invoice for one subscription.
// Returns (true, nil) if an invoice was created, (false, nil) if skipped (e.g. trial).
func (e *Engine) billSubscription(ctx context.Context, sub domain.Subscription) (bool, error) {
	if sub.CurrentBillingPeriodStart == nil || sub.CurrentBillingPeriodEnd == nil {
		return false, fmt.Errorf("subscription has no billing period set")
	}

	periodStart := *sub.CurrentBillingPeriodStart
	periodEnd := *sub.CurrentBillingPeriodEnd

	// Skip if in trial — advance cycle but don't generate invoice
	if sub.TrialEndAt != nil && time.Now().UTC().Before(*sub.TrialEndAt) {
		nextBilling := advanceBillingPeriod(periodEnd, domain.BillingMonthly)
		slog.Info("skipping billing (trial active)", "subscription_id", sub.ID)
		return false, e.subs.UpdateBillingCycle(ctx, sub.TenantID, sub.ID, periodEnd, nextBilling, nextBilling)
	}

	plan, err := e.pricing.GetPlan(ctx, sub.TenantID, sub.PlanID)
	if err != nil {
		return false, fmt.Errorf("get plan: %w", err)
	}

	// Aggregate usage for each meter in the plan (by customer, not subscription —
	// events are ingested with customer context, not subscription context)
	usageTotals, err := e.usage.AggregateForBillingPeriod(ctx, sub.TenantID, sub.CustomerID, plan.MeterIDs, periodStart, periodEnd)
	if err != nil {
		return false, fmt.Errorf("aggregate usage: %w", err)
	}

	// Build line items
	var lineItems []domain.InvoiceLineItem
	subtotal := int64(0)

	// Base fee line item
	if plan.BaseAmountCents > 0 {
		lineItems = append(lineItems, domain.InvoiceLineItem{
			LineType:        domain.LineTypeBaseFee,
			Description:     fmt.Sprintf("%s - base fee", plan.Name),
			Quantity:        1,
			UnitAmountCents: plan.BaseAmountCents,
			AmountCents:     plan.BaseAmountCents,
			TotalAmountCents: plan.BaseAmountCents,
			Currency:        plan.Currency,
		})
		subtotal += plan.BaseAmountCents
	}

	// Usage line items — one per meter
	for _, meterID := range plan.MeterIDs {
		quantity, ok := usageTotals[meterID]
		if !ok || quantity == 0 {
			continue
		}

		meter, err := e.pricing.GetMeter(ctx, sub.TenantID, meterID)
		if err != nil {
			return false, fmt.Errorf("get meter %s: %w", meterID, err)
		}

		if meter.RatingRuleVersionID == "" {
			continue // No pricing rule attached
		}

		// Get the linked rule to find its key, then resolve the latest version
		linkedRule, err := e.pricing.GetRatingRule(ctx, sub.TenantID, meter.RatingRuleVersionID)
		if err != nil {
			return false, fmt.Errorf("get rating rule for meter %s: %w", meterID, err)
		}

		// Use the latest active version of this rule (not the hardcoded version)
		rule, err := e.pricing.GetLatestRuleByKey(ctx, sub.TenantID, linkedRule.RuleKey)
		if err != nil {
			// Fall back to the linked version if latest lookup fails
			rule = linkedRule
		}

		// Check for per-customer price override
		override, overrideErr := e.pricing.GetOverride(ctx, sub.TenantID, sub.CustomerID, rule.ID)
		if overrideErr == nil && override.Active {
			rule = override.ToRatingRule()
		}

		amount, err := domain.ComputeAmountCents(rule, quantity)
		if err != nil {
			return false, fmt.Errorf("compute amount for meter %s: %w", meterID, err)
		}

		unitAmount := int64(0)
		if quantity > 0 {
			unitAmount = amount / quantity
		}

		lineItems = append(lineItems, domain.InvoiceLineItem{
			LineType:            domain.LineTypeUsage,
			MeterID:             meterID,
			Description:         fmt.Sprintf("%s (%s)", meter.Name, meter.Unit),
			Quantity:            quantity,
			UnitAmountCents:     unitAmount,
			AmountCents:         amount,
			TotalAmountCents:    amount,
			Currency:            plan.Currency,
			PricingMode:         string(rule.Mode),
			RatingRuleVersionID: rule.ID,
			BillingPeriodStart:  &periodStart,
			BillingPeriodEnd:    &periodEnd,
		})
		subtotal += amount
	}

	// Create invoice — pull settings for invoice number + payment terms
	now := time.Now().UTC()
	netDays := 30
	invoiceNumber := fmt.Sprintf("VLX-%s-%04d", now.Format("200601"), now.UnixMilli()%10000)

	if e.settings != nil {
		if ts, err := e.settings.Get(ctx, sub.TenantID); err == nil {
			if ts.NetPaymentTerms > 0 {
				netDays = ts.NetPaymentTerms
			}
		}
		if num, err := e.settings.NextInvoiceNumber(ctx, sub.TenantID); err == nil && num != "" {
			invoiceNumber = num
		}
	}

	dueAt := now.AddDate(0, 0, netDays)

	inv, err := e.invoices.CreateInvoice(ctx, sub.TenantID, domain.Invoice{
		CustomerID:         sub.CustomerID,
		SubscriptionID:     sub.ID,
		InvoiceNumber:      invoiceNumber,
		Status:             domain.InvoiceFinalized,
		PaymentStatus:      domain.PaymentPending,
		Currency:           plan.Currency,
		SubtotalCents:      subtotal,
		TotalAmountCents:   subtotal,
		AmountDueCents:     subtotal,
		BillingPeriodStart: periodStart,
		BillingPeriodEnd:   periodEnd,
		IssuedAt:           &now,
		DueAt:              &dueAt,
		NetPaymentTermDays: netDays,
	})
	if err != nil {
		return false, fmt.Errorf("create invoice: %w", err)
	}

	// Create line items
	for _, item := range lineItems {
		item.InvoiceID = inv.ID
		if _, err := e.invoices.CreateLineItem(ctx, sub.TenantID, item); err != nil {
			return false, fmt.Errorf("create line item: %w", err)
		}
	}

	// Apply customer credits before charging (reduces amount_due)
	if e.credits != nil && subtotal > 0 {
		credited, err := e.credits.ApplyToInvoice(ctx, sub.TenantID, sub.CustomerID, inv.ID, subtotal, inv.InvoiceNumber)
		if err != nil {
			slog.Warn("failed to apply credits", "invoice_id", inv.ID, "error", err)
		} else if credited > 0 {
			// Actually reduce the invoice amount_due
			if _, err := e.invoices.ApplyCreditAmount(ctx, sub.TenantID, inv.ID, credited); err != nil {
				slog.Warn("failed to reduce invoice amount_due", "invoice_id", inv.ID, "error", err)
			} else {
				slog.Info("credits applied to invoice",
					"invoice_id", inv.ID,
					"credited_cents", credited,
					"remaining_due", subtotal-credited,
				)
			}
		}
	}

	// Auto-charge: if customer has payment method, create PaymentIntent (async)
	if e.charger != nil && e.paymentSetups != nil && inv.AmountDueCents > 0 {
		if ps, err := e.paymentSetups.GetPaymentSetup(ctx, sub.TenantID, sub.CustomerID); err == nil &&
			ps.SetupStatus == domain.PaymentSetupReady && ps.StripeCustomerID != "" {
			// Fire-and-forget: charge in background so billing cycle isn't blocked
			go func(tenantID string, invID string, stripeCustomerID string) {
				chargeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				// Re-read invoice to get updated amount_due after credits
				chargeInv, err := e.invoices.GetInvoice(chargeCtx, tenantID, invID)
				if err != nil || chargeInv.AmountDueCents <= 0 {
					return
				}
				if _, err := e.charger.ChargeInvoice(chargeCtx, tenantID, chargeInv, stripeCustomerID); err != nil {
					slog.Warn("auto-charge failed", "invoice_id", invID, "error", err)
				} else {
					slog.Info("auto-charge initiated", "invoice_id", invID)
				}
			}(sub.TenantID, inv.ID, ps.StripeCustomerID)
		}
	}

	// Advance billing cycle
	nextPeriodStart := periodEnd
	nextPeriodEnd := advanceBillingPeriod(periodEnd, plan.BillingInterval)

	if err := e.subs.UpdateBillingCycle(ctx, sub.TenantID, sub.ID, nextPeriodStart, nextPeriodEnd, nextPeriodEnd); err != nil {
		return false, fmt.Errorf("advance billing cycle: %w", err)
	}

	slog.Info("invoice generated",
		"invoice_id", inv.ID,
		"subscription_id", sub.ID,
		"total_cents", subtotal,
		"line_items", len(lineItems),
	)

	return true, nil
}

func advanceBillingPeriod(from time.Time, interval domain.BillingInterval) time.Time {
	switch interval {
	case domain.BillingYearly:
		return from.AddDate(1, 0, 0)
	default:
		return from.AddDate(0, 1, 0)
	}
}
