package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
	"github.com/sagarsuperuser/velox/internal/platform/money"
	"github.com/sagarsuperuser/velox/internal/platform/telemetry"
	"github.com/sagarsuperuser/velox/internal/tax"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
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
	profiles      BillingProfileReader
	taxCalc       tax.Calculator
	coupons       CouponApplier
	clock         clock.Clock
	testClocks    TestClockReader
}

// TestClockReader looks up a test clock's frozen_time. The billing engine
// calls this for every subscription that has test_clock_id set, so the clock
// decides "what time is it for this sub?" instead of wall-clock. Returns
// errs.ErrNotFound when the clock has been deleted (caller treats missing
// clock as wall-clock — the detached sub quietly rejoins the live timeline).
type TestClockReader interface {
	Get(ctx context.Context, tenantID, id string) (domain.TestClock, error)
}

// CreditApplier applies customer credits to an invoice before charging.
type CreditApplier interface {
	ApplyToInvoice(ctx context.Context, tenantID, customerID, invoiceID string, amountCents int64, invoiceNumber ...string) (int64, error)
}

// CouponApplier computes the coupon discount to apply to an invoice's
// gross subtotal for a given subscription, then — after the invoice
// commits — is called to advance the periods_applied counter on every
// redemption that contributed. ApplyToInvoice itself is side-effect-free;
// the MarkPeriodsApplied step is what burns a period of a 'once' /
// 'repeating' coupon, so it must run only when the invoice that consumed
// the discount is durably persisted.
type CouponApplier interface {
	ApplyToInvoice(ctx context.Context, tenantID, subscriptionID string, planIDs []string, subtotalCents int64) (domain.CouponDiscountResult, error)
	MarkPeriodsApplied(ctx context.Context, tenantID string, redemptionIDs []string) error
}

// BillingProfileReader reads customer billing profiles for tax exemption checks.
type BillingProfileReader interface {
	GetBillingProfile(ctx context.Context, tenantID, customerID string) (domain.CustomerBillingProfile, error)
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
	// ApplyDuePendingItemPlansAtomic swaps plan_id ← pending_plan_id for every
	// item on the subscription whose pending_plan_effective_at <= now, in one
	// statement. Returns the refreshed items (including any that weren't due,
	// untouched). Called at the cycle boundary so the just-closed period is
	// billed on the pre-change plan and the next period uses the new plan.
	// Returns nil + no error when no items are due (caller proceeds with the
	// existing plan).
	ApplyDuePendingItemPlansAtomic(ctx context.Context, tenantID, subscriptionID string, now time.Time) ([]domain.SubscriptionItem, error)
}

// UsageAggregator aggregates usage events for a billing period.
type UsageAggregator interface {
	AggregateForBillingPeriod(ctx context.Context, tenantID, customerID string, meterIDs []string, from, to time.Time) (map[string]int64, error)
	AggregateForBillingPeriodByAgg(ctx context.Context, tenantID, customerID string, meters map[string]string, from, to time.Time) (map[string]int64, error)
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
	CreateInvoiceWithLineItems(ctx context.Context, tenantID string, inv domain.Invoice, items []domain.InvoiceLineItem) (domain.Invoice, error)
	CreateLineItem(ctx context.Context, tenantID string, item domain.InvoiceLineItem) (domain.InvoiceLineItem, error)
	ApplyCreditAmount(ctx context.Context, tenantID, id string, amountCents int64) (domain.Invoice, error)
	GetInvoice(ctx context.Context, tenantID, id string) (domain.Invoice, error)
	MarkPaid(ctx context.Context, tenantID, id string, stripePaymentIntentID string, paidAt time.Time) (domain.Invoice, error)
	SetAutoChargePending(ctx context.Context, tenantID, id string, pending bool) error
	ListAutoChargePending(ctx context.Context, limit int) ([]domain.Invoice, error)
}

func NewEngine(subs SubscriptionReader, usage UsageAggregator, pricing PricingReader, invoices InvoiceWriter, credits CreditApplier, settings SettingsReader, paymentSetups PaymentSetupReader, charger InvoiceCharger, clk clock.Clock, profiles ...BillingProfileReader) *Engine {
	if clk == nil {
		clk = clock.Real()
	}
	e := &Engine{subs: subs, usage: usage, pricing: pricing, invoices: invoices, credits: credits, settings: settings, paymentSetups: paymentSetups, charger: charger, clock: clk}
	if len(profiles) > 0 {
		e.profiles = profiles[0]
	}
	return e
}

// SetTaxCalculator sets the tax calculator used during billing.
// When nil, the engine falls back to inline manual tax logic for backward compatibility.
func (e *Engine) SetTaxCalculator(c tax.Calculator) {
	e.taxCalc = c
}

