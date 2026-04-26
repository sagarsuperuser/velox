package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/shopspring/decimal"

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
	taxProviders  TaxProviderResolver
	taxCalcStore  TaxCalculationWriter
	coupons       CouponApplier
	clock         clock.Clock
	testClocks    TestClockReader
	events        domain.EventDispatcher
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
	ApplyToInvoice(ctx context.Context, tenantID, subscriptionID, customerID, invoiceCurrency string, planIDs []string, subtotalCents int64) (domain.CouponDiscountResult, error)
	// ApplyToInvoiceForCustomer is the customer-scoped fallback: when no
	// subscription coupon applies (or the invoice has no subscription at
	// all), the engine consults the customer's standing assignment so the
	// operator's "apply this coupon to all future invoices" action takes
	// effect. Subscription-scope wins when both exist (Stripe's rule).
	// RedemptionIDs carries customer_discounts row IDs (distinct from the
	// coupon_redemptions IDs returned by ApplyToInvoice) — the engine
	// routes them to MarkCustomerDiscountPeriodsApplied after commit.
	ApplyToInvoiceForCustomer(ctx context.Context, tenantID, customerID, invoiceCurrency string, planIDs []string, subtotalCents int64) (domain.CouponDiscountResult, error)
	MarkPeriodsApplied(ctx context.Context, tenantID string, redemptionIDs []string) error
	// MarkCustomerDiscountPeriodsApplied advances the periods_applied
	// counter on each customer_discounts row. Kept separate from
	// MarkPeriodsApplied so the two tables stay their own sources of
	// truth — duration exhaustion on the customer-scope side doesn't
	// reach into coupon_redemptions, and vice versa.
	MarkCustomerDiscountPeriodsApplied(ctx context.Context, tenantID string, ids []string) error
	// RedeemForInvoice commits a coupon against an already-issued draft
	// invoice (the apply-coupon-after-issue flow). Engine calls this from
	// ApplyCouponToInvoice and compensates with VoidRedemptionsForInvoice
	// if the subsequent atomic-apply fails. PlanIDs carries the target
	// subscription's full plan set so the PlanIDs restriction matches
	// any-one-of rather than a single plan.
	RedeemForInvoice(ctx context.Context, tenantID string, req domain.CouponRedeemRequest) (domain.CouponRedeemResult, error)
	// VoidRedemptionsForInvoice reverses every coupon redemption tied to
	// the invoice: marks each voided, decrements times_redeemed, rolls
	// back periods_applied. Engine calls this as the compensating action
	// when ApplyDiscountAtomic fails after a successful Redeem. Idempotent
	// — already-voided rows are left alone.
	VoidRedemptionsForInvoice(ctx context.Context, tenantID, invoiceID string) (int, error)
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

	// FireScheduledCancellation transitions a sub with a due cancel_at or
	// cancel_at_period_end intent to canceled in one statement. Called by
	// the cycle scan after invoice generation, instead of UpdateBillingCycle,
	// when the schedule fields say it's time. The `at` argument is the
	// engine's effectiveNow so canceled_at stays consistent with test-
	// clock-driven time travel.
	FireScheduledCancellation(ctx context.Context, tenantID, id string, at time.Time) (domain.Subscription, error)

	// ClearPauseCollection nulls the pause_collection_* columns. Called by
	// the cycle scan to auto-resume a sub whose pause_collection.resumes_at
	// has passed. Mirrors the explicit DELETE /pause-collection in the
	// store-side semantics.
	ClearPauseCollection(ctx context.Context, tenantID, id string) (domain.Subscription, error)

	// ActivateAfterTrial atomically transitions a sub from 'trialing' to
	// 'active' and stamps activated_at if not already set. Called by the
	// cycle scan when the trial window has elapsed. Idempotent at the SQL
	// level: re-running on a row already 'active' returns InvalidState
	// (caller swallows it as benign).
	ActivateAfterTrial(ctx context.Context, tenantID, id string, at time.Time) (domain.Subscription, error)
}

// UsageAggregator aggregates usage events for a billing period. Returns
// decimal.Decimal so fractional AI-usage primitives (GPU-hours, cached-token
// ratios) round-trip without precision loss; the engine converts to cents
// at the multiplication step.
//
// AggregateByPricingRules is the multi-dim-aware path (priority+claim
// LATERAL JOIN across the 5 aggregation modes). The cycle scan, the
// customer-usage endpoint, and the create_preview surface all call it —
// preview math == invoice math by construction.
type UsageAggregator interface {
	AggregateForBillingPeriod(ctx context.Context, tenantID, customerID string, meterIDs []string, from, to time.Time) (map[string]decimal.Decimal, error)
	AggregateForBillingPeriodByAgg(ctx context.Context, tenantID, customerID string, meters map[string]string, from, to time.Time) (map[string]decimal.Decimal, error)
	AggregateByPricingRules(ctx context.Context, tenantID, customerID, meterID string, defaultMode domain.AggregationMode, from, to time.Time) ([]domain.RuleAggregation, error)
}

// PricingReader reads plan, rating rule, and override data.
//
// ListMeterPricingRulesByMeter is needed by the preview path to echo each
// rule's DimensionMatch (the canonical pricing identity) onto the
// per-rule preview line.
type PricingReader interface {
	GetPlan(ctx context.Context, tenantID, id string) (domain.Plan, error)
	GetMeter(ctx context.Context, tenantID, id string) (domain.Meter, error)
	GetRatingRule(ctx context.Context, tenantID, id string) (domain.RatingRuleVersion, error)
	GetLatestRuleByKey(ctx context.Context, tenantID, ruleKey string) (domain.RatingRuleVersion, error)
	GetOverride(ctx context.Context, tenantID, customerID, ruleID string) (domain.CustomerPriceOverride, error)
	ListMeterPricingRulesByMeter(ctx context.Context, tenantID, meterID string) ([]domain.MeterPricingRule, error)
}