// SetCouponApplier sets the coupon service used during billing. When nil, the
// engine skips coupon resolution entirely and invoice discount_cents remains 0.
func (e *Engine) SetCouponApplier(c CouponApplier) {
	e.coupons = c
}

// SetTestClockReader wires the test-clock resolver. Optional: when nil, the
// engine always uses wall-clock time, even for subs with test_clock_id set
// (useful in narrow unit tests that don't exercise test-mode timing).
func (e *Engine) SetTestClockReader(r TestClockReader) {
	e.testClocks = r
}

// effectiveNow returns the clock time the engine should use for this sub.
// If the sub is attached to a test clock, the clock's frozen_time wins;
// otherwise wall-clock via e.clock. A deleted or unreadable test clock
// falls back silently to wall-clock — a dangling test_clock_id must not
// stall the billing tick for every other tenant.
func (e *Engine) effectiveNow(ctx context.Context, sub domain.Subscription) time.Time {
	if sub.TestClockID == "" || e.testClocks == nil {
		return e.clock.Now()
	}
	tc, err := e.testClocks.Get(ctx, sub.TenantID, sub.TestClockID)
	if err != nil {
		slog.Warn("test clock lookup failed, falling back to wall clock",
			"subscription_id", sub.ID, "test_clock_id", sub.TestClockID, "error", err)
		return e.clock.Now()
	}
	return tc.FrozenTime
}

// TaxApplication is the invoice-level tax summary returned by
// ApplyTaxToLineItems. The line items passed in are mutated in place with
// per-line TaxRateBP, TaxAmountCents, and TotalAmountCents so caller-side sums
// reconcile to TaxAmountCents.
//
// SubtotalCents and DiscountCents are the net (ex-tax) values the caller
// should persist to invoice.SubtotalCents / invoice.DiscountCents. In
// tax-exclusive mode they equal the caller's input. In tax-inclusive mode
// (tenant_settings.tax_inclusive=true) the caller passes gross values and
// the engine back-calculates net here; downstream invoice arithmetic remains
// SubtotalCents - DiscountCents + TaxAmountCents = customer total.
type TaxApplication struct {
	TaxAmountCents int64
	TaxRateBP      int64
	TaxName        string
	TaxCountry     string
	TaxID          string
	SubtotalCents  int64
	DiscountCents  int64
}

// ApplyTaxToLineItems resolves the tenant + customer tax configuration and
// computes tax against the post-discount subtotal. Shared by the main billing
// path (RunCycle) and subscription proration so both invoice shapes carry the
// same tax fields — previously proration invoices were silently tax-free.
//
// Behaviour:
//   - Tenant-level rate/name come from tenant_settings; customer billing profile
//     overrides rate (bp.TaxOverrideRateBP) and zeroes it for tax_exempt customers.
//   - If a tax.Calculator is wired it's consulted with per-line inputs whose
//     AmountCents already reflects each line's proportional share of the
//     invoice-level discount; calculator errors produce a warning and fall
//     through to zero tax (same behaviour as the original inline path).
//   - Otherwise, the inline math uses banker's rounding and corrects the last
//     line for any ±1¢ rounding drift so line-level tax sums match the
//     invoice-level total.
//
// Safe to call with subtotal-discount <= 0 — returns zero tax and leaves line
// items untouched.
func (e *Engine) ApplyTaxToLineItems(ctx context.Context, tenantID, customerID, currency string, subtotal, discount int64, lineItems []domain.InvoiceLineItem) (TaxApplication, error) {
	var app TaxApplication
	// Default: caller's inputs flow through unchanged. The inclusive branch
	// below overwrites these with net (back-calculated) values.
	app.SubtotalCents = subtotal
	app.DiscountCents = discount

	var inclusive bool
	if e.settings != nil {
		if ts, err := e.settings.Get(ctx, tenantID); err == nil {
			app.TaxRateBP = ts.TaxRateBP
			app.TaxName = ts.TaxName
			inclusive = ts.TaxInclusive
		}
	}

	var customerAddr tax.CustomerAddress
	if e.profiles != nil && customerID != "" {
		if bp, err := e.profiles.GetBillingProfile(ctx, tenantID, customerID); err == nil {
			customerAddr = tax.CustomerAddress{
				Line1:      bp.AddressLine1,
				City:       bp.City,
				State:      bp.State,
				PostalCode: bp.PostalCode,
				Country:    bp.Country,
			}
			if bp.TaxExempt {
				app.TaxRateBP = 0
				app.TaxName = ""
			} else {
				if bp.TaxOverrideRateBP != nil {
					app.TaxRateBP = *bp.TaxOverrideRateBP
				}
				app.TaxCountry = bp.Country
				app.TaxID = bp.TaxID
			}
		}
	}

	discountedSubtotal := subtotal - discount
	if discountedSubtotal <= 0 {
		return app, nil
	}

	if e.taxCalc != nil {
		taxInputs := make([]tax.LineItemInput, len(lineItems))
		for i, li := range lineItems {
			taxable := li.AmountCents
			if subtotal > 0 && discount > 0 {
				taxable = max(li.AmountCents-money.RoundHalfToEven(li.AmountCents*discount, subtotal), 0)
			}
			taxInputs[i] = tax.LineItemInput{
				AmountCents: taxable,
				Description: li.Description,
				Quantity:    li.Quantity,
			}
		}
		taxResult, taxErr := e.taxCalc.CalculateTax(ctx, currency, customerAddr, taxInputs)
		if taxErr != nil {
			slog.Warn("tax calculation failed, proceeding with zero tax",
				"error", taxErr, "tenant_id", tenantID, "customer_id", customerID)
			return app, nil
		}
		if taxResult != nil && taxResult.TotalTaxAmountCents > 0 {
			app.TaxAmountCents = taxResult.TotalTaxAmountCents
			app.TaxRateBP = taxResult.TaxRateBP
			if taxResult.TaxName != "" {
				app.TaxName = taxResult.TaxName
			}
			if taxResult.TaxCountry != "" {
				app.TaxCountry = taxResult.TaxCountry
			}
			for _, lt := range taxResult.LineItemTaxes {
				if lt.Index >= 0 && lt.Index < len(lineItems) {
					lineItems[lt.Index].TaxRateBP = lt.TaxRateBP
					lineItems[lt.Index].TaxAmountCents = lt.TaxAmountCents
					lineItems[lt.Index].TotalAmountCents = lineItems[lt.Index].AmountCents + lt.TaxAmountCents
				}
			}
		}
		return app, nil
	}

	if app.TaxRateBP <= 0 {
		return app, nil
	}

	if inclusive {
		// Tax-inclusive: caller's subtotal / discount / line.AmountCents are
		// gross (tax-included sticker prices). Back-calculate the net
		// equivalents so the stored invoice is { SubtotalCents (net) -
		// DiscountCents (net) + TaxAmountCents } == customer payment (gross).
		denom := int64(10000 + app.TaxRateBP)
		netDiscounted := money.RoundHalfToEven(discountedSubtotal*10000, denom)
		app.TaxAmountCents = discountedSubtotal - netDiscounted

		var lineTaxSum int64
		var lineNetUndiscSum int64
		for i := range lineItems {
			lineGross := lineItems[i].AmountCents
			lineGrossDisc := lineGross
			if subtotal > 0 && discount > 0 {
				d := money.RoundHalfToEven(lineGross*discount, subtotal)
				lineGrossDisc = max(lineGross-d, 0)
			}
			lineNetUndisc := money.RoundHalfToEven(lineGross*10000, denom)
			lineNetDisc := money.RoundHalfToEven(lineGrossDisc*10000, denom)
			lineTax := lineGrossDisc - lineNetDisc

			lineItems[i].AmountCents = lineNetUndisc
			lineItems[i].TaxRateBP = app.TaxRateBP
			lineItems[i].TaxAmountCents = lineTax
			lineItems[i].TotalAmountCents = lineNetUndisc + lineTax
			lineTaxSum += lineTax
			lineNetUndiscSum += lineNetUndisc
		}
		// Same ±1¢ reconciliation pattern as the exclusive path: last line
		// absorbs any per-line rounding drift so line-level sums match
		// invoice-level totals exactly.
		if len(lineItems) > 0 && lineTaxSum != app.TaxAmountCents {
			diff := app.TaxAmountCents - lineTaxSum
			last := &lineItems[len(lineItems)-1]
			last.TaxAmountCents += diff
			last.TotalAmountCents += diff
		}
		// Subtotal/discount in net units so the caller's invariant
		// Subtotal - Discount + Tax = customer paid (= discountedSubtotal gross).
		app.SubtotalCents = lineNetUndiscSum
		app.DiscountCents = lineNetUndiscSum + app.TaxAmountCents - discountedSubtotal
		return app, nil
	}

	app.TaxAmountCents = money.RoundHalfToEven(discountedSubtotal*int64(app.TaxRateBP), 10000)

	var lineTaxSum int64
	for i := range lineItems {
		taxable := lineItems[i].AmountCents
		if subtotal > 0 && discount > 0 {
			taxable = max(lineItems[i].AmountCents-money.RoundHalfToEven(lineItems[i].AmountCents*discount, subtotal), 0)
		}
		lineTax := money.RoundHalfToEven(taxable*int64(app.TaxRateBP), 10000)
		lineItems[i].TaxRateBP = app.TaxRateBP
		lineItems[i].TaxAmountCents = lineTax
		lineItems[i].TotalAmountCents = lineItems[i].AmountCents + lineTax
		lineTaxSum += lineTax
	}
	if len(lineItems) > 0 && lineTaxSum != app.TaxAmountCents {
		diff := app.TaxAmountCents - lineTaxSum
		last := &lineItems[len(lineItems)-1]
		last.TaxAmountCents += diff
		last.TotalAmountCents += diff
	}
	return app, nil
}