// InvoiceWriter creates invoices and line items.
type InvoiceWriter interface {
	CreateInvoice(ctx context.Context, tenantID string, inv domain.Invoice) (domain.Invoice, error)
	CreateInvoiceWithLineItems(ctx context.Context, tenantID string, inv domain.Invoice, items []domain.InvoiceLineItem) (domain.Invoice, error)
	CreateLineItem(ctx context.Context, tenantID string, item domain.InvoiceLineItem) (domain.InvoiceLineItem, error)
	ApplyCreditAmount(ctx context.Context, tenantID, id string, amountCents int64) (domain.Invoice, error)
	GetInvoice(ctx context.Context, tenantID, id string) (domain.Invoice, error)
	ListLineItems(ctx context.Context, tenantID, invoiceID string) ([]domain.InvoiceLineItem, error)
	MarkPaid(ctx context.Context, tenantID, id string, stripePaymentIntentID string, paidAt time.Time) (domain.Invoice, error)
	SetAutoChargePending(ctx context.Context, tenantID, id string, pending bool) error
	ListAutoChargePending(ctx context.Context, limit int) ([]domain.Invoice, error)
	// SetTaxTransaction persists the upstream provider's tax_transaction
	// reference (Stripe: tx_xxx) after CommitTax succeeds. Required for
	// later reversal when a credit note is issued against the invoice.
	SetTaxTransaction(ctx context.Context, tenantID, id string, taxTransactionID string) error
	// ApplyDiscountAtomic stamps a coupon discount + recomputed tax
	// snapshot onto a draft invoice in one tx. Used by
	// Engine.ApplyCouponToInvoice for the apply-coupon-after-issue flow.
	ApplyDiscountAtomic(ctx context.Context, tenantID, invoiceID string, update domain.InvoiceDiscountUpdate, lineItems []domain.InvoiceLineItem) (domain.Invoice, error)
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

// TaxProviderResolver returns the Provider implementation for a given
// tenant's settings. Injected into the engine so billing doesn't need to
// know about Stripe keys or the tenant's provider choice directly.
type TaxProviderResolver interface {
	Resolve(ctx context.Context, ts domain.TenantSettings) (tax.Provider, error)
}

// TaxCalculationWriter persists a provider calculation to the
// tax_calculations audit table. Separate interface so engine tests can
// skip persistence without wiring a full postgres store.
type TaxCalculationWriter interface {
	Record(ctx context.Context, tenantID, invoiceID string, req tax.Request, res *tax.Result) (string, error)
}

// SetTaxProviderResolver wires the per-tenant tax provider resolver. When
// nil, the engine skips tax calculation entirely and invoices carry zero
// tax (same behaviour as tax_provider='none').
func (e *Engine) SetTaxProviderResolver(r TaxProviderResolver) {
	e.taxProviders = r
}

// SetTaxCalculationStore wires the audit-trail writer for tax calculations.
// Optional — engine still works without it, but nothing is persisted to
// tax_calculations and post-hoc audit is limited to whatever Stripe keeps.
func (e *Engine) SetTaxCalculationStore(s TaxCalculationWriter) {
	e.taxCalcStore = s
}

// CommitTax resolves the tenant's tax provider and commits the named
// calculation to a tax_transaction. Used after invoice finalize to
// finalize Stripe Tax reporting. Returns nil when no resolver is wired
// or the tenant's settings can't be loaded — the invoice still exists,
// so the caller should log but not unwind.
//
// On success, the returned upstream transaction id (Stripe: tx_xxx) is
// persisted onto the invoice so a later credit note can issue a tax
// reversal against it. Persistence failures are logged but non-fatal —
// the tax is still committed upstream and the calculation row + Stripe
// dashboard let an operator reconcile manually.
func (e *Engine) CommitTax(ctx context.Context, tenantID, invoiceID, calculationID string) error {
	if e.taxProviders == nil || e.settings == nil {
		return nil
	}
	ts, err := e.settings.Get(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("load tenant settings: %w", err)
	}
	provider, err := e.taxProviders.Resolve(ctx, ts)
	if err != nil {
		return fmt.Errorf("resolve provider: %w", err)
	}
	if provider == nil {
		return nil
	}
	txID, err := provider.Commit(ctx, calculationID, invoiceID)
	if err != nil {
		return err
	}
	if txID != "" {
		if err := e.invoices.SetTaxTransaction(ctx, tenantID, invoiceID, txID); err != nil {
			slog.Warn("tax: commit succeeded but persisting tax_transaction_id failed",
				"error", err, "tenant_id", tenantID, "invoice_id", invoiceID,
				"tax_transaction_id", txID)
		}
	}
	return nil
}

// ReverseTax resolves the tenant's tax provider and issues a reversal
// against a previously committed tax_transaction. Called from the credit
// note issue path after the refund / credit-grant / invoice-reduction
// side-effects have succeeded. Returns an empty ReversalResult when no
// resolver is wired, no provider is configured, or the provider has no
// durable upstream state (none, manual) — the caller treats an empty
// TransactionID as "nothing to record" and proceeds.
func (e *Engine) ReverseTax(ctx context.Context, tenantID string, req tax.ReversalRequest) (*tax.ReversalResult, error) {
	if e.taxProviders == nil || e.settings == nil {
		return &tax.ReversalResult{}, nil
	}
	ts, err := e.settings.Get(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("load tenant settings: %w", err)
	}
	provider, err := e.taxProviders.Resolve(ctx, ts)
	if err != nil {
		return nil, fmt.Errorf("resolve provider: %w", err)
	}
	if provider == nil {
		return &tax.ReversalResult{}, nil
	}
	return provider.Reverse(ctx, req)
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

// SetEventDispatcher wires the outbound webhook dispatcher. The engine emits
// subscription.pending_change.applied at the cycle boundary when a scheduled
// item plan change rolls into effect; without a dispatcher that event is
// dropped (acceptable for narrow billing unit tests).
func (e *Engine) SetEventDispatcher(d domain.EventDispatcher) {
	e.events = d
}

// shouldFireScheduledCancel reports whether a sub's soft-cancel intent has
// caught up with the current cycle tick. Two trigger conditions, OR'd:
//
//   - cancel_at_period_end=true and the period we just billed has ended
//     (periodEnd <= now) — by construction this is true at every invocation
//     since billSubscription is only entered when next_billing_at <= now,
//     but we keep the guard explicit so the helper doesn't depend on
//     caller invariants.
//
//   - cancel_at <= now — a specific timestamp the cycle has crossed.
//
// The check is intentionally placed after invoice generation so the just-
// ended period bills normally before the sub transitions to canceled
// (matching Stripe: the final invoice goes out, then the sub ends).
func shouldFireScheduledCancel(sub domain.Subscription, periodEnd, now time.Time) bool {
	if sub.CancelAtPeriodEnd && !periodEnd.After(now) {
		return true
	}
	if sub.CancelAt != nil && !sub.CancelAt.After(now) {
		return true
	}
	return false
}

// advanceCycleOrCancel either fires a due scheduled cancel or advances the
// billing cycle, whichever the sub's current schedule fields require. The
// two outcomes are mutually exclusive at this point in the flow — a sub
// that's about to cancel must not also have its cycle advanced, otherwise
// the next tick would observe a canceled sub with a fresh next_billing_at
// and either log a confusing skip-not-active or risk a double-cycle bug
// later. trigger is "scheduled" or "scheduled_at" for telemetry.
func (e *Engine) advanceCycleOrCancel(ctx context.Context, sub domain.Subscription, periodEnd, nextPeriodStart, nextPeriodEnd, now time.Time) error {
	if !shouldFireScheduledCancel(sub, periodEnd, now) {
		return e.subs.UpdateBillingCycle(ctx, sub.TenantID, sub.ID, nextPeriodStart, nextPeriodEnd, nextPeriodEnd)
	}

	canceled, err := e.subs.FireScheduledCancellation(ctx, sub.TenantID, sub.ID, now)
	if err != nil {
		// InvalidState here means a concurrent immediate-cancel already won
		// the race. Treat as a no-op success — the sub is canceled, which is
		// what we wanted, and the immediate-cancel handler already fired its
		// own webhook. A surfaced error here would mark the cycle as failed.
		if errors.Is(err, errs.ErrInvalidState) {
			slog.Info("scheduled cancel skipped, already canceled",
				"subscription_id", sub.ID, "tenant_id", sub.TenantID)
			return nil
		}
		return fmt.Errorf("fire scheduled cancel: %w", err)
	}

	slog.Info("scheduled cancel fired",
		"subscription_id", sub.ID,
		"tenant_id", sub.TenantID,
		"canceled_at", now.UTC(),
	)

	if e.events != nil {
		payload := map[string]any{
			"subscription_id": canceled.ID,
			"customer_id":     canceled.CustomerID,
			"status":          string(canceled.Status),
			"canceled_at":     now.UTC(),
			"triggered_by":    "schedule",
		}
		if err := e.events.Dispatch(ctx, sub.TenantID, domain.EventSubscriptionCanceled, payload); err != nil {
			slog.Error("dispatch subscription.canceled (scheduled)",
				"subscription_id", sub.ID, "tenant_id", sub.TenantID, "error", err)
		}
	}
	return nil
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
// per-line TaxRateBP, TaxAmountCents, Jurisdiction, TaxCode, and
// TotalAmountCents so caller-side sums reconcile to TaxAmountCents.
//
// SubtotalCents and DiscountCents are the net (ex-tax) values the caller
// should persist to invoice.SubtotalCents / invoice.DiscountCents. In
// tax-exclusive mode they equal the caller's input. In tax-inclusive mode
// the provider carves tax out of the gross line amounts and the engine
// rewrites lineItems[i].AmountCents to the net value so the invariant
// SubtotalCents - DiscountCents + TaxAmountCents = customer payment holds.
//
// TaxProvider / TaxCalculationID / TaxReverseCharge / TaxExemptReason are
// the durable audit snapshot stamped onto the invoice header.
//
// TaxStatus signals whether the calculation succeeded (ok) or was deferred
// because the provider failed under a block-on-failure policy (pending).
// Pending invoices are persisted without tax amounts and are blocked from
// finalize until a retry worker completes the calculation.
type TaxApplication struct {
	TaxAmountCents   int64
	TaxRateBP        int64
	TaxName          string
	TaxCountry       string
	TaxID            string
	SubtotalCents    int64
	DiscountCents    int64
	TaxProvider      string
	TaxCalculationID string
	TaxReverseCharge bool
	TaxExemptReason  string
	TaxStatus        domain.InvoiceTaxStatus
	TaxDeferredAt    *time.Time
	TaxPendingReason string
}

// truncateReason clips a provider error string to a sensible length before
// stuffing it into invoices.tax_pending_reason. Full provider payloads are
// persisted to tax_calculations for audit — this column is a human-readable
// hint for the dashboard banner.
func truncateReason(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// ApplyTaxToLineItems resolves the tenant's configured tax provider, calls
// Calculate, and stamps the per-line + invoice-level results back onto the
// supplied domain types. Shared by the main billing path and proration so
// every invoice shape carries the same audit snapshot.
//
// Flow:
//  1. Load tenant settings + customer billing profile.
//  2. Resolve the Provider (NoneProvider / ManualProvider / StripeTaxProvider).
//  3. Build a tax.Request from the line items, billing profile, and plan-level
//     tax codes collected by the caller via InvoiceLineItem.TaxCode.
//  4. Call Provider.Calculate; on error warn and fall through to zero tax so
//     billing is never blocked by a tax backend outage.
//  5. Mutate line items in place with the per-line results. In inclusive
//     mode the provider returns carved net amounts; the engine rewrites
//     AmountCents to that net value so Subtotal - Discount + Tax == gross.
//  6. Persist the calculation to tax_calculations for durable audit.
//
// Safe to call with subtotal-discount <= 0 — returns zero tax and leaves
// line items untouched.
func (e *Engine) ApplyTaxToLineItems(ctx context.Context, tenantID, customerID, currency string, subtotal, discount int64, lineItems []domain.InvoiceLineItem) (TaxApplication, error) {
	app := TaxApplication{
		SubtotalCents: subtotal,
		DiscountCents: discount,
		TaxStatus:     domain.InvoiceTaxOK,
	}

	if e.taxProviders == nil || e.settings == nil {
		return app, nil
	}
	ts, err := e.settings.Get(ctx, tenantID)
	if err != nil {
		slog.Warn("tax: failed to load tenant settings, proceeding with zero tax",
			"error", err, "tenant_id", tenantID)
		return app, nil
	}
	app.TaxName = ts.TaxName

	provider, err := e.taxProviders.Resolve(ctx, ts)
	if err != nil || provider == nil {
		slog.Warn("tax: failed to resolve provider, proceeding with zero tax",
			"error", err, "tenant_id", tenantID, "provider", ts.TaxProvider)
		for i := range lineItems {
			lineItems[i].TaxRateBP = 0
			lineItems[i].TaxAmountCents = 0
			lineItems[i].TotalAmountCents = lineItems[i].AmountCents
		}
		return app, nil
	}
	app.TaxProvider = provider.Name()

	var profile domain.CustomerBillingProfile
	if e.profiles != nil && customerID != "" {
		if bp, err := e.profiles.GetBillingProfile(ctx, tenantID, customerID); err == nil {
			profile = bp
			app.TaxCountry = bp.Country
			app.TaxID = bp.TaxID
		}
	}

	onFailure := ts.TaxOnFailure
	if onFailure == "" {
		onFailure = tax.OnFailureBlock
	}

	req := tax.Request{
		Currency: currency,
		CustomerAddress: tax.Address{
			Line1:      profile.AddressLine1,
			Line2:      profile.AddressLine2,
			City:       profile.City,
			State:      profile.State,
			PostalCode: profile.PostalCode,
			Country:    profile.Country,
		},
		CustomerTaxID:        profile.TaxID,
		CustomerTaxIDType:    profile.TaxIDType,
		CustomerStatus:       profile.TaxStatus,
		CustomerExemptReason: profile.TaxExemptReason,
		TaxInclusive:         ts.TaxInclusive,
		DiscountCents:        discount,
		DefaultTaxCode:       ts.DefaultProductTaxCode,
		OnFailure:            onFailure,
		LineItems:            make([]tax.RequestLine, len(lineItems)),
	}
	for i, li := range lineItems {
		req.LineItems[i] = tax.RequestLine{
			Ref:         fmt.Sprintf("line_%d", i),
			AmountCents: li.AmountCents,
			Quantity:    li.Quantity,
			TaxCode:     li.TaxCode,
		}
	}

	res, err := provider.Calculate(ctx, req)
	if err != nil {
		// Under OnFailureBlock policy the provider surfaces the error so we
		// can defer the invoice rather than silently charging the wrong tax.
		// The invoice is persisted with tax_status=pending + zero tax lines;
		// a background retry worker will re-run calculation and lift the
		// block when Stripe returns. Finalize is guarded downstream.
		slog.Warn("tax: provider calculation failed, deferring invoice",
			"error", err, "tenant_id", tenantID, "provider", provider.Name())
		for i := range lineItems {
			lineItems[i].TaxRateBP = 0
			lineItems[i].TaxAmountCents = 0
			lineItems[i].TotalAmountCents = lineItems[i].AmountCents
		}
		app.TaxStatus = domain.InvoiceTaxPending
		app.TaxPendingReason = truncateReason(err.Error(), 500)
		deferredAt := time.Now().UTC()
		if e.clock != nil {
			deferredAt = e.clock.Now()
		}
		app.TaxDeferredAt = &deferredAt
		// Persist the failed attempt so operators can see why. Uses a nil
		// result — the store's RecordFromResult handles that by marking the
		// provider "none" with the marshaled request as audit material.
		if e.taxCalcStore != nil {
			if _, perr := e.taxCalcStore.Record(ctx, tenantID, "", req, nil); perr != nil {
				slog.Warn("tax: failed to persist deferred tax_calculations row",
					"error", perr, "tenant_id", tenantID)
			}
		}
		return app, nil
	}

	app.TaxAmountCents = res.TotalTaxCents
	app.TaxRateBP = res.EffectiveRateBP
	if res.TaxName != "" {
		app.TaxName = res.TaxName
	}
	if res.TaxCountry != "" {
		app.TaxCountry = res.TaxCountry
	}
	app.TaxCalculationID = res.CalculationID
	app.TaxReverseCharge = res.ReverseCharge
	app.TaxExemptReason = res.ExemptReason

	// Apply per-line results. Index-aligned with lineItems because the
	// Request was built in the same order; Result.Lines[i] corresponds to
	// lineItems[i].
	var netSubtotalSum int64
	for i := range lineItems {
		if i >= len(res.Lines) {
			break
		}
		rl := res.Lines[i]
		net := rl.NetAmountCents
		if net == 0 {
			net = lineItems[i].AmountCents
		}
		lineItems[i].AmountCents = net
		lineItems[i].TaxRateBP = rl.TaxRateBP
		lineItems[i].TaxAmountCents = rl.TaxAmountCents
		lineItems[i].TaxJurisdiction = rl.Jurisdiction
		lineItems[i].TaxCode = rl.TaxCode
		lineItems[i].TotalAmountCents = net + rl.TaxAmountCents
		netSubtotalSum += net
	}

	// Inclusive mode: the provider returned net line amounts; rewrite
	// subtotal/discount to net units so the invoice invariant holds.
	if ts.TaxInclusive && netSubtotalSum != subtotal {
		app.SubtotalCents = netSubtotalSum
		// Gross paid == subtotal - discount, which after tax carve-out is
		// netSubtotalSum - netDiscount + tax. Solve for netDiscount.
		gross := subtotal - discount
		app.DiscountCents = max(netSubtotalSum+res.TotalTaxCents-gross, 0)
	}

	if e.taxCalcStore != nil {
		if _, err := e.taxCalcStore.Record(ctx, tenantID, "", req, res); err != nil {
			// Persistence failure doesn't block the invoice — log it. The
			// billing engine's correctness does not depend on the audit row.
			slog.Warn("tax: failed to persist tax_calculations row",
				"error", err, "tenant_id", tenantID, "provider", provider.Name())
		}
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

	// Guard: only bill active or trialing subscriptions. Trialing subs flow
	// through to the trial state machine below, which either advances the
	// cycle without billing (trial active) or atomically flips to active and
	// then bills (trial elapsed).
	if sub.Status != domain.SubscriptionActive && sub.Status != domain.SubscriptionTrialing {
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

	// Auto-resume pause_collection if resumes_at has passed. Stripe parity:
	// the cycle scan checks resumes_at at cycle time (not via a separate
	// timer) — when this period closes, if the pause was set to expire
	// somewhere inside it, the next period bills normally. The clear
	// updates sub.PauseCollection in-memory so the rest of this call
	// generates a finalized (not draft) invoice as if no pause had ever
	// been set.
	if sub.PauseCollection != nil && sub.PauseCollection.ResumesAt != nil && !sub.PauseCollection.ResumesAt.After(now) {
		updated, err := e.subs.ClearPauseCollection(ctx, sub.TenantID, sub.ID)
		if err != nil {
			slog.Warn("auto-resume pause_collection failed",
				"subscription_id", sub.ID,
				"error", err,
			)
		} else {
			sub = updated
			slog.Info("auto-resumed pause_collection",
				"subscription_id", sub.ID,
				"tenant_id", sub.TenantID,
			)
			if e.events != nil {
				_ = e.events.Dispatch(ctx, sub.TenantID, domain.EventSubscriptionCollectionResumed, map[string]any{
					"subscription_id": sub.ID,
					"customer_id":     sub.CustomerID,
					"resumed_at":      now.UTC(),
					"triggered_by":    "schedule",
				})
			}
		}
	}

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
		// Snapshot the item IDs whose pending change was due before the atomic
		// swap — ApplyDuePendingItemPlansAtomic returns the full refreshed item
		// set (due + not-due) with pending_plan_id cleared, so we can't tell
		// post-hoc which rows actually changed. This pre-swap list is what we
		// fire subscription.pending_change.applied events for.
		dueItems := make([]domain.SubscriptionItem, 0)
		for _, it := range sub.Items {
			if it.PendingPlanID != "" && it.PendingPlanEffectiveAt != nil && !it.PendingPlanEffectiveAt.After(now) {
				dueItems = append(dueItems, it)
			}
		}

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

			// Post-swap the plan swap is durable; fire one event per item that
			// transitioned. Emitted only on successful swap so a half-applied
			// state doesn't lie to webhook consumers.
			if e.events != nil {
				newPlanByItem := make(map[string]string, len(applied))
				for _, it := range applied {
					newPlanByItem[it.ID] = it.PlanID
				}
				for _, was := range dueItems {
					payload := map[string]any{
						"subscription_id": sub.ID,
						"customer_id":     sub.CustomerID,
						"item_id":         was.ID,
						"old_plan_id":     was.PlanID,
						"new_plan_id":     newPlanByItem[was.ID],
						"applied_at":      now.UTC(),
					}
					if err := e.events.Dispatch(ctx, sub.TenantID, domain.EventSubscriptionPendingChangeApplied, payload); err != nil {
						slog.Error("dispatch subscription.pending_change.applied",
							"subscription_id", sub.ID,
							"item_id", was.ID,
							"tenant_id", sub.TenantID,
							"error", err,
						)
					}
				}
			}
		}
	}

	if len(sub.Items) == 0 {
		return false, fmt.Errorf("subscription has no items to bill")
	}

	periodStart := *sub.CurrentBillingPeriodStart
	periodEnd := *sub.CurrentBillingPeriodEnd

	// Trial state machine. Two cases:
	//
	// (a) status='trialing' AND now < trial_end_at: trial is still running.
	//     Skip billing, advance cycle so we revisit at the next boundary.
	//     trial_end_at may not align with period_end — when it doesn't,
	//     the next-cycle visit will fall into case (b).
	//
	// (b) status='trialing' AND now >= trial_end_at: trial has elapsed.
	//     Atomically flip to 'active' and stamp activated_at, fire
	//     subscription.trial_ended (triggered_by="schedule"), then
	//     continue with normal billing for this period. The atomic
	//     UPDATE protects against a concurrent operator EndTrial racing
	//     the scheduler.
	//
	// Subs whose status is no longer 'trialing' (operator already ended
	// the trial, or the row was created without a trial in the first
	// place) skip both branches and fall through to normal billing.
	if sub.Status == domain.SubscriptionTrialing {
		trialOver := sub.TrialEndAt == nil || !now.Before(*sub.TrialEndAt)
		if !trialOver {
			nextBilling := advanceBillingPeriod(periodEnd, domain.BillingMonthly)
			slog.Info("skipping billing (trial active)", "subscription_id", sub.ID)
			return false, e.subs.UpdateBillingCycle(ctx, sub.TenantID, sub.ID, periodEnd, nextBilling, nextBilling)
		}
		updated, err := e.subs.ActivateAfterTrial(ctx, sub.TenantID, sub.ID, now)
		if err != nil {
			slog.Warn("auto-activate after trial failed",
				"subscription_id", sub.ID,
				"error", err,
			)
		} else {
			sub = updated
			slog.Info("trial ended, transitioned to active",
				"subscription_id", sub.ID,
				"tenant_id", sub.TenantID,
			)
			if e.events != nil {
				_ = e.events.Dispatch(ctx, sub.TenantID, domain.EventSubscriptionTrialEnded, map[string]any{
					"subscription_id": sub.ID,
					"customer_id":     sub.CustomerID,
					"ended_at":        now.UTC(),
					"triggered_by":    "schedule",
				})
			}
		}
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

	// Enforce usage cap if configured. Cap is a subscription-level total
	// across all meters — a container-level guardrail, not a per-plan
	// constraint. Cap stays integer (UsageCapUnits int64) because operators
	// author it as a whole-unit ceiling; per-meter quantities are decimal
	// and prorated proportionally if the cap fires.
	if sub.UsageCapUnits != nil && *sub.UsageCapUnits > 0 && sub.OverageAction == "block" {
		totalUsage := decimal.Zero
		for _, qty := range usageTotals {
			totalUsage = totalUsage.Add(qty)
		}
		capDec := decimal.NewFromInt(*sub.UsageCapUnits)
		if totalUsage.GreaterThan(capDec) {
			for mid, qty := range usageTotals {
				usageTotals[mid] = qty.Mul(capDec).Div(totalUsage)
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
			if !ok || quantity.IsZero() {
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

			// Per-unit amount on the invoice line is informational; the
			// authoritative number is amount_cents from the rule. Compute as
			// amount/quantity in decimal space, banker-round to int cents.
			// Quantity here is decimal (fractional usage allowed) and cannot
			// be zero — the IsZero check above already short-circuited.
			unitAmount := decimal.NewFromInt(amount).Div(quantity).RoundBank(0).IntPart()

			lineItems = append(lineItems, domain.InvoiceLineItem{
				LineType:    domain.LineTypeUsage,
				MeterID:     meterID,
				Description: fmt.Sprintf("%s (%s)", meter.Name, meter.Unit),
				// Quantity is truncated to int for the line item — fractional
				// quantities (e.g. 1.5 GPU-hours) are supported in pricing
				// math but the line item display column is still integer.
				// Followup: widen InvoiceLineItem.Quantity to NUMERIC.
				Quantity:            quantity.IntPart(),
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
	//
	// appliedRedemptionIDs (subscription-scope) and appliedCustomerDiscountIDs
	// (customer-scope) carry state across the invoice-create boundary: we
	// must only advance periods_applied AFTER the invoice is durably
	// persisted, otherwise a create failure would burn a period of a
	// repeating coupon that the customer never actually got. The two lists
	// feed separate writer methods because customer_discounts is its own
	// table — see CouponApplier.MarkCustomerDiscountPeriodsApplied.
	var discountCents int64
	var appliedRedemptionIDs []string
	var appliedCustomerDiscountIDs []string
	if e.coupons != nil && subtotal > 0 {
		if sub.ID != "" {
			d, err := e.coupons.ApplyToInvoice(ctx, sub.TenantID, sub.ID, sub.CustomerID, invoiceCurrency, planIDs, subtotal)
			if err != nil {
				slog.Warn("coupon apply failed, proceeding without discount",
					"error", err, "subscription_id", sub.ID)
			} else {
				discountCents = d.Cents
				appliedRedemptionIDs = d.RedemptionIDs
			}
		}
		// Customer-scope fallback: only runs when subscription-scope
		// produced no discount (or there's no subscription at all).
		// Stripe's rule — subscription.discount beats customer.discount on
		// the same invoice, so we never stack the two.
		if discountCents == 0 && sub.CustomerID != "" {
			d, err := e.coupons.ApplyToInvoiceForCustomer(ctx, sub.TenantID, sub.CustomerID, invoiceCurrency, planIDs, subtotal)
			if err != nil {
				slog.Warn("customer coupon apply failed, proceeding without discount",
					"error", err, "customer_id", sub.CustomerID)
			} else {
				discountCents = d.Cents
				appliedCustomerDiscountIDs = d.RedemptionIDs
			}
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

	// When tax was deferred the invoice must stay in draft: a finalized
	// invoice implies the amounts (including tax) are authoritative, which
	// they are not until the retry worker completes the calculation. The
	// retry worker lifts the block and transitions draft → finalized.
	//
	// pause_collection (Stripe parity): when a sub has pause_collection set
	// (still non-nil after the auto-resume check above), force draft. The
	// engine still runs the cycle and produces line items so the period is
	// captured and aging behaves normally; the operator/customer flow that
	// would finalize, charge, and dunn is skipped until pause is cleared.
	invStatus := domain.InvoiceFinalized
	if taxApp.TaxStatus == domain.InvoiceTaxPending {
		invStatus = domain.InvoiceDraft
	}
	collectionPaused := sub.PauseCollection != nil
	if collectionPaused {
		invStatus = domain.InvoiceDraft
	}

	// ATOMIC: Create invoice + all line items in a single transaction.
	// This prevents orphaned invoices with missing line items on partial failure.
	// The unique index on (tenant_id, subscription_id, billing_period_start, billing_period_end)
	// provides idempotency — duplicate calls return an error instead of double-billing.
	inv, err := e.invoices.CreateInvoiceWithLineItems(ctx, sub.TenantID, domain.Invoice{
		CustomerID:         sub.CustomerID,
		SubscriptionID:     sub.ID,
		InvoiceNumber:      invoiceNumber,
		Status:             invStatus,
		PaymentStatus:      domain.PaymentPending,
		Currency:           invoiceCurrency,
		SubtotalCents:      taxApp.SubtotalCents,
		DiscountCents:      taxApp.DiscountCents,
		TaxRateBP:          taxRateBP,
		TaxName:            taxName,
		TaxCountry:         taxCountry,
		TaxID:              taxID,
		TaxAmountCents:     taxAmountCents,
		TaxProvider:        taxApp.TaxProvider,
		TaxCalculationID:   taxApp.TaxCalculationID,
		TaxReverseCharge:   taxApp.TaxReverseCharge,
		TaxExemptReason:    taxApp.TaxExemptReason,
		TaxStatus:          taxApp.TaxStatus,
		TaxDeferredAt:      taxApp.TaxDeferredAt,
		TaxPendingReason:   taxApp.TaxPendingReason,
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
			_ = e.advanceCycleOrCancel(ctx, sub, periodEnd, nextPeriodStart, nextPeriodEnd, now)
			return false, nil
		}
		return false, fmt.Errorf("create invoice: %w", err)
	}

	// Stripe Tax: once the invoice is durably persisted, commit the
	// tax_calculation into a tax_transaction so Stripe can report the
	// liability. Failures here don't unwind the invoice — the calculation
	// row survives as an audit trail and we surface the failure via logs
	// + metrics for the tenant to reconcile. Manual/none providers have
	// Commit as a no-op so this path is safe to call unconditionally.
	if inv.TaxProvider != "" && inv.TaxCalculationID != "" {
		if err := e.CommitTax(ctx, sub.TenantID, inv.ID, inv.TaxCalculationID); err != nil {
			slog.Warn("tax: commit failed after invoice creation",
				"error", err,
				"tenant_id", sub.TenantID,
				"invoice_id", inv.ID,
				"provider", inv.TaxProvider,
				"tax_calculation_id", inv.TaxCalculationID)
		}
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
	if e.coupons != nil && len(appliedCustomerDiscountIDs) > 0 {
		if err := e.coupons.MarkCustomerDiscountPeriodsApplied(ctx, sub.TenantID, appliedCustomerDiscountIDs); err != nil {
			slog.Warn("customer-discount mark-periods-applied failed",
				"invoice_id", inv.ID,
				"customer_id", sub.CustomerID,
				"error", err)
		}
	}

	// Apply customer credits before charging. ApplyToInvoice is atomic:
	// it both debits the credit ledger AND reduces the invoice's amount_due_cents
	// in a single transaction. A failure leaves both unchanged — no dual-write
	// hole where credits are consumed but the invoice still shows the pre-credit
	// amount due (which would double-bill the customer via Stripe).
	//
	// Skip during pause_collection — credits should not be consumed against a
	// draft invoice that may never be finalized; the credit will apply when
	// collection resumes and the invoice transitions out of draft.
	if e.credits != nil && totalWithTax > 0 && !collectionPaused {
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
				if err := e.advanceCycleOrCancel(ctx, sub, periodEnd, nextPeriodStart, nextPeriodEnd, now); err != nil {
					return true, fmt.Errorf("advance billing cycle: %w", err)
				}
				return true, nil
			}
		}
	}

	// Auto-charge: synchronous with timeout. If it fails, mark for scheduler retry
	// instead of fire-and-forget goroutine that loses failures.
	//
	// Skip entirely when pause_collection is set — the invoice is draft so
	// charging it would be a state-violation; dunning is also off the table
	// because finalize hasn't happened. This is the Stripe-parity behavior:
	// pause_collection neuters the financial side without touching the cycle.
	if e.charger != nil && e.paymentSetups != nil && inv.AmountDueCents > 0 && !collectionPaused {
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

	// Advance billing cycle (or fire scheduled cancel if due)
	nextPeriodStart := periodEnd
	nextPeriodEnd := advanceBillingPeriod(periodEnd, plans[sub.Items[0].PlanID].BillingInterval)

	if err := e.advanceCycleOrCancel(ctx, sub, periodEnd, nextPeriodStart, nextPeriodEnd, now); err != nil {
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

// ApplyCouponToInvoice applies a coupon to an already-issued draft
// invoice (the operator-initiated "apply this coupon to this invoice"
// flow). Orchestrates the full change:
//
//  1. Load the invoice and its line items, re-assert the state gates
//     (draft, no existing discount, tax not yet committed upstream).
//  2. If the invoice is tied to a subscription, resolve the full plan
//     set so a PlanIDs-restricted coupon matches any-one-of.
//  3. Redeem the coupon (creates redemption row + bumps times_redeemed).
//  4. Recompute tax against (subtotal - discount). Inclusive mode's net
//     carve flows back through TaxApplication.
//  5. Persist the new discount + tax snapshot atomically via
//     ApplyDiscountAtomic (lock + gate re-check inside the tx).
//  6. Advance periods_applied on the redemption.
//
// Compensation: if step 5 fails, step 3 is rolled back via
// VoidRedemptionsForInvoice so a failed apply doesn't burn a coupon
// period or inflate times_redeemed. Replay path (idempotency-key hit
// on the redemption) still runs through steps 4–6 because a prior
// attempt may have crashed between Redeem and ApplyDiscountAtomic;
// the atomic step itself re-asserts discount_cents=0 so a true double
// apply surfaces as InvalidState, not silent overwrite.
func (e *Engine) ApplyCouponToInvoice(ctx context.Context, tenantID, invoiceID, code, idempotencyKey string) (domain.Invoice, error) {
	if e.coupons == nil {
		return domain.Invoice{}, errs.InvalidState("coupon service not configured")
	}

	inv, err := e.invoices.GetInvoice(ctx, tenantID, invoiceID)
	if err != nil {
		return domain.Invoice{}, err
	}

	if inv.Status != domain.InvoiceDraft {
		return domain.Invoice{}, errs.InvalidState(fmt.Sprintf(
			"invoice must be draft to apply a coupon (current: %s)", inv.Status))
	}
	if inv.DiscountCents > 0 {
		return domain.Invoice{}, errs.InvalidState("invoice already has a discount applied")
	}
	if inv.TaxTransactionID != "" {
		return domain.Invoice{}, errs.InvalidState("invoice tax has already been committed upstream")
	}
	if inv.SubtotalCents <= 0 {
		return domain.Invoice{}, errs.InvalidState("invoice has no subtotal to discount")
	}

	items, err := e.invoices.ListLineItems(ctx, tenantID, invoiceID)
	if err != nil {
		return domain.Invoice{}, fmt.Errorf("list line items: %w", err)
	}

	// Plan set is used by the coupon's PlanIDs restriction gate. Empty plan
	// list (no subscription, or subscription-less invoice) disables the gate.
	var planIDs []string
	if inv.SubscriptionID != "" && e.subs != nil {
		if sub, err := e.subs.Get(ctx, tenantID, inv.SubscriptionID); err == nil {
			planIDs = make([]string, 0, len(sub.Items))
			for _, it := range sub.Items {
				planIDs = append(planIDs, it.PlanID)
			}
		}
	}

	redeemRes, err := e.coupons.RedeemForInvoice(ctx, tenantID, domain.CouponRedeemRequest{
		Code:           code,
		CustomerID:     inv.CustomerID,
		SubscriptionID: inv.SubscriptionID,
		InvoiceID:      inv.ID,
		SubtotalCents:  inv.SubtotalCents,
		Currency:       inv.Currency,
		IdempotencyKey: idempotencyKey,
		PlanIDs:        planIDs,
	})
	if err != nil {
		return domain.Invoice{}, err
	}

	discountCents := redeemRes.Redemption.DiscountCents
	if discountCents <= 0 {
		// Defence-in-depth: the service clamps zero-discount redemptions
		// at the gate, so this should never fire. Void anything that did
		// commit to keep times_redeemed honest.
		if !redeemRes.Replay {
			_, _ = e.coupons.VoidRedemptionsForInvoice(ctx, tenantID, invoiceID)
		}
		return domain.Invoice{}, errs.InvalidState("coupon produced zero discount")
	}

	taxApp, err := e.ApplyTaxToLineItems(ctx, tenantID, inv.CustomerID, inv.Currency,
		inv.SubtotalCents, discountCents, items)
	if err != nil {
		if !redeemRes.Replay {
			_, _ = e.coupons.VoidRedemptionsForInvoice(ctx, tenantID, invoiceID)
		}
		return domain.Invoice{}, fmt.Errorf("recompute tax: %w", err)
	}

	update := domain.InvoiceDiscountUpdate{
		SubtotalCents:    taxApp.SubtotalCents,
		DiscountCents:    taxApp.DiscountCents,
		TaxAmountCents:   taxApp.TaxAmountCents,
		TaxRateBP:        taxApp.TaxRateBP,
		TaxName:          taxApp.TaxName,
		TaxCountry:       taxApp.TaxCountry,
		TaxID:            taxApp.TaxID,
		TaxProvider:      taxApp.TaxProvider,
		TaxCalculationID: taxApp.TaxCalculationID,
		TaxReverseCharge: taxApp.TaxReverseCharge,
		TaxExemptReason:  taxApp.TaxExemptReason,
		TaxStatus:        taxApp.TaxStatus,
		TaxDeferredAt:    taxApp.TaxDeferredAt,
		TaxPendingReason: taxApp.TaxPendingReason,
	}

	updated, err := e.invoices.ApplyDiscountAtomic(ctx, tenantID, invoiceID, update, items)
	if err != nil {
		if !redeemRes.Replay {
			if _, vErr := e.coupons.VoidRedemptionsForInvoice(ctx, tenantID, invoiceID); vErr != nil {
				slog.Error("coupon: compensating void after apply-coupon failure also failed",
					"invoice_id", invoiceID, "apply_error", err, "void_error", vErr)
			}
		}
		return domain.Invoice{}, fmt.Errorf("apply discount: %w", err)
	}

	// Advance periods_applied on the committed redemption. Failure here is
	// logged, not surfaced — the invoice is already updated and the worst
	// case is a repeating coupon applying one extra cycle, which is
	// preferable to rolling back a durable financial mutation.
	if !redeemRes.Replay && redeemRes.Redemption.ID != "" {
		if err := e.coupons.MarkPeriodsApplied(ctx, tenantID, []string{redeemRes.Redemption.ID}); err != nil {
			slog.Warn("coupon mark-periods-applied failed after apply-coupon",
				"invoice_id", invoiceID, "redemption_id", redeemRes.Redemption.ID, "error", err)
		}
	}

	return updated, nil
}

func advanceBillingPeriod(from time.Time, interval domain.BillingInterval) time.Time {
	switch interval {
	case domain.BillingYearly:
		return from.AddDate(1, 0, 0)
	default:
		return from.AddDate(0, 1, 0)
	}
}