// RunCycle finds all subscriptions due for billing and generates invoices.
// Returns the number of invoices generated and any errors encountered.
func (e *Engine) RunCycle(ctx context.Context, batchSize int) (int, []error) {
	if batchSize <= 0 {
		batchSize = 50
	}

	ctx, span := telemetry.Tracer("billing").Start(ctx, "billing.RunCycle",
		trace.WithAttributes(attribute.Int("batch_size", batchSize)),
	)
	defer span.End()

	due, err := e.subs.GetDueBilling(ctx, e.clock.Now(), batchSize)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "fetch due subscriptions")
		return 0, []error{fmt.Errorf("fetch due subscriptions: %w", err)}
	}
	span.SetAttributes(attribute.Int("due_count", len(due)))

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
	ctx, span := telemetry.Tracer("billing").Start(ctx, "billing.BillSubscription",
		trace.WithAttributes(
			attribute.String("subscription_id", sub.ID),
			attribute.String("tenant_id", sub.TenantID),
			attribute.String("customer_id", sub.CustomerID),
		),
	)
	defer span.End()

	// Guard: only bill active subscriptions
	if sub.Status != domain.SubscriptionActive {
		slog.Info("skipping billing (not active)", "subscription_id", sub.ID, "status", sub.Status)
		return false, nil
	}

	if sub.CurrentBillingPeriodStart == nil || sub.CurrentBillingPeriodEnd == nil {
		return false, fmt.Errorf("subscription has no billing period set")
	}

	// Resolve "now" once per sub: a test-clock-attached sub runs on its
	// frozen_time, wall-clock otherwise. All subsequent time comparisons in
	// this function must use this value so trial/pending-plan/mark-paid
	// decisions stay consistent with the clock the sub lives on.
	now := e.effectiveNow(ctx, sub)

	// If any item has a scheduled plan change whose effective_at is due, apply
	// them all BEFORE reading plans so the new cycle bills on the new plans.
	// A concurrent DELETE on an item's /pending-change can race this — the
	// atomic UPDATE swaps the row only if pending_plan_id is still set, so
	// whichever statement commits first wins. Items with no due change are
	// left untouched.
	anyDue := false
	for _, it := range sub.Items {
		if it.PendingPlanID != "" && it.PendingPlanEffectiveAt != nil && !it.PendingPlanEffectiveAt.After(now) {
			anyDue = true
			break
		}
	}
	if anyDue {
		applied, err := e.subs.ApplyDuePendingItemPlansAtomic(ctx, sub.TenantID, sub.ID, now)
		if err != nil && !errors.Is(err, errs.ErrNotFound) {
			return false, fmt.Errorf("apply pending item plans: %w", err)
		}
		if applied != nil {
			sub.Items = applied
			slog.Info("applied scheduled item plan changes",
				"subscription_id", sub.ID,
				"items_changed", len(applied),
			)
		}
	}

	if len(sub.Items) == 0 {
		return false, fmt.Errorf("subscription has no items to bill")
	}

	periodStart := *sub.CurrentBillingPeriodStart
	periodEnd := *sub.CurrentBillingPeriodEnd

	// Skip if in trial — advance cycle but don't generate invoice.
	if sub.TrialEndAt != nil && now.Before(*sub.TrialEndAt) {
		nextBilling := advanceBillingPeriod(periodEnd, domain.BillingMonthly)
		slog.Info("skipping billing (trial active)", "subscription_id", sub.ID)
		return false, e.subs.UpdateBillingCycle(ctx, sub.TenantID, sub.ID, periodEnd, nextBilling, nextBilling)
	}

	// Resolve every item's plan up-front so we can read currency / meters / base
	// fee from the set. Plans come back keyed by item plan_id — items sharing a
	// plan (which UNIQUE (sub_id, plan_id) prevents, but defend anyway) resolve
	// to the same plan struct.
	plans := make(map[string]domain.Plan, len(sub.Items))
	planIDs := make([]string, 0, len(sub.Items))
	for _, it := range sub.Items {
		if _, ok := plans[it.PlanID]; ok {
			continue
		}
		pl, err := e.pricing.GetPlan(ctx, sub.TenantID, it.PlanID)
		if err != nil {
			return false, fmt.Errorf("get plan %s: %w", it.PlanID, err)
		}
		plans[it.PlanID] = pl
		planIDs = append(planIDs, it.PlanID)
	}

	// Invoice currency: customer billing profile > tenant settings > first
	// item's plan currency > "usd". The tie-breaker in multi-item mode is the
	// plan of the first item (created_at ordering) — items on a single
	// subscription are expected to share a currency; mismatches are a pricing
	// misconfiguration, not a billing problem to solve here.
	firstPlanCurrency := plans[sub.Items[0].PlanID].Currency
	invoiceCurrency := firstPlanCurrency
	if e.profiles != nil {
		if bp, err := e.profiles.GetBillingProfile(ctx, sub.TenantID, sub.CustomerID); err == nil && bp.Currency != "" {
			invoiceCurrency = bp.Currency
		}
	}
	if invoiceCurrency == "" && e.settings != nil {
		if ts, err := e.settings.Get(ctx, sub.TenantID); err == nil && ts.DefaultCurrency != "" {
			invoiceCurrency = ts.DefaultCurrency
		}
	}
	if invoiceCurrency == "" {
		invoiceCurrency = "usd"
	}

	// Collect the union of meter_ids across every item's plan. Usage is
	// customer+meter-scoped (not item-scoped) — a meter shared between two
	// items' plans aggregates once, not twice. The aggregation type is picked
	// from whichever meter lookup resolves first; in practice a meter has one
	// canonical aggregation so this is a no-op, but the map shape tolerates
	// duplicates defensively.
	meterAggs := make(map[string]string)
	for _, it := range sub.Items {
		for _, meterID := range plans[it.PlanID].MeterIDs {
			if _, ok := meterAggs[meterID]; ok {
				continue
			}
			m, err := e.pricing.GetMeter(ctx, sub.TenantID, meterID)
			if err == nil {
				meterAggs[meterID] = m.Aggregation
			} else {
				meterAggs[meterID] = "sum"
			}
		}
	}

	// Aggregate usage for each meter using its configured aggregation type.
	usageTotals, err := e.usage.AggregateForBillingPeriodByAgg(ctx, sub.TenantID, sub.CustomerID, meterAggs, periodStart, periodEnd)
	if err != nil {
		return false, fmt.Errorf("aggregate usage: %w", err)
	}

	// Enforce usage cap if configured (integer math only). Cap is a
	// subscription-level total across all meters — it doesn't need to respect
	// item boundaries because the cap is a container-level guardrail, not a
	// per-plan constraint.
	if sub.UsageCapUnits != nil && *sub.UsageCapUnits > 0 && sub.OverageAction == "block" {
		totalUsage := int64(0)
		for _, qty := range usageTotals {
			totalUsage += qty
		}
		if totalUsage > *sub.UsageCapUnits {
			cap := *sub.UsageCapUnits
			for mid, qty := range usageTotals {
				usageTotals[mid] = qty * cap / totalUsage
			}
		}
	}

	// Build line items.
	var lineItems []domain.InvoiceLineItem
	subtotal := int64(0)

	// Detect partial period once — same across all items since they share the
	// billing period.
	periodDays := int(periodEnd.Sub(periodStart).Hours() / 24)

	// Base fee line item per item — quantity-multiplied and prorated for partial
	// periods. One line per item so the invoice clearly shows what each plan
	// contributes (mirrors Stripe's per-item invoice layout).
	for _, it := range sub.Items {
		plan := plans[it.PlanID]
		if plan.BaseAmountCents <= 0 {
			continue
		}
		baseFee := plan.BaseAmountCents * it.Quantity
		description := fmt.Sprintf("%s - base fee (qty %d)", plan.Name, it.Quantity)

		fullCycleDays := int(advanceBillingPeriod(periodStart, plan.BillingInterval).Sub(periodStart).Hours() / 24)
		if periodDays > 0 && fullCycleDays > 0 && periodDays < fullCycleDays {
			baseFee = money.RoundHalfToEven(plan.BaseAmountCents*it.Quantity*int64(periodDays), int64(fullCycleDays))
			description = fmt.Sprintf("%s - base fee (qty %d, prorated %d/%d days)", plan.Name, it.Quantity, periodDays, fullCycleDays)
		}

		unitAmount := plan.BaseAmountCents
		if it.Quantity > 0 {
			unitAmount = money.RoundHalfToEven(baseFee, it.Quantity)
		}

		lineItems = append(lineItems, domain.InvoiceLineItem{
			LineType:         domain.LineTypeBaseFee,
			Description:      description,
			Quantity:         it.Quantity,
			UnitAmountCents:  unitAmount,
			AmountCents:      baseFee,
			TotalAmountCents: baseFee,
			Currency:         invoiceCurrency,
		})
		subtotal += baseFee
	}

	// Usage line items — one per meter. Usage is billed once per meter even if
	// multiple items' plans reference the same meter; quantity on a usage line
	// is the metered count, not the item's seat quantity.
	seenMeters := make(map[string]struct{})
	for _, it := range sub.Items {
		plan := plans[it.PlanID]
		for _, meterID := range plan.MeterIDs {
			if _, ok := seenMeters[meterID]; ok {
				continue
			}
			seenMeters[meterID] = struct{}{}

			quantity, ok := usageTotals[meterID]
			if !ok || quantity == 0 {
				continue
			}

			meter, err := e.pricing.GetMeter(ctx, sub.TenantID, meterID)
			if err != nil {
				return false, fmt.Errorf("get meter %s: %w", meterID, err)
			}

			if meter.RatingRuleVersionID == "" {
				continue
			}

			linkedRule, err := e.pricing.GetRatingRule(ctx, sub.TenantID, meter.RatingRuleVersionID)
			if err != nil {
				return false, fmt.Errorf("get rating rule for meter %s: %w", meterID, err)
			}

			rule, err := e.pricing.GetLatestRuleByKey(ctx, sub.TenantID, linkedRule.RuleKey)
			if err != nil {
				rule = linkedRule
			}

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
				unitAmount = money.RoundHalfToEven(amount, quantity)
			}

			lineItems = append(lineItems, domain.InvoiceLineItem{
				LineType:            domain.LineTypeUsage,
				MeterID:             meterID,
				Description:         fmt.Sprintf("%s (%s)", meter.Name, meter.Unit),
				Quantity:            quantity,
				UnitAmountCents:     unitAmount,
				AmountCents:         amount,
				TotalAmountCents:    amount,
				Currency:            invoiceCurrency,
				PricingMode:         string(rule.Mode),
				RatingRuleVersionID: rule.ID,
				BillingPeriodStart:  &periodStart,
				BillingPeriodEnd:    &periodEnd,
			})
			subtotal += amount
		}
	}

	// Create invoice — pull settings for payment terms + tax, then allocate the
	// invoice number as a strictly monotonic per-tenant sequence. No fallback:
	// a collision-prone number is worse than a failed billing tick since the
	// tick will retry, while a duplicate invoice number corrupts accounting.
	// `now` was resolved at the top of billSubscription via effectiveNow —
	// reuse it so invoice timestamps sit on the same timeline as the rest of
	// this call (matters for test-clock subs where wall-clock ≠ frozen_time).
	netDays := 30

	if e.settings == nil {
		return false, fmt.Errorf("billing engine: settings reader is required for invoice numbering")
	}
	if ts, err := e.settings.Get(ctx, sub.TenantID); err == nil && ts.NetPaymentTerms > 0 {
		netDays = ts.NetPaymentTerms
	}
	invoiceNumber, err := e.settings.NextInvoiceNumber(ctx, sub.TenantID)
	if err != nil {
		return false, fmt.Errorf("allocate invoice number: %w", err)
	}

	// Apply coupon discount — Stripe-style order: subtotal → discount → tax →
	// total. Tax is computed against the post-discount amount so customers
	// aren't taxed on money they didn't actually pay. A zero result here is
	// the happy no-coupon path; a non-zero result is clamped to subtotal by
	// the coupon service before reaching us, so no negative-total risk.
	// appliedRedemptionIDs carries FEAT-6 state across the invoice-create
	// boundary: we must only advance periods_applied AFTER the invoice is
	// durably persisted, otherwise a create failure would burn a period of
	// a repeating coupon that the customer never actually got.
	var discountCents int64
	var appliedRedemptionIDs []string
	if e.coupons != nil && subtotal > 0 && sub.ID != "" {
		d, err := e.coupons.ApplyToInvoice(ctx, sub.TenantID, sub.ID, planIDs, subtotal)
		if err != nil {
			slog.Warn("coupon apply failed, proceeding without discount",
				"error", err, "subscription_id", sub.ID)
		} else {
			discountCents = d.Cents
			appliedRedemptionIDs = d.RedemptionIDs
		}
	}
	taxApp, _ := e.ApplyTaxToLineItems(ctx, sub.TenantID, sub.CustomerID, invoiceCurrency, subtotal, discountCents, lineItems)
	// In tax-inclusive mode the engine back-calculates net subtotal/discount
	// from the gross inputs; in exclusive mode these pass through unchanged,
	// so the caller always reads the authoritative values off the result.
	taxAmountCents := taxApp.TaxAmountCents
	taxRateBP := taxApp.TaxRateBP
	taxName := taxApp.TaxName
	taxCountry := taxApp.TaxCountry
	taxID := taxApp.TaxID

	totalWithTax := taxApp.SubtotalCents - taxApp.DiscountCents + taxAmountCents
	dueAt := now.AddDate(0, 0, netDays)

	// ATOMIC: Create invoice + all line items in a single transaction.
	// This prevents orphaned invoices with missing line items on partial failure.
	// The unique index on (tenant_id, subscription_id, billing_period_start, billing_period_end)
	// provides idempotency — duplicate calls return an error instead of double-billing.
	inv, err := e.invoices.CreateInvoiceWithLineItems(ctx, sub.TenantID, domain.Invoice{
		CustomerID:         sub.CustomerID,
		SubscriptionID:     sub.ID,
		InvoiceNumber:      invoiceNumber,
		Status:             domain.InvoiceFinalized,
		PaymentStatus:      domain.PaymentPending,
		Currency:           invoiceCurrency,
		SubtotalCents:      taxApp.SubtotalCents,
		DiscountCents:      taxApp.DiscountCents,
		TaxRateBP:          taxRateBP,
		TaxName:            taxName,
		TaxCountry:         taxCountry,
		TaxID:              taxID,
		TaxAmountCents:     taxAmountCents,
		TotalAmountCents:   totalWithTax,
		AmountDueCents:     totalWithTax,
		BillingPeriodStart: periodStart,
		BillingPeriodEnd:   periodEnd,
		IssuedAt:           &now,
		DueAt:              &dueAt,
		NetPaymentTermDays: netDays,
	}, lineItems)
	if err != nil {
		// Idempotency: if this invoice already exists (UNIQUE violation on the
		// per-subscription+period constraint), the store returns errs.ErrAlreadyExists.
		// Match on the sentinel, not err.Error() substrings — translated messages,
		// wrapped errors, or DB driver changes would silently break substring matches
		// and cause duplicate charges in multi-worker retries.
		if errors.Is(err, errs.ErrAlreadyExists) {
			slog.Info("invoice already exists for billing period (idempotent skip)",
				"subscription_id", sub.ID,
				"period_start", periodStart,
				"period_end", periodEnd,
			)
			// Still advance the billing cycle in case it was missed
			nextPeriodStart := periodEnd
			nextPeriodEnd := advanceBillingPeriod(periodEnd, plans[sub.Items[0].PlanID].BillingInterval)
			_ = e.subs.UpdateBillingCycle(ctx, sub.TenantID, sub.ID, nextPeriodStart, nextPeriodEnd, nextPeriodEnd)
			return false, nil
		}
		return false, fmt.Errorf("create invoice: %w", err)
	}

	// Advance periods_applied on every redemption that contributed to the
	// discount. This MUST happen after CreateInvoiceWithLineItems succeeds
	// (and only on the non-idempotent-skip path) so a coupon period is
	// burned exactly once per real invoice. Per-redemption failures are
	// logged and swallowed — the invoice already exists, so the worst case
	// is a repeating coupon applying one extra cycle, which we'd rather
	// have than refusing to bill the customer over a bookkeeping glitch.
	if e.coupons != nil && len(appliedRedemptionIDs) > 0 {
		if err := e.coupons.MarkPeriodsApplied(ctx, sub.TenantID, appliedRedemptionIDs); err != nil {
			slog.Warn("coupon mark-periods-applied failed",
				"invoice_id", inv.ID,
				"subscription_id", sub.ID,
				"error", err)
		}
	}

	// Apply customer credits before charging. ApplyToInvoice is atomic:
	// it both debits the credit ledger AND reduces the invoice's amount_due_cents
	// in a single transaction. A failure leaves both unchanged — no dual-write
	// hole where credits are consumed but the invoice still shows the pre-credit
	// amount due (which would double-bill the customer via Stripe).
	if e.credits != nil && totalWithTax > 0 {
		credited, err := e.credits.ApplyToInvoice(ctx, sub.TenantID, sub.CustomerID, inv.ID, totalWithTax, inv.InvoiceNumber)
		if err != nil {
			slog.Warn("failed to apply credits", "invoice_id", inv.ID, "error", err)
		} else if credited > 0 {
			slog.Info("credits applied to invoice",
				"invoice_id", inv.ID,
				"credited_cents", credited,
			)
		}
	}

	// If credits covered 100%, mark as paid immediately (no Stripe charge needed)
	if totalWithTax > 0 {
		updatedInv, err := e.invoices.GetInvoice(ctx, sub.TenantID, inv.ID)
		if err == nil && updatedInv.AmountDueCents <= 0 {
			// Reuse the sub-scoped `now` so fully-credit-paid invoices on a
			// test clock get paid_at from the frozen timeline, not wall-clock.
			if _, err := e.invoices.MarkPaid(ctx, sub.TenantID, inv.ID, "", now); err != nil {
				slog.Warn("failed to mark fully-credited invoice as paid", "invoice_id", inv.ID, "error", err)
			} else {
				slog.Info("invoice fully covered by credits, marked as paid", "invoice_id", inv.ID)
				// Still advance the billing cycle
				nextPeriodStart := periodEnd
				nextPeriodEnd := advanceBillingPeriod(periodEnd, plans[sub.Items[0].PlanID].BillingInterval)
				if err := e.subs.UpdateBillingCycle(ctx, sub.TenantID, sub.ID, nextPeriodStart, nextPeriodEnd, nextPeriodEnd); err != nil {
					return true, fmt.Errorf("advance billing cycle: %w", err)
				}
				return true, nil
			}
		}
	}

	// Auto-charge: synchronous with timeout. If it fails, mark for scheduler retry
	// instead of fire-and-forget goroutine that loses failures.
	if e.charger != nil && e.paymentSetups != nil && inv.AmountDueCents > 0 {
		if ps, err := e.paymentSetups.GetPaymentSetup(ctx, sub.TenantID, sub.CustomerID); err == nil &&
			ps.SetupStatus == domain.PaymentSetupReady && ps.StripeCustomerID != "" {

			chargeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			chargeInv, err := e.invoices.GetInvoice(chargeCtx, sub.TenantID, inv.ID)
			if err == nil && chargeInv.AmountDueCents > 0 {
				if _, err := e.charger.ChargeInvoice(chargeCtx, sub.TenantID, chargeInv, ps.StripeCustomerID); err != nil {
					slog.Warn("auto-charge failed, marking for retry",
						"invoice_id", inv.ID,
						"error", err,
					)
					// Mark for scheduler-based retry instead of losing the failure
					_ = e.invoices.SetAutoChargePending(ctx, sub.TenantID, inv.ID, true)
				} else {
					slog.Info("auto-charge succeeded", "invoice_id", inv.ID)
				}
			}
		}
	}

	// Advance billing cycle
	nextPeriodStart := periodEnd
	nextPeriodEnd := advanceBillingPeriod(periodEnd, plans[sub.Items[0].PlanID].BillingInterval)

	if err := e.subs.UpdateBillingCycle(ctx, sub.TenantID, sub.ID, nextPeriodStart, nextPeriodEnd, nextPeriodEnd); err != nil {
		return false, fmt.Errorf("advance billing cycle: %w", err)
	}

	slog.Info("invoice generated",
		"invoice_id", inv.ID,
		"subscription_id", sub.ID,
		"total_cents", totalWithTax,
		"tax_bp", taxRateBP,
		"line_items", len(lineItems),
	)

	return true, nil
}

// RetryPendingCharges picks up invoices flagged for auto-charge retry
// and attempts to charge them. Called by the scheduler.
func (e *Engine) RetryPendingCharges(ctx context.Context, limit int) (int, []error) {
	if e.charger == nil || e.paymentSetups == nil {
		return 0, nil
	}

	pending, err := e.invoices.ListAutoChargePending(ctx, limit)
	if err != nil {
		return 0, []error{fmt.Errorf("list pending charges: %w", err)}
	}

	charged := 0
	var errs []error
	for _, inv := range pending {
		ps, err := e.paymentSetups.GetPaymentSetup(ctx, inv.TenantID, inv.CustomerID)
		if err != nil || ps.SetupStatus != domain.PaymentSetupReady || ps.StripeCustomerID == "" {
			continue
		}

		chargeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if _, err := e.charger.ChargeInvoice(chargeCtx, inv.TenantID, inv, ps.StripeCustomerID); err != nil {
			errs = append(errs, fmt.Errorf("charge invoice %s: %w", inv.ID, err))
			cancel()
			continue
		}
		cancel()

		// Clear the pending flag on success
		_ = e.invoices.SetAutoChargePending(ctx, inv.TenantID, inv.ID, false)
		charged++
		slog.Info("auto-charge retry succeeded", "invoice_id", inv.ID)
	}

	return charged, errs
}

func advanceBillingPeriod(from time.Time, interval domain.BillingInterval) time.Time {
	switch interval {
	case domain.BillingYearly:
		return from.AddDate(1, 0, 0)
	default:
		return from.AddDate(0, 1, 0)
	}
}

