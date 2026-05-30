package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/shopspring/decimal"

	"github.com/sagarsuperuser/velox/internal/auth"
	"github.com/sagarsuperuser/velox/internal/credit"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/payment"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/clock"
	"github.com/sagarsuperuser/velox/internal/platform/money"
	"github.com/sagarsuperuser/velox/internal/platform/telemetry"
	"github.com/sagarsuperuser/velox/internal/tax"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// roundDays converts a duration to whole-day count, rounded to nearest.
// Period boundaries snapped to 00:00 in tenant TZ produce durations
// that are either exact day-multiples or within ±1h of one (DST
// spring-forward subtracts an hour, fall-back adds one). Round absorbs
// the DST drift; truncation would silently miscount.
func roundDays(d time.Duration) int {
	return int(math.Round(d.Hours() / 24))
}

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
	creditGranter CreditGranter
	settings      SettingsReader
	paymentSetups PaymentReadiness
	charger       InvoiceCharger
	profiles      BillingProfileReader
	customers     CustomerReader
	taxProviders  TaxProviderResolver
	taxCalcStore  TaxCalculationWriter
	clock         clock.Clock
	testClocks    TestClockReader
	events        domain.EventDispatcher
	noPMNotifier  NoPaymentMethodNotifier
	auditLogger   AuditWriter
}

// AuditWriter is the narrow audit surface the engine needs. Defined
// here (not imported from internal/audit) so billing doesn't gain a
// reverse import of the audit package — production wires
// *audit.Logger via SetAuditLogger in router.go. Optional: nil = engine
// skips the audit write but still mutates state + dispatches webhooks.
type AuditWriter interface {
	Log(ctx context.Context, tenantID, action, resourceType, resourceID, resourceLabel string, metadata map[string]any) error
}

// SetAuditLogger wires the audit logger used by the engine to record
// background-fired lifecycle events (currently: scheduled cancellation
// firing at period end). Without this, those auto-fires only show in
// outbound webhooks and slog — the operator Activity feed misses them.
func (e *Engine) SetAuditLogger(a AuditWriter) {
	e.auditLogger = a
}

// SetCreditGranter wires the credit-grant issuer used by BillOnCancel
// for cancel proration on in_advance plans (ADR-031). Optional —
// without it, in_advance subs cancel without a proration credit.
func (e *Engine) SetCreditGranter(g CreditGranter) {
	e.creditGranter = g
}

// CustomerReader is the narrow read interface the engine uses to
// resolve a customer's test_clock_id pin (ADR-027). Implemented by
// *customer.PostgresStore. Optional — when nil, EffectiveNowForCustomer
// falls back to wall-clock; this is the safe default for narrow unit
// tests that don't exercise customer-level clock pins.
type CustomerReader interface {
	Get(ctx context.Context, tenantID, id string) (domain.Customer, error)
}

// SetCustomerReader wires the customer reader used by
// EffectiveNowForCustomer (and transitively EffectiveNowForInvoice on
// one-off invoices). Production wires *customer.PostgresStore via
// api/router.go.
func (e *Engine) SetCustomerReader(r CustomerReader) {
	e.customers = r
}

// Compile-time assertion that *Engine satisfies clock.Resolver. Lets
// every domain bind effective-now via the platform/clock package
// without importing billing — clock owns the interface, billing owns
// the implementation. Replaces the per-service ClockResolver
// interfaces we threaded through dunning / subscription / invoice in
// the post-ADR-029 patches.
var _ clock.Resolver = (*Engine)(nil)

// NoPaymentMethodNotifier dispatches a customer-facing email when an
// invoice finalizes for a customer with no PaymentSetup ready. Without
// this, the customer would never know to add a card and the invoice
// would silently sit unpaid until it goes overdue and dunning fires
// — too late for happy-path collection. Stripe sends an equivalent
// "Action required: payment method needed" email at finalize for
// failed charges; we extend the same pattern to the no-PM case so the
// customer experience is symmetric.
//
// Optional — when nil, engine skips the notification (local dev,
// integration tests). Wire in router.go via SetNoPaymentMethodNotifier.
type NoPaymentMethodNotifier interface {
	NotifyNoPaymentMethod(ctx context.Context, tenantID string, inv domain.Invoice) error
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
// ApplyToInvoiceAt is the simulated-time-aware variant — engine callers
// pass their `now` (cycle close instant) so the ledger usage entry +
// invoice updated_at land on simulated time rather than advance-end
// frozen_time during catchup.
type CreditApplier interface {
	ApplyToInvoiceAt(ctx context.Context, tenantID, customerID, invoiceID string, amountCents int64, at time.Time, invoiceNumber ...string) (int64, error)
}

// CreditGranter issues a new credit grant. Used by BillOnCancel
// (ADR-031 slice 3) to refund the unused portion of an already-
// billed in_advance period to the customer's credit balance.
// Implemented by *credit.Service.
type CreditGranter interface {
	Grant(ctx context.Context, tenantID string, input credit.GrantInput) (domain.CreditLedgerEntry, error)
}

// BillingProfileReader reads customer billing profiles for tax exemption checks.
type BillingProfileReader interface {
	GetBillingProfile(ctx context.Context, tenantID, customerID string) (domain.CustomerBillingProfile, error)
}

// PaymentReadiness resolves the data the engine needs to decide
// "can we auto-charge this customer's invoice right now?" Two values
// in one call — the Stripe Customer ID (required as the PI customer
// param) plus a flag for whether the customer has an active default
// PM. Replaces the older PaymentReadiness / GetPaymentSetup shape
// (customer_payment_setups table, retired in migration 0097): the
// Stripe Customer ID lives on customers.stripe_customer_id, the PM-
// presence check queries payment_methods canonically.
type PaymentReadiness interface {
	ResolveForCharge(ctx context.Context, tenantID, customerID string) (stripeCustomerID string, hasDefaultPM bool, err error)
}

// InvoiceCharger creates a Stripe PaymentIntent for a finalized invoice.
// Narrow interface — default-mode charge only. Callers that need to
// tag the PI for special routing (the dunning retrier marks PIs with
// velox_purpose=dunning_retry so the webhook suppresses the duplicate
// payment-failed email) access *payment.Stripe directly and use the
// dedicated typed method (ChargeInvoiceForDunningRetry). Keeps engine
// package free of payment-options surface.
type InvoiceCharger interface {
	ChargeInvoice(ctx context.Context, tenantID string, inv domain.Invoice, stripeCustomerID string) (domain.Invoice, error)
}

// SubscriptionReader reads subscription and plan data for billing.
type SubscriptionReader interface {
	// GetDueBilling: wall-clock cron path. Returns ONLY subs that
	// are NOT pinned to a test clock (ADR-028 disjoint flows). The
	// scheduler tick fans this out per livemode.
	GetDueBilling(ctx context.Context, before time.Time, limit int) ([]domain.Subscription, error)
	// GetDueBillingForClock: operator-driven catchup path. Returns
	// ONLY subs pinned to the given clock whose next_billing_at is
	// on-or-before that clock's frozen time. Called by RunCatchup
	// after Advance flips status to 'advancing'.
	GetDueBillingForClock(ctx context.Context, tenantID, clockID string, limit int) ([]domain.Subscription, error)
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

	// ListWithThresholds returns active+trialing subscriptions in the given
	// livemode partition that have at least one billing threshold configured
	// (amount or per-item). Drives the threshold scan tick — each row is
	// rated against its current partial-cycle running totals to decide
	// whether to fire an early finalize. Hydrated with Items and
	// BillingThresholds so the scan doesn't issue per-sub follow-up reads.
	ListWithThresholds(ctx context.Context, livemode bool, limit int) ([]domain.Subscription, error)
	// ListWithThresholdsForClock is the catchup-path counterpart to
	// ListWithThresholds — returns clock-pinned subs with thresholds
	// configured. ADR-029 Phase 3.
	ListWithThresholdsForClock(ctx context.Context, tenantID, clockID string, limit int) ([]domain.Subscription, error)

	// ListItemChangesInPeriod returns every subscription_item_changes row
	// for the given subscription whose changed_at falls within
	// (periodStart, periodEnd]. Drives segment-aware base-fee billing at
	// cycle close — each row marks a boundary in the period at which the
	// plan or quantity changed, and each [boundary_n, boundary_{n+1}]
	// segment is billed at its own rate × duration. Lago / Chargebee /
	// Orb shape for mid-period proration.
	ListItemChangesInPeriod(ctx context.Context, tenantID, subscriptionID string, periodStart, periodEnd time.Time) ([]domain.SubscriptionItemChange, error)
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
	// FindBaseInvoiceForPeriod returns the invoice carrying the in_advance
	// base-fee line for a subscription's period (line's
	// billing_period_start = periodStart). Gates proration-credit emission
	// in BillOnCancel — only paid in_advance invoices warrant a refund-
	// style credit. Industry-aligned: Chargebee distinguishes Refundable
	// (paid source) vs Adjustment (unpaid source) credits; Stripe warns
	// to disable proration when the source invoice is unpaid.
	FindBaseInvoiceForPeriod(ctx context.Context, tenantID, subscriptionID string, periodStart time.Time) (domain.Invoice, error)
	ListLineItems(ctx context.Context, tenantID, invoiceID string) ([]domain.InvoiceLineItem, error)
	MarkPaid(ctx context.Context, tenantID, id string, stripePaymentIntentID string, paidAt time.Time) (domain.Invoice, error)
	SetAutoChargePending(ctx context.Context, tenantID, id string, pending bool) error
	ListAutoChargePending(ctx context.Context, limit int) ([]domain.Invoice, error)
	// ListAutoChargePendingForClock is the catchup-path counterpart to
	// ListAutoChargePending — returns invoices whose owning subscription
	// is pinned to the given clock. ADR-029 Phase 1: simulation-time
	// charge attempts only fire on operator Advance, never on the
	// wall-clock cron tick, mirroring Stripe Test Clocks.
	ListAutoChargePendingForClock(ctx context.Context, tenantID, clockID string, limit int) ([]domain.Invoice, error)
	// SetTaxTransaction persists the upstream provider's tax_transaction
	// reference (Stripe: tx_xxx) after CommitTax succeeds. Required for
	// later reversal when a credit note is issued against the invoice.
	SetTaxTransaction(ctx context.Context, tenantID, id string, taxTransactionID string) error
	// ApplyDiscountAtomic stamps a coupon discount + recomputed tax
	// snapshot onto a draft invoice in one tx. Used by
	// Engine.ApplyCouponToInvoice for the apply-coupon-after-issue flow.
	ApplyDiscountAtomic(ctx context.Context, tenantID, invoiceID string, update domain.InvoiceDiscountUpdate, lineItems []domain.InvoiceLineItem) (domain.Invoice, error)
	// UpdateTaxAtomic re-stamps an invoice's tax decision after a manual
	// retry. Used by Engine.RetryTaxForInvoice to persist the recomputed
	// per-line and invoice-level tax fields atomically.
	UpdateTaxAtomic(ctx context.Context, tenantID, invoiceID string, update domain.InvoiceTaxRetryUpdate, lineItems []domain.InvoiceLineItem) (domain.Invoice, error)
}

func NewEngine(subs SubscriptionReader, usage UsageAggregator, pricing PricingReader, invoices InvoiceWriter, credits CreditApplier, settings SettingsReader, paymentSetups PaymentReadiness, charger InvoiceCharger, clk clock.Clock, profiles ...BillingProfileReader) *Engine {
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
//
// LookupCalculationCreatedAt powers the CommitTax expiry guard.
// Stripe Tax calculations expire 24h after creation — operator-finalized
// drafts held longer than that quietly fail at commit time and leave
// the invoice with tax_calculation_id but no tax_transaction_id (i.e.
// Stripe Tax reporting silently broken). Returning the calc's age lets
// CommitTax fail loud before calling Stripe so the operator knows to
// retry tax first. Returns errs.ErrNotFound when no row matches —
// caller treats as "skip the guard" (manual / none provider produces
// no row; defensive against tests that wire a fake store without the
// row pre-loaded).
type TaxCalculationWriter interface {
	Record(ctx context.Context, tenantID, invoiceID string, req tax.Request, res *tax.Result) (string, error)
	LookupCalculationCreatedAt(ctx context.Context, tenantID, invoiceID, providerRef string) (time.Time, error)
}

// taxCalculationMaxAge is the Stripe Tax calculation window. Stripe's
// own docs put it at 24h; we use 23h to leave a safety buffer so a
// near-expiry calc isn't sent to Stripe just to bounce. Operator
// guidance on expiry: "Retry tax to refresh, then finalize."
const taxCalculationMaxAge = 23 * time.Hour

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
	// Pin tenant on ctx — see ApplyTaxToLineItems for the rationale.
	ctx = auth.WithTenantID(ctx, tenantID)
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
	// Expiry guard. Stripe Tax calculations are valid for 24h after
	// creation; operator-finalized drafts held longer fail at Stripe's
	// CreateFromCalculation with "calculation expired" and previously
	// left the invoice with tax_calculation_id but no
	// tax_transaction_id. Fail loud here so the operator sees a clear
	// "retry tax to refresh" message instead of a silent reporting gap.
	// Skipped when the store isn't wired (engine tests) — the store
	// row IS the source of truth for calc creation time, and tests
	// without it shouldn't be gated by a guard they didn't opt into.
	if e.taxCalcStore != nil && calculationID != "" {
		createdAt, lookupErr := e.taxCalcStore.LookupCalculationCreatedAt(ctx, tenantID, invoiceID, calculationID)
		if lookupErr == nil {
			if age := clock.Now(ctx).Sub(createdAt); age > taxCalculationMaxAge {
				return errs.InvalidState(fmt.Sprintf(
					"tax calculation expired (age %s, max %s) — retry tax to refresh, then finalize",
					age.Truncate(time.Minute), taxCalculationMaxAge))
			}
		} else if !errors.Is(lookupErr, errs.ErrNotFound) {
			// Lookup failure on a real DB error: log and fall through.
			// Better to attempt commit and let Stripe reject than to
			// block finalize on a transient DB blip.
			slog.Warn("tax: expiry-guard lookup failed; attempting commit anyway",
				"error", lookupErr, "tenant_id", tenantID, "invoice_id", invoiceID,
				"calculation_id", calculationID)
		}
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
	// Pin tenant on ctx — see ApplyTaxToLineItems for the rationale.
	ctx = auth.WithTenantID(ctx, tenantID)
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

// SetNoPaymentMethodNotifier wires the customer-notification dispatcher
// fired when an invoice finalizes without a PaymentSetup ready. See
// the NoPaymentMethodNotifier doc-comment for the full rationale.
func (e *Engine) SetNoPaymentMethodNotifier(n NoPaymentMethodNotifier) {
	e.noPMNotifier = n
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

// baseSegment is one [start, end] slice of a period during which an
// item had a fixed plan + quantity. Segment-aware base-fee billing
// emits one line per segment at the segment's own plan rate × duration
// fraction, matching the Lago / Chargebee / Orb shape for mid-period
// proration. Single-segment items (no mid-period changes) collapse
// to the pre-segment-aware single-line behavior.
type baseSegment struct {
	start, end time.Time
	planID     string
	quantity   int64
}

// itemBaseSegments walks a chronologically-ordered slice of changes
// for one subscription item and produces the [start, end, plan, qty]
// segments that span [periodStart, periodEnd]. Handles every shape:
//
//   - No changes → one full-period segment at the item's current state.
//   - Plan or quantity change → two segments (before / after the change).
//   - Item added mid-period → first segment starts at the add time.
//   - Item removed mid-period → last segment ends at the remove time;
//     no tail segment beyond remove.
//   - Item both added AND removed in the same period → segment(s) only
//     span [add, remove].
//
// item may be nil for items that no longer exist at periodEnd
// (removed mid-period); in that case the tail state is determined
// entirely from the last change's to_* fields (or absent if the last
// change is 'remove').
func itemBaseSegments(item *domain.SubscriptionItem, changes []domain.SubscriptionItemChange, periodStart, periodEnd time.Time) []baseSegment {
	if len(changes) == 0 {
		if item == nil {
			return nil
		}
		return []baseSegment{{
			start: periodStart, end: periodEnd,
			planID: item.PlanID, quantity: item.Quantity,
		}}
	}

	// Initial state at periodStart, derived from the first change.
	// 'add' means the item didn't exist at periodStart.
	first := changes[0]
	exists := first.ChangeType != "add"
	var planID string
	var quantity int64
	if exists {
		planID = first.FromPlanID
		quantity = first.FromQuantity
	}

	prevTime := periodStart
	out := []baseSegment{}
	for _, c := range changes {
		if exists && c.ChangedAt.After(prevTime) {
			out = append(out, baseSegment{
				start: prevTime, end: c.ChangedAt,
				planID: planID, quantity: quantity,
			})
		}
		prevTime = c.ChangedAt
		switch c.ChangeType {
		case "add", "plan", "quantity":
			planID = c.ToPlanID
			quantity = c.ToQuantity
			exists = true
		case "remove":
			exists = false
		}
	}

	if exists && periodEnd.After(prevTime) {
		out = append(out, baseSegment{
			start: prevTime, end: periodEnd,
			planID: planID, quantity: quantity,
		})
	}
	return out
}

// usageInterval is one [start, end) range during which a meter was
// active for the subscription. Built per-segment from the same
// itemBaseSegments walk used for base-fee billing — Orb-shape
// segment-aware metering. Multiple items with overlapping segments on
// the same meter get merged so the meter is billed once per disjoint
// active range, not double-counted per item.
type usageInterval struct {
	start, end time.Time
}

// mergeUsageIntervals collapses overlapping or touching ranges into
// disjoint intervals. Sorted by start. Keeps the segment-aware usage
// loop from emitting two overlapping lines for the same meter when
// two items happen to share a segment range.
func mergeUsageIntervals(ivs []usageInterval) []usageInterval {
	if len(ivs) == 0 {
		return nil
	}
	sort.Slice(ivs, func(i, j int) bool { return ivs[i].start.Before(ivs[j].start) })
	out := []usageInterval{ivs[0]}
	for _, iv := range ivs[1:] {
		last := &out[len(out)-1]
		// Touching or overlapping intervals merge — covers the
		// boundary-swap case where seg1.end == seg2.start.
		if !iv.start.After(last.end) {
			if iv.end.After(last.end) {
				last.end = iv.end
			}
			continue
		}
		out = append(out, iv)
	}
	return out
}

// emitBaseSegmentLine pushes a base-fee line item for one [start, end]
// segment of an in_arrears period. Prorated against the cycle length
// (full plan-interval cycle from periodStart) so a 14-day segment of
// a 30-day cycle bills at 14/30 of the segment's plan amount. Single-
// segment items (no mid-period changes) collapse to "segment ==
// period" math, matching the pre-segment-aware single-line path.
func emitBaseSegmentLine(seg baseSegment, plan domain.Plan, periodStart time.Time, periodDays int, currency string, lineItems *[]domain.InvoiceLineItem, subtotal *int64) {
	segDays := roundDays(seg.end.Sub(seg.start))
	if segDays <= 0 {
		return
	}
	fullCycleDays := roundDays(advanceBillingPeriod(periodStart, plan.BillingInterval).Sub(periodStart))
	baseFee := plan.BaseAmountCents * seg.quantity
	description := fmt.Sprintf("%s - base fee (qty %d)", plan.Name, seg.quantity)

	// Prorate when:
	//   (a) this is a partial segment within a full cycle (mid-period
	//       change or partial-creation period), OR
	//   (b) the period itself is partial (creation mid-cycle), independent
	//       of segments — same formula reduces correctly.
	if segDays > 0 && fullCycleDays > 0 && segDays < fullCycleDays {
		baseFee = money.RoundHalfToEven(plan.BaseAmountCents*seg.quantity*int64(segDays), int64(fullCycleDays))
		description = fmt.Sprintf("%s - base fee (qty %d, prorated %d/%d days)", plan.Name, seg.quantity, segDays, fullCycleDays)
	} else if periodDays > 0 && fullCycleDays > 0 && periodDays < fullCycleDays {
		// Backstop for the periodDays<fullCycleDays case when only a
		// single segment exists and equals the whole period — preserves
		// the pre-segment-aware partial-creation behavior.
		baseFee = money.RoundHalfToEven(plan.BaseAmountCents*seg.quantity*int64(periodDays), int64(fullCycleDays))
		description = fmt.Sprintf("%s - base fee (qty %d, prorated %d/%d days)", plan.Name, seg.quantity, periodDays, fullCycleDays)
	}

	unitAmount := plan.BaseAmountCents
	if seg.quantity > 0 {
		unitAmount = money.RoundHalfToEven(baseFee, seg.quantity)
	}
	segStart := seg.start
	segEnd := seg.end
	*lineItems = append(*lineItems, domain.InvoiceLineItem{
		LineType:           domain.LineTypeBaseFee,
		Description:        description,
		Quantity:           seg.quantity,
		UnitAmountCents:    unitAmount,
		AmountCents:        baseFee,
		TotalAmountCents:   baseFee,
		Currency:           currency,
		BillingPeriodStart: &segStart,
		BillingPeriodEnd:   &segEnd,
	})
	*subtotal += baseFee
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

	// Audit row for the engine-initiated transition. Operator-side
	// cancel + portal-side cancel both write AuditActionCancel; this
	// is the third path (auto-fire at period end) that previously
	// skipped audit, leaving the subscription Activity timeline
	// showing "Cancellation scheduled" with no terminal event. The
	// canceled_by='schedule' field matches the outbound-webhook
	// shape so a CS rep reading both surfaces sees the same vocabulary.
	if e.auditLogger != nil {
		_ = e.auditLogger.Log(ctx, sub.TenantID, domain.AuditActionCancel, "subscription", canceled.ID, canceled.Code, map[string]any{
			"canceled_by": "schedule",
			"customer_id": canceled.CustomerID,
		})
	}

	if e.events != nil {
		payload := map[string]any{
			"subscription_id": canceled.ID,
			"customer_id":     canceled.CustomerID,
			"status":          string(canceled.Status),
			"canceled_at":     now.UTC(),
			"canceled_by":     "schedule",
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
		return e.clock.Now(ctx)
	}
	tc, err := e.testClocks.Get(ctx, sub.TenantID, sub.TestClockID)
	if err != nil {
		slog.Warn("test clock lookup failed, falling back to wall clock",
			"subscription_id", sub.ID, "test_clock_id", sub.TestClockID, "error", err)
		return e.clock.Now(ctx)
	}
	return tc.FrozenTime
}

// EffectiveNowForInvoice resolves the time anchor for any per-invoice
// state-machine step that needs to stay in the simulation. Resolves
// invoice → subscription → test_clock and returns frozen_time when
// pinned, wall-clock otherwise. Manual drafts (no subscription) fall
// back to the customer pin via EffectiveNowForCustomer — one-off
// invoices for clock-pinned customers stamp simulated time too.
//
// Used by dunning to keep `next_action_at` in the same time domain
// the catchup query compares against (`<= frozen_time`). Without this
// resolver, dunning would stamp wall-clock into a column the
// orchestrator reads as simulated-time — and clock-pinned runs whose
// stamps land outside the catchup window get stranded. ADR-029 follow-up.
//
// Errors fall back to wall-clock with a warn — same safety stance as
// effectiveNow: a dangling subscription / clock pointer can't stall an
// operator-triggered retry. The fallback may stamp the wrong domain
// for clock-pinned runs, but failing the operator's action is worse.
func (e *Engine) EffectiveNowForInvoice(ctx context.Context, tenantID, invoiceID string) (time.Time, error) {
	inv, err := e.invoices.GetInvoice(ctx, tenantID, invoiceID)
	if err != nil {
		return e.clock.Now(ctx), fmt.Errorf("get invoice for clock resolution: %w", err)
	}
	if inv.SubscriptionID == "" {
		// One-off invoice: customer may still be clock-pinned (ADR-027).
		return e.EffectiveNowForCustomer(ctx, tenantID, inv.CustomerID)
	}
	sub, err := e.subs.Get(ctx, tenantID, inv.SubscriptionID)
	if err != nil {
		slog.Warn("subscription lookup failed during clock resolution, falling back to wall clock",
			"invoice_id", invoiceID, "subscription_id", inv.SubscriptionID, "error", err)
		return e.clock.Now(ctx), nil
	}
	return e.effectiveNow(ctx, sub), nil
}

// EffectiveNowForSubscription resolves the time anchor for an
// operator-triggered action on an existing subscription (Activate,
// ChangeItem, etc.). Loads the sub, then delegates to effectiveNow.
// Wall-clock fallback on error mirrors the other resolvers.
func (e *Engine) EffectiveNowForSubscription(ctx context.Context, tenantID, subscriptionID string) (time.Time, error) {
	sub, err := e.subs.Get(ctx, tenantID, subscriptionID)
	if err != nil {
		return e.clock.Now(ctx), fmt.Errorf("get subscription for clock resolution: %w", err)
	}
	return e.effectiveNow(ctx, sub), nil
}

// EffectiveNowForCustomer resolves the time anchor for an
// operator-triggered action where only the customer is known
// (subscription.Service.Create, one-off invoice composer). Reads
// customer.test_clock_id directly — subs inherit the pin from the
// owning customer at creation (ADR-027), so customer-level resolution
// is authoritative for any (yet-to-be-created or one-off) entity in
// that customer's orbit.
//
// Wall-clock fallback when the engine isn't wired with a customer
// reader (narrow unit tests) or when the customer / clock lookup
// fails. Same safety stance as the other resolvers: never block an
// operator action on a dangling pin.
func (e *Engine) EffectiveNowForCustomer(ctx context.Context, tenantID, customerID string) (time.Time, error) {
	if e.customers == nil {
		return e.clock.Now(ctx), nil
	}
	cust, err := e.customers.Get(ctx, tenantID, customerID)
	if err != nil {
		return e.clock.Now(ctx), fmt.Errorf("get customer for clock resolution: %w", err)
	}
	if cust.TestClockID == "" || e.testClocks == nil {
		return e.clock.Now(ctx), nil
	}
	tc, err := e.testClocks.Get(ctx, tenantID, cust.TestClockID)
	if err != nil {
		slog.Warn("test clock lookup failed during customer clock resolution, falling back to wall clock",
			"customer_id", customerID, "test_clock_id", cust.TestClockID, "error", err)
		return e.clock.Now(ctx), nil
	}
	return tc.FrozenTime, nil
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
	// TaxErrorCode is the typed classification of TaxPendingReason
	// (one of customer_data_invalid / jurisdiction_unsupported /
	// provider_outage / provider_auth / unknown). Populated by
	// tax.Classify on the deferral path; empty for ok results.
	TaxErrorCode string
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
	// Wiring is required, not optional. Tests that don't need tax
	// must wire NoneProvider explicitly (e.NewResolver(nil) +
	// e.SetTaxProviderResolver) — the previous silent zero-tax
	// fallback masked misconfiguration in production. Fail loudly
	// here so the caller sees the misconfiguration in the log
	// instead of getting a $0-tax invoice.
	if e.taxProviders == nil {
		return TaxApplication{}, fmt.Errorf("tax provider resolver not wired (call SetTaxProviderResolver)")
	}
	if e.settings == nil {
		return TaxApplication{}, fmt.Errorf("settings store not wired")
	}

	app := TaxApplication{
		SubtotalCents: subtotal,
		DiscountCents: discount,
		TaxStatus:     domain.InvoiceTaxOK,
	}

	// Pin tenant_id on ctx before resolving the provider. The
	// StripeTaxProvider's clientForCtx reads auth.TenantID(ctx) to
	// look up per-tenant Stripe credentials; background workers
	// (scheduler tick, test-clock catchup) build ctx with
	// WithLivemode but never set the tenant. Without this pin,
	// every tax call from a worker resolved a nil Stripe client
	// and surfaced as "no client configured for livemode=…" even
	// when credentials existed in the DB. HTTP-handler-driven
	// calls were unaffected because session/auth middleware pins
	// tenant_id on the request ctx.
	ctx = auth.WithTenantID(ctx, tenantID)

	// SettingsStore.Get synthesizes Velox defaults on miss
	// (tenant.DefaultSettings) — this Get returns a real error
	// only on a true DB failure, never on missing-row. Bootstrap
	// always creates the settings row, so missing-row is itself
	// rare.
	ts, err := e.settings.Get(ctx, tenantID)
	if err != nil {
		return TaxApplication{}, fmt.Errorf("load tenant settings: %w", err)
	}
	app.TaxName = ts.TaxName

	// Resolver is exhaustive: switch over ts.TaxProvider always
	// returns a non-nil Provider with nil error
	// (none / manual / stripe_tax → falls back to manual when
	// Stripe wiring isn't there). The previous err-or-nil branch
	// was dead code.
	provider, err := e.taxProviders.Resolve(ctx, ts)
	if err != nil {
		return TaxApplication{}, fmt.Errorf("resolve tax provider: %w", err)
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
		// Classify the provider error into a typed taxonomy so the
		// dashboard banner and webhook consumers can branch on cause
		// (customer-data fix vs provider-outage retry). The pending
		// reason is the cleaned upstream message (Stripe Tax JSON
		// envelope unwrapped) — preserved verbatim in tax_calculations
		// for diagnostic depth.
		app.TaxErrorCode = string(tax.Classify(err))
		app.TaxPendingReason = truncateReason(tax.CleanMessage(err.Error()), 500)
		// tax_deferred_at lands in simulated time on clock-pinned
		// invoices via ctx-bound effective-now. The clock-nil branch
		// covers narrow unit tests that construct an Engine without
		// wiring a Clock; production always wires Real().
		var deferredAt time.Time
		if e.clock != nil {
			deferredAt = e.clock.Now(ctx)
		} else {
			deferredAt = clock.Now(ctx)
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
		lineItems[i].TaxabilityReason = rl.TaxabilityReason
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
// RunCycleForClock processes due subs attached to ONE test clock. The
// per-sub period loop in billSubscription catches each sub up to the
// clock's frozen time in a single pass. Called by the test-clock
// catchup worker after MarkAdvancing — operator-driven (Advance
// click), never the wall-clock cron.
//
// Returns the count of invoices generated and any per-sub errors
// (non-fatal — failures on one sub don't stall the others). The
// outer loop ensures every due sub on the clock is processed even
// if more than batchSize are attached.
//
// ADR-028 disjoint-flow architecture: this is the ONLY path for
// clock-pinned billing. The wall-clock RunCycle explicitly excludes
// clock-pinned subs.
func (e *Engine) RunCycleForClock(ctx context.Context, tenantID, clockID string, batchSize int) (int, []error) {
	if batchSize <= 0 {
		batchSize = 50
	}
	ctx, span := telemetry.Tracer("billing").Start(ctx, "billing.RunCycleForClock",
		trace.WithAttributes(
			attribute.String("clock_id", clockID),
			attribute.String("tenant_id", tenantID),
			attribute.Int("batch_size", batchSize),
		),
	)
	defer span.End()

	generated := 0
	var errs []error

	// Outer loop only matters if a clock has more than batchSize
	// attached subs (rare). Each pass fetches a fresh batch with
	// SKIP LOCKED; subs whose catchup completes in the inner
	// per-sub period loop fall off the next pass.
	for {
		if err := ctx.Err(); err != nil {
			errs = append(errs, fmt.Errorf("clock catchup ctx done: %w", err))
			break
		}

		due, err := e.subs.GetDueBillingForClock(ctx, tenantID, clockID, batchSize)
		if err != nil {
			errs = append(errs, fmt.Errorf("fetch due-on-clock: %w", err))
			break
		}
		if len(due) == 0 {
			break
		}

		for _, sub := range due {
			n, err := e.billSubscription(ctx, sub)
			if err != nil {
				slog.Error("bill subscription failed (clock-catchup)",
					"subscription_id", sub.ID,
					"clock_id", clockID,
					"invoices_before_error", n,
					"error", err,
				)
				errs = append(errs, fmt.Errorf("subscription %s: %w", sub.ID, err))
			}
			generated += n
		}
	}

	span.SetAttributes(attribute.Int("generated", generated))
	slog.Info("clock catchup cycle complete",
		"clock_id", clockID,
		"generated", generated,
		"errors", len(errs),
	)
	return generated, errs
}

func (e *Engine) RunCycle(ctx context.Context, batchSize int) (int, []error) {
	if batchSize <= 0 {
		batchSize = 50
	}

	ctx, span := telemetry.Tracer("billing").Start(ctx, "billing.RunCycle",
		trace.WithAttributes(attribute.Int("batch_size", batchSize)),
	)
	defer span.End()

	due, err := e.subs.GetDueBilling(ctx, e.clock.Now(ctx), batchSize)
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
		n, err := e.billSubscription(ctx, sub)
		if err != nil {
			slog.Error("bill subscription failed",
				"subscription_id", sub.ID,
				"tenant_id", sub.TenantID,
				"invoices_before_error", n,
				"error", err,
			)
			errs = append(errs, fmt.Errorf("subscription %s: %w", sub.ID, err))
		}
		generated += n
	}

	slog.Info("billing cycle complete", "generated", generated, "errors", len(errs))
	return generated, errs
}

// maxPeriodsPerSubPerCall caps how many periods billSubscription
// will generate for a single sub in one call. The cap is a safety
// guard against a billOnePeriod bug that fails to advance
// next_billing_at — without it the period loop would spin forever.
//
// 10000 covers any realistic catch-up: 833 years of monthly billing,
// 27 years of daily. Test-clock advances beyond that need a chained
// retry, which is fine — the wall-clock 10-min CatchupTimeout is the
// outer ceiling.
const maxPeriodsPerSubPerCall = 10000

// billSubscription catches a subscription up to its effectiveNow by
// looping billOnePeriod until next_billing_at exceeds the clock the
// sub runs on. Production wall-clock subs typically need exactly one
// iteration (the cycle accumulates one period of debt before each
// scheduler tick). Test-clock catch-up after a multi-year advance
// runs N iterations in a single call — the operator clicks Advance
// once and the sub catches up fully, no chained retries.
//
// Returns the number of invoices generated (skipped/trial periods
// don't count) and any fatal error encountered. Partial progress
// is preserved on error: invoices generated before the error stay
// committed (each iteration is its own DB tx via billOnePeriod).
//
// The pacing env knob (VELOX_TEST_CLOCK_CATCHUP_DELAY_MS) is honoured
// between iterations so the manual restart-resilience smoke test
// keeps working — kill -9 mid-loop is still observable from the
// outside.
//
// ADR-028.
func (e *Engine) billSubscription(ctx context.Context, sub domain.Subscription) (int, error) {
	count := 0
	for i := 0; i < maxPeriodsPerSubPerCall; i++ {
		// Honour ctx deadline (CatchupTimeout from the worker, request
		// timeout from the scheduler). Returns the partial count + err.
		if err := ctx.Err(); err != nil {
			return count, fmt.Errorf("billing loop ctx done: %w", err)
		}

		// Refresh sub state. Required because billOnePeriod commits
		// per-period UpdateBillingCycle in its own tx, and we want
		// the next iteration to observe the post-advance fields.
		fresh, err := e.subs.Get(ctx, sub.TenantID, sub.ID)
		if err != nil {
			return count, fmt.Errorf("refresh subscription %s: %w", sub.ID, err)
		}
		sub = fresh

		// Caught-up check. The clock the sub lives on (test or wall)
		// determines "now"; sub.NextBillingAt strictly after that
		// means there's nothing more to bill in this call.
		now := e.effectiveNow(ctx, sub)
		if sub.NextBillingAt == nil || sub.NextBillingAt.After(now) {
			return count, nil
		}

		// Snapshot for the no-progress detector below.
		prevNextBilling := sub.NextBillingAt

		invoiced, err := e.billOnePeriod(ctx, sub)
		if err != nil {
			return count, err
		}
		if invoiced {
			count++
		}

		// No-progress guard: if billOnePeriod returned cleanly but
		// did not advance next_billing_at, the next iteration would
		// fire identically forever. Bail with a typed error so the
		// caller can mark the clock internal_failure with a useful
		// reason instead of looping until the per-sub cap.
		afterCheck, err := e.subs.Get(ctx, sub.TenantID, sub.ID)
		if err != nil {
			return count, fmt.Errorf("post-bill refresh %s: %w", sub.ID, err)
		}
		if afterCheck.NextBillingAt == nil || (prevNextBilling != nil && !afterCheck.NextBillingAt.After(*prevNextBilling)) {
			// Skipped sub (status non-active, no items, etc.) —
			// billOnePeriod returned cleanly without advancing.
			// That's the natural exit, not an error.
			return count, nil
		}

		// Optional pacing for the manual kill-mid-flight restart-
		// resilience smoke test (MANUAL_TEST FLOW TC2). Honours ctx
		// cancellation so kill -TERM mid-sleep wakes promptly.
		if delay := catchupDelayFromEnvBilling(); delay > 0 {
			select {
			case <-ctx.Done():
				return count, fmt.Errorf("billing loop ctx done during pacing: %w", ctx.Err())
			case <-time.After(delay):
			}
		}
	}
	return count, fmt.Errorf("subscription %s: per-sub safety cap %d hit — billOnePeriod did not converge", sub.ID, maxPeriodsPerSubPerCall)
}

// catchupDelayFromEnvBilling reads VELOX_TEST_CLOCK_CATCHUP_DELAY_MS
// for in-loop pacing. Mirrors testclock.catchupDelayFromEnv but
// duplicated here to avoid a billing→testclock import (would create
// a cycle). The semantics match — this is the same env, same
// behaviour, just read at the inner loop instead of the outer one.
func catchupDelayFromEnvBilling() time.Duration {
	v := os.Getenv("VELOX_TEST_CLOCK_CATCHUP_DELAY_MS")
	if v == "" {
		return 0
	}
	ms, err := strconv.Atoi(v)
	if err != nil || ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

// billOnePeriod generates an invoice for ONE billing period of a
// subscription, advancing next_billing_at by one cycle. Pre-Phase-2
// (ADR-028) this was the only billing primitive — RunCycle called it
// once per due sub per pass, and runCatchupLoop relied on an outer
// loop to compress N periods of catch-up into N passes. Phase 2
// keeps this function unchanged but wraps it in billSubscription
// which loops until the sub catches up to its effectiveNow.
//
// Returns (true, nil) if an invoice was created, (false, nil) if
// skipped (e.g. trial-active, status not active). The bool here
// means "invoice generated", NOT "next_billing_at advanced" — some
// skip paths (trial-active) advance the cycle without generating
// an invoice. The wrapper distinguishes via fresh sub-state reads.
func (e *Engine) billOnePeriod(ctx context.Context, sub domain.Subscription) (bool, error) {
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

	// Resolve "now" as this cycle's own close instant — sub.NextBillingAt
	// at entry, falling back to the clock's frozen_time / wall-clock only
	// when NextBillingAt is unset (unreachable from billSubscription's
	// caught-up check, but kept defensive).
	//
	// Anchoring on the cycle boundary keeps every per-cycle decision in
	// the time domain the cycle belongs to:
	//   - IssuedAt / CreatedAt / DueAt stamped at the cycle close,
	//     not at advance-end frozen_time. Multi-period catchup now
	//     produces one invoice per cycle with its own period-correct
	//     timestamp instead of all invoices sharing advance-end.
	//   - Pause auto-resume gate evaluates at cycle close: a pause
	//     scheduled to resume mid-cycle is honored — the May 1 cycle
	//     does NOT bill if pause resumes May 5, even when the advance
	//     lands at May 20. Industry parity (Stripe, Lago, Orb).
	//   - Trial-end activation stamps `activated_at` at the cycle's
	//     own boundary instead of advance-end.
	//   - Cancel-at-period-end / scheduled-change applications stamp
	//     their `canceled_at` / `applied_at` at the cycle boundary.
	//   - Tax calculation date matches the cycle, so the tax rate
	//     applicable at the cycle close (not at advance-end) is used.
	//   - MarkPaid for zero-amount auto-paid invoices stamps PaidAt at
	//     the cycle close, aligning with IssuedAt.
	//
	// In the cron (wall-clock) path NextBillingAt ≈ time.Now() within
	// one scheduler tick, so the change is neutral — invoice timestamps
	// land exactly on the period boundary instead of "few minutes after,"
	// which is the more defensible cosmetic anyway.
	now := e.effectiveNow(ctx, sub)
	if sub.NextBillingAt != nil {
		now = *sub.NextBillingAt
	}

	// Pause auto-resume is no longer evaluated here — it now runs as a
	// dedicated phase BEFORE this loop (Scheduler.pauseResumer for
	// wall-clock, testclock orchestrator Phase 0.7 for clock-pinned).
	// That phase clears pause_collection at resumes_at directly,
	// matching Stripe-parity "resume AT resumes_at" semantics; by the
	// time we reach this code the pause has either already been
	// cleared or it's still genuinely in force. The old in-cycle gate
	// silently leaked any sub whose next_billing_at was further out
	// than resumes_at — first surfaced by a test-clock advance past
	// resumes_at on a sub whose cycle wasn't due. See ADR-038.

	// If any item has a scheduled plan change whose effective_at falls
	// within (or at) the cycle being processed, apply them all BEFORE
	// reading plans so the new cycle bills on the new plans.
	//
	// Gate is CurrentBillingPeriodEnd, NOT wall-clock now. Stripe-Billing
	// parity: a change scheduled for "next cycle" must not apply
	// retroactively to a late-billed earlier cycle just because the
	// engine ran late. Pre-fix the comparison was `effective_at <= now`,
	// which fired for any cycle as long as the engine was running after
	// the effective date — visible bug on test-clock catchup spanning
	// multiple periods (engine processes each period sequentially with
	// frozen_time as `now`, so a mid-year scheduled change retroactively
	// applied to ALL prior periods).
	//
	// Falls back to `now` when CurrentBillingPeriodEnd is unset (rare;
	// shouldn't happen for a finalized cycle, but the fallback preserves
	// pre-fix behavior in that path).
	gate := now
	if sub.CurrentBillingPeriodEnd != nil {
		gate = *sub.CurrentBillingPeriodEnd
	}
	anyDue := false
	for _, it := range sub.Items {
		if it.PendingPlanID != "" && it.PendingPlanEffectiveAt != nil && !it.PendingPlanEffectiveAt.After(gate) {
			anyDue = true
			break
		}
	}
	if anyDue {
		// Snapshot the item IDs whose pending change is due BEFORE the
		// atomic swap — ApplyDuePendingItemPlansAtomic returns the full
		// refreshed item set (due + not-due) with pending_plan_id
		// cleared, so we can't tell post-hoc which rows actually
		// changed. This pre-swap list is what we fire
		// subscription.pending_change.applied events for. The outgoing
		// plan itself is captured in subscription_item_changes by the
		// DB trigger (migration 0029) — the segment-aware base-fee
		// loop further down reads that history rather than carrying a
		// snapshot map through this function.
		dueItems := make([]domain.SubscriptionItem, 0)
		for _, it := range sub.Items {
			if it.PendingPlanID != "" && it.PendingPlanEffectiveAt != nil && !it.PendingPlanEffectiveAt.After(gate) {
				dueItems = append(dueItems, it)
			}
		}

		// SQL filter inside ApplyDuePendingItemPlansAtomic uses the
		// gate value to select rows: WHERE pending_plan_effective_at
		// <= $gate. Same semantics as the Go-side check above so
		// in-memory and DB views agree.
		applied, err := e.subs.ApplyDuePendingItemPlansAtomic(ctx, sub.TenantID, sub.ID, gate)
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
			// Trial-active cycle advance: honors billing_time so calendar
			// subs with an extended-past-period trial stay calendar-aligned.
			// Interval is hardcoded monthly here (plans not yet fetched);
			// trial-extended-past-yearly-cycle is an edge case that the
			// pre-existing hardcoded `monthly` already approximated.
			nextBilling := domain.NextBillingPeriodEnd(periodEnd, sub.BillingTime, domain.BillingMonthly, e.tenantLocation(ctx, sub.TenantID))
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
			// ADR-031 trial-end coverage: BillOnCreate fires for the
			// just-opened paid period [current_period_start,
			// current_period_end] so in_advance items don't slip
			// through. Without this, billOnePeriod's normal cycle
			// billing below charges in_advance items for the NEXT
			// period (periodEnd → nextPeriodEnd) and the trial-end
			// stub goes unbilled — revenue leak specific to in_advance
			// + trial. In_arrears items are unaffected (the cycle
			// billing below charges them for the just-closed period
			// normally). No-op when no item is in_advance.
			//
			// Idempotent via the (sub_id, period_start, period_end)
			// UNIQUE constraint — repeated catchup ticks don't
			// double-bill. Best-effort: failures log but don't abort
			// the cycle; the next Advance retries.
			if _, advErr := e.BillOnCreate(ctx, sub); advErr != nil {
				slog.Warn("trial-end first-invoice failed; in_advance base fee will be deferred",
					"subscription_id", sub.ID,
					"tenant_id", sub.TenantID,
					"error", advErr,
				)
			}
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
	// billing period. Use math.Round, not int truncation: a sub created
	// at 14:00 was previously billed for 30/31 days because hour-precise
	// duration / 24 truncated to 30. With period boundaries snapped to
	// 00:00 in tenant TZ (subscription.Service), elapsed hours land on
	// (or within ±1h of) a whole-day multiple — DST transitions
	// account for the ±1h tolerance, which Round absorbs.
	periodDays := roundDays(periodEnd.Sub(periodStart))

	// At cycle close, check whether the scheduled cancel will fire at
	// this boundary. If yes, in_advance base lines for the UPCOMING
	// period MUST be skipped — the sub is about to terminate, the
	// customer won't consume the upcoming period, and emitting the
	// line would overcharge by one full prepayment. The cycle-close
	// invoice still bills usage from the just-elapsed period
	// (in-arrears for usage is always correct) and any in_arrears
	// base for the just-elapsed period. After this invoice fires,
	// advanceCycleOrCancel below fires the scheduled cancel.
	// (audit finding flagged during the 2026-05-18 cancel-flow walk
	// through; same bug class would re-appear if hard-pause is ever
	// re-added — paused subs at pause-activation should likewise
	// skip the next-period in_advance base line.)
	terminalCycleClose := shouldFireScheduledCancel(sub, periodEnd, now)

	// Pull the per-item change log for this period — drives segment-
	// aware base-fee billing (Lago / Chargebee / Orb shape). Each row
	// demarcates a [pre-change, post-change] boundary, so the in_arrears
	// base for a sub that had a mid-period plan or quantity change is
	// emitted as one line per segment at the segment's own plan + qty
	// rate × duration fraction. No mid-period changes → segments collapse
	// to a single full-period line (same as pre-segment-aware behavior).
	//
	// Failure here propagates to the per-sub error handler. Pre-fix
	// (2026-05-30 design-debt audit): read failure silently fell back
	// to single-line billing, mis-billing any sub that had a mid-period
	// plan or quantity change with no signal to the operator. Per
	// feedback_no_silent_fallbacks, fail loud — the engine already
	// continues to the next sub when this one errors.
	itemChanges, err := e.subs.ListItemChangesInPeriod(ctx, sub.TenantID, sub.ID, periodStart, periodEnd)
	if err != nil {
		return false, fmt.Errorf("list item changes: %w", err)
	}
	changesByItem := map[string][]domain.SubscriptionItemChange{}
	for _, c := range itemChanges {
		changesByItem[c.SubscriptionItemID] = append(changesByItem[c.SubscriptionItemID], c)
	}
	// Hydrate any plans referenced only in the change log (items removed
	// mid-period, or pre-swap plans not present on current items). Plans
	// already loaded for current items are skipped.
	//
	// Failure here propagates as above. Pre-fix the segment under a
	// failed plan lookup would be silently dropped from the invoice,
	// undercharging the customer; per feedback_no_silent_fallbacks the
	// engine fails the sub's cycle rather than guess.
	for _, c := range itemChanges {
		for _, pid := range []string{c.FromPlanID, c.ToPlanID} {
			if pid == "" {
				continue
			}
			if _, ok := plans[pid]; ok {
				continue
			}
			pl, err := e.pricing.GetPlan(ctx, sub.TenantID, pid)
			if err != nil {
				return false, fmt.Errorf("get segment plan %s: %w", pid, err)
			}
			plans[pid] = pl
		}
	}
	// Augment meterAggs with meters from hydrated change-log plans —
	// segment-aware usage needs every meter that was active at ANY
	// point during the period, not just meters on current items'
	// plans. Without this, a meter that existed on plan_A (now
	// swapped out) wouldn't be aggregated in the [seg.start, seg.end)
	// window where plan_A was still active.
	for _, pl := range plans {
		for _, meterID := range pl.MeterIDs {
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

	// Base fee line item — segment-aware for in_arrears, single-line
	// for in_advance (which bills the upcoming period at the post-swap
	// plan; segments don't apply because the previous period was
	// already pre-paid).
	//
	// ADR-031 + segment-aware (Lago/Chargebee/Orb):
	//   - in_arrears: one line per segment within [periodStart, periodEnd].
	//     Multi-segment when the customer changed plan or quantity
	//     mid-period; single-segment otherwise. Each segment bills at
	//     its own rate × (segment_days / cycle_days).
	//   - in_advance: one line for the UPCOMING period at the current
	//     (post-swap) plan. The just-elapsed period's base was paid
	//     upfront at the previous boundary.
	itemsByID := map[string]domain.SubscriptionItem{}
	for _, it := range sub.Items {
		itemsByID[it.ID] = it
	}

	// Pass 1: items currently on the sub.
	for _, it := range sub.Items {
		plan := plans[it.PlanID]
		isAdvance := plan.BaseBillTiming == domain.BillInAdvance

		if isAdvance {
			if terminalCycleClose {
				// Sub cancels at this boundary — don't pre-pay a period
				// that won't be used. Usage on this invoice still
				// captures the just-elapsed consumption.
				continue
			}
			if plan.BaseAmountCents <= 0 {
				continue
			}
			baseStart := periodEnd
			// In_advance base-fee line item label MUST match what the
			// engine will use as the next billing period — uses the
			// billing_time-aware helper so calendar+monthly subs show
			// the calendar-aligned next period on the line item, not
			// the day-of-month-preserved drifted period.
			baseEnd := domain.NextBillingPeriodEnd(periodEnd, sub.BillingTime, plan.BillingInterval, e.tenantLocation(ctx, sub.TenantID))
			baseFee := plan.BaseAmountCents * it.Quantity
			description := fmt.Sprintf("%s - base fee (qty %d)", plan.Name, it.Quantity)
			// Prorate when the upcoming period is shorter than a full
			// plan cycle — e.g. the calendar-snap stub produced after
			// a yearly→monthly plan-change cycle close (new period =
			// `(yearly_end, first-of-next-month)`, 1-30 days). Pre-fix
			// this billed the full monthly base for a 7-day stub.
			// Same shape as BillOnCreate's proration and
			// emitBaseSegmentLine's segDays/fullCycleDays gate.
			advanceDays := roundDays(baseEnd.Sub(baseStart))
			fullCycleDays := roundDays(advanceBillingPeriod(baseStart, plan.BillingInterval).Sub(baseStart))
			if advanceDays > 0 && fullCycleDays > 0 && advanceDays < fullCycleDays {
				baseFee = money.RoundHalfToEven(plan.BaseAmountCents*it.Quantity*int64(advanceDays), int64(fullCycleDays))
				description = fmt.Sprintf("%s - base fee (qty %d, prorated %d/%d days)", plan.Name, it.Quantity, advanceDays, fullCycleDays)
			}
			unitAmount := plan.BaseAmountCents
			if it.Quantity > 0 {
				unitAmount = money.RoundHalfToEven(baseFee, it.Quantity)
			}
			baseStartCopy := baseStart
			baseEndCopy := baseEnd
			lineItems = append(lineItems, domain.InvoiceLineItem{
				LineType:           domain.LineTypeBaseFee,
				Description:        description,
				Quantity:           it.Quantity,
				UnitAmountCents:    unitAmount,
				AmountCents:        baseFee,
				TotalAmountCents:   baseFee,
				Currency:           invoiceCurrency,
				BillingPeriodStart: &baseStartCopy,
				BillingPeriodEnd:   &baseEndCopy,
			})
			subtotal += baseFee
			continue
		}

		// in_arrears: emit per-segment lines.
		itemForSeg := it
		segments := itemBaseSegments(&itemForSeg, changesByItem[it.ID], periodStart, periodEnd)
		for _, seg := range segments {
			segPlan, ok := plans[seg.planID]
			if !ok || segPlan.BaseAmountCents <= 0 {
				continue
			}
			// Skip segments whose plan was in_advance — that portion of
			// the period was already prepaid (BillOnCreate / prior cycle
			// close) and emitting a segment line here would double-bill
			// it. Matches the Pass 2 guard for removed in_advance items
			// below. Lets cross-cadence plan-swaps (in_advance OLD →
			// in_arrears NEW) bill correctly: the OLD prepaid segment
			// is skipped, the NEW in_arrears segment bills its consumed
			// portion at the NEW rate.
			if segPlan.BaseBillTiming == domain.BillInAdvance {
				continue
			}
			emitBaseSegmentLine(seg, segPlan, periodStart, periodDays, invoiceCurrency, &lineItems, &subtotal)
		}
	}

	// Pass 2: items removed mid-period (in the change log but not on
	// sub.Items now). Bill the pre-remove segments at their own rates.
	// in_arrears only — in_advance items removed mid-period already
	// paid upfront for the period; refund flows through the cancel-
	// proration / removed-item credit path (not this loop).
	for itemID, changes := range changesByItem {
		if _, stillPresent := itemsByID[itemID]; stillPresent {
			continue
		}
		segments := itemBaseSegments(nil, changes, periodStart, periodEnd)
		for _, seg := range segments {
			segPlan, ok := plans[seg.planID]
			if !ok || segPlan.BaseAmountCents <= 0 {
				continue
			}
			// Removed-item segments are always in_arrears-style billing
			// (period consumed before remove). in_advance items would
			// have been refunded via the cancel-proration credit path,
			// not billed here.
			if segPlan.BaseBillTiming == domain.BillInAdvance {
				continue
			}
			emitBaseSegmentLine(seg, segPlan, periodStart, periodDays, invoiceCurrency, &lineItems, &subtotal)
		}
	}

	// Segment-aware usage billing (Orb shape): each meter is billed
	// once per disjoint [start, end) range during which it was
	// active on the sub. For subs with no mid-period changes, every
	// meter has exactly one full-period interval — output collapses
	// to the pre-segment-aware single-line behavior. For subs where
	// a mid-period plan change adds/removes meters, each meter
	// bills only for the time it was actually on the sub.
	//
	// Cap math is preserved: the period's totalUsage (pre-cap) and
	// the capScale derived from it apply uniformly across all
	// (meter, interval) pairs.
	capScale := decimal.New(1, 0)
	if sub.UsageCapUnits != nil && *sub.UsageCapUnits > 0 && sub.OverageAction == "block" {
		// usageTotals here is already post-cap (the existing block
		// above scaled it in place). Recover the scale by comparing
		// to a re-aggregated full-period total — but simpler: the
		// cap-scale block above sets a stable factor we can re-derive
		// from sub.UsageCapUnits + sum of post-cap totals.
		postCapTotal := decimal.Zero
		for _, qty := range usageTotals {
			postCapTotal = postCapTotal.Add(qty)
		}
		capDec := decimal.NewFromInt(*sub.UsageCapUnits)
		if postCapTotal.Equal(capDec) && !postCapTotal.IsZero() {
			// Cap actually fired (post-cap total == cap). Compute the
			// pre-cap total by re-aggregating, then derive scale.
			preCapTotals, err := e.usage.AggregateForBillingPeriodByAgg(ctx, sub.TenantID, sub.CustomerID, meterAggs, periodStart, periodEnd)
			if err == nil {
				preCapTotal := decimal.Zero
				for _, qty := range preCapTotals {
					preCapTotal = preCapTotal.Add(qty)
				}
				if preCapTotal.GreaterThan(capDec) && !preCapTotal.IsZero() {
					capScale = capDec.Div(preCapTotal)
				}
			}
		}
	}

	// Build (meter, intervals) map from item segments + removed-item
	// segments. Each item × segment contributes [seg.start, seg.end]
	// to every meter in its segment's plan.
	meterIntervals := map[string][]usageInterval{}
	addMeterInterval := func(mid string, start, end time.Time) {
		meterIntervals[mid] = append(meterIntervals[mid], usageInterval{start, end})
	}
	for _, it := range sub.Items {
		itemForSeg := it
		segments := itemBaseSegments(&itemForSeg, changesByItem[it.ID], periodStart, periodEnd)
		if len(segments) == 0 {
			// Item present at period_end but no segments (e.g. zero-duration
			// boundary swap collapsed by the helper). Treat the post-change
			// plan's meters as active for the full period — defensive
			// fallback that preserves pre-segment-aware behavior.
			for _, mid := range plans[it.PlanID].MeterIDs {
				addMeterInterval(mid, periodStart, periodEnd)
			}
			continue
		}
		for _, seg := range segments {
			segPlan, ok := plans[seg.planID]
			if !ok {
				continue
			}
			for _, mid := range segPlan.MeterIDs {
				addMeterInterval(mid, seg.start, seg.end)
			}
		}
	}
	for itemID, changes := range changesByItem {
		if _, stillPresent := itemsByID[itemID]; stillPresent {
			continue
		}
		segments := itemBaseSegments(nil, changes, periodStart, periodEnd)
		for _, seg := range segments {
			segPlan, ok := plans[seg.planID]
			if !ok {
				continue
			}
			for _, mid := range segPlan.MeterIDs {
				addMeterInterval(mid, seg.start, seg.end)
			}
		}
	}

	// Cache per-interval aggregation so a meter active across N
	// non-overlapping intervals incurs at most N queries (and 1 query
	// when N == 1 and the interval equals the full period — we use
	// the precomputed usageTotals in that case).
	intervalAggCache := map[string]map[string]decimal.Decimal{}
	intervalKey := func(iv usageInterval) string {
		return iv.start.UTC().Format(time.RFC3339Nano) + "|" + iv.end.UTC().Format(time.RFC3339Nano)
	}

	for meterID, ivs := range meterIntervals {
		merged := mergeUsageIntervals(ivs)
		for _, iv := range merged {
			var quantity decimal.Decimal
			fullPeriod := iv.start.Equal(periodStart) && iv.end.Equal(periodEnd)
			if fullPeriod {
				quantity = usageTotals[meterID]
			} else {
				key := intervalKey(iv)
				totals, cached := intervalAggCache[key]
				if !cached {
					t, err := e.usage.AggregateForBillingPeriodByAgg(ctx, sub.TenantID, sub.CustomerID, meterAggs, iv.start, iv.end)
					if err != nil {
						return false, fmt.Errorf("aggregate usage for segment [%v, %v): %w", iv.start, iv.end, err)
					}
					totals = t
					intervalAggCache[key] = t
				}
				quantity = totals[meterID]
				// Apply cap scale to sub-period quantities so the
				// per-interval sum matches the cap-scaled full-period
				// total. Cap doesn't apply if capScale == 1.
				if !capScale.Equal(decimal.New(1, 0)) {
					quantity = quantity.Mul(capScale)
				}
			}
			if quantity.IsZero() {
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
			unitAmount := decimal.NewFromInt(amount).Div(quantity).RoundBank(0).IntPart()

			ivStart := iv.start
			ivEnd := iv.end
			lineItems = append(lineItems, domain.InvoiceLineItem{
				LineType:            domain.LineTypeUsage,
				MeterID:             meterID,
				Description:         fmt.Sprintf("%s (%s)", meter.Name, meter.Unit),
				Quantity:            quantity.IntPart(),
				UnitAmountCents:     unitAmount,
				AmountCents:         amount,
				TotalAmountCents:    amount,
				Currency:            invoiceCurrency,
				PricingMode:         string(rule.Mode),
				RatingRuleVersionID: rule.ID,
				BillingPeriodStart:  &ivStart,
				BillingPeriodEnd:    &ivEnd,
			})
			subtotal += amount
		}
	}

	// Skip empty cycle-close invoices — matches BillOnCreate's and
	// BillFinalOnImmediateCancel's existing zero-subtotal guards and
	// the Stripe / Lago / Chargebee / Orb convention of NOT emitting
	// a $0 invoice when there's literally nothing to bill. Common
	// triggers in practice:
	//
	//   - in_advance plan + scheduled cancel-at-period-end (PR-9):
	//     upcoming-period base line is skipped because the customer
	//     won't use the period; if there's no usage to bill for the
	//     just-elapsed period either, the result is zero line items.
	//   - in_arrears item removed mid-period: pre-remove segment
	//     emits, but post-remove there's no current item, so the
	//     next cycle's invoice may have zero base lines.
	//   - Pure-trial period closure: nothing to bill yet.
	//
	// Still advance the cycle so the period anchor moves forward —
	// the absence of an invoice doesn't mean the period didn't pass.
	// No invoice number is consumed (NextInvoiceNumber is monotonic;
	// burning one on a phantom invoice creates audit gaps).
	if subtotal == 0 && len(lineItems) == 0 {
		nextPeriodStart := periodEnd
		nextPeriodEnd := domain.NextBillingPeriodEnd(periodEnd, sub.BillingTime, plans[sub.Items[0].PlanID].BillingInterval, e.tenantLocation(ctx, sub.TenantID))
		if err := e.advanceCycleOrCancel(ctx, sub, periodEnd, nextPeriodStart, nextPeriodEnd, now); err != nil {
			return false, fmt.Errorf("advance billing cycle (no-op invoice): %w", err)
		}
		slog.Info("cycle close skipped — no billable lines",
			"subscription_id", sub.ID,
			"period_start", periodStart,
			"period_end", periodEnd,
		)
		return false, nil
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

	// Coupons removed 2026-05-29 (Phase A1). Discount stays at zero;
	// AI-native discount intent flows through the credit ledger.
	var discountCents int64
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

	// Single source of truth for the draft/finalized decision —
	// shared across all four invoice-emitting paths (cycle close,
	// BillOnCreate, BillFinalOnImmediateCancel, handleItemProration).
	// Encodes: tax pending → draft; pause_collection set → draft;
	// otherwise finalized. See domain.InvoiceFinalizationStatus.
	invStatus := domain.InvoiceFinalizationStatus(taxApp.TaxStatus, sub.PauseCollection)
	collectionPaused := sub.PauseCollection != nil

	// ADR-031: when ANY plan on the sub is in_advance, the cycle
	// invoice's header period shifts to the UPCOMING period. The base
	// for the upcoming period dominates the invoice's intent — usage
	// from the just-elapsed period rides along on dedicated line
	// items (those keep their own elapsed-period stamps set above).
	// This shift is what lets the day-1 (subscription_create) invoice
	// and the cycle-close (subscription_cycle) invoice coexist under
	// the (sub_id, period_start, period_end) UNIQUE constraint — they
	// land on different periods.
	invoicePeriodStart, invoicePeriodEnd := periodStart, periodEnd
	for _, it := range sub.Items {
		if plans[it.PlanID].BaseBillTiming == domain.BillInAdvance {
			invoicePeriodStart = periodEnd
			// Invoice header for an in_advance sub covers the upcoming
			// period — must match what the sub's next current_period_*
			// will be set to (computed via NextBillingPeriodEnd below
			// at cycle close). Diverging here would leave the invoice
			// header period and the sub's tracked period out of sync.
			invoicePeriodEnd = domain.NextBillingPeriodEnd(periodEnd, sub.BillingTime, plans[it.PlanID].BillingInterval, e.tenantLocation(ctx, sub.TenantID))
			break
		}
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
		TaxErrorCode:       taxApp.TaxErrorCode,
		TotalAmountCents:   totalWithTax,
		AmountDueCents:     totalWithTax,
		BillingPeriodStart: invoicePeriodStart,
		BillingPeriodEnd:   invoicePeriodEnd,
		IssuedAt:           &now,
		DueAt:              &dueAt,
		// CreatedAt = clock.Now() so test-clock-driven invoices land
		// created_at on simulation time (matching issued_at). Pre-fix
		// the store fell back to time.Now() (wall-clock) and the
		// activity timeline showed split-brain timestamps — created
		// on real time, issued on test-clock time.
		CreatedAt:          now,
		NetPaymentTermDays: netDays,
		BillingReason:      domain.BillingReasonSubscriptionCycle,
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
			nextPeriodEnd := domain.NextBillingPeriodEnd(periodEnd, sub.BillingTime, plans[sub.Items[0].PlanID].BillingInterval, e.tenantLocation(ctx, sub.TenantID))
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

	// Apply customer credits before charging. ApplyToInvoice is atomic:
	// it both debits the credit ledger AND reduces the invoice's amount_due_cents
	// in a single transaction. A failure leaves both unchanged — no dual-write
	// hole where credits are consumed but the invoice still shows the pre-credit
	// amount due (which would double-bill the customer via Stripe).
	//
	// Skip during pause_collection — credits should not be consumed against a
	// draft invoice that may never be finalized; the credit will apply when
	// collection resumes and the invoice transitions out of draft.
	//
	// Pre-fix bug (caught 2026-05-30 design-debt audit): a DB blip in
	// ApplyToInvoiceAt would log + continue, then the auto-charge block
	// below would charge Stripe the FULL pre-credit total — silently
	// overcharging the customer by the credit balance amount. Fix:
	// flag the invoice for scheduler retry and skip the downstream
	// MarkPaid + auto-charge blocks so the next RetryPendingCharges
	// tick can re-apply credits atomically with the charge.
	creditApplyOK := true
	if e.credits != nil && totalWithTax > 0 && !collectionPaused {
		credited, err := e.credits.ApplyToInvoiceAt(ctx, sub.TenantID, sub.CustomerID, inv.ID, totalWithTax, now, inv.InvoiceNumber)
		if err != nil {
			slog.Warn("failed to apply credits — flagging for retry; auto-charge skipped to avoid overcharge",
				"invoice_id", inv.ID, "error", err)
			creditApplyOK = false
			_ = e.invoices.SetAutoChargePending(ctx, sub.TenantID, inv.ID, true)
		} else if credited > 0 {
			slog.Info("credits applied to invoice",
				"invoice_id", inv.ID,
				"credited_cents", credited,
			)
		}
	}

	// If credits covered 100%, mark as paid immediately (no Stripe
	// charge needed) — BUT only when the invoice was finalized at
	// create time (i.e., tax_status=ok and pause_collection unset, per
	// InvoiceFinalizationStatus). For draft invoices (tax pending or
	// pause-collection set), skip the MarkPaid call: leave the
	// invoice draft with credits already applied. Tax retry's
	// auto-finalize chain will land the draft → finalized + auto-pay
	// when tax resolves; pause-collection's resume path will do the
	// same when collection unpauses.
	//
	// Pre-fix bug (caught 2026-05-22): this block called MarkPaid
	// regardless of status, transitioning a tax-pending draft directly
	// to paid. The customer was charged subtotal-only (tax_amount=0)
	// and tax retry blocked forever (retry requires status='draft',
	// but status was 'paid'). Customer DEMO-000906 demonstrated.
	if creditApplyOK && totalWithTax > 0 && inv.Status == domain.InvoiceFinalized {
		updatedInv, err := e.invoices.GetInvoice(ctx, sub.TenantID, inv.ID)
		if err == nil && updatedInv.AmountDueCents <= 0 {
			// Reuse the sub-scoped `now` so fully-credit-paid invoices on a
			// test clock get paid_at from the frozen timeline, not wall-clock.
			if _, err := e.invoices.MarkPaid(ctx, sub.TenantID, inv.ID, "", now); err != nil {
				slog.Warn("failed to mark fully-credited invoice as paid", "invoice_id", inv.ID, "error", err)
			} else {
				slog.Info("invoice fully covered by credits, marked as paid", "invoice_id", inv.ID)
				// Still advance the billing cycle (billing_time-aware
				// so calendar subs auto-realign on credit-paid cycles too).
				nextPeriodStart := periodEnd
				nextPeriodEnd := domain.NextBillingPeriodEnd(periodEnd, sub.BillingTime, plans[sub.Items[0].PlanID].BillingInterval, e.tenantLocation(ctx, sub.TenantID))
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
	if creditApplyOK && e.charger != nil && e.paymentSetups != nil && inv.AmountDueCents > 0 && !collectionPaused {
		stripeCusID, hasDefaultPM, psErr := e.paymentSetups.ResolveForCharge(ctx, sub.TenantID, sub.CustomerID)
		pmReady := psErr == nil && hasDefaultPM && stripeCusID != ""

		if pmReady {
			chargeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			chargeInv, err := e.invoices.GetInvoice(chargeCtx, sub.TenantID, inv.ID)
			if err == nil && chargeInv.AmountDueCents > 0 {
				if _, err := e.charger.ChargeInvoice(chargeCtx, sub.TenantID, chargeInv, stripeCusID); err != nil {
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
		} else {
			// No PM ready: queue for the scheduler-retry path AND
			// notify the customer. RetryPendingCharges checks PM on
			// each tick — skips when still missing, charges
			// immediately when the customer attaches one (Chargebee's
			// "Collect Invoice on Card Update"). The notifier sends
			// the same "Action required: payment method needed" email
			// Stripe sends on charge failures, so the customer learns
			// about the gap from email — not from the invoice silently
			// going overdue weeks later.
			slog.Info("no payment method at finalize, queuing for scheduler retry + notifying customer",
				"invoice_id", inv.ID,
				"customer_id", sub.CustomerID,
			)
			_ = e.invoices.SetAutoChargePending(ctx, sub.TenantID, inv.ID, true)
			if e.noPMNotifier != nil {
				// Reload the invoice so the notifier sees the just-
				// finalized state (invoice number, totals).
				if notifyInv, err := e.invoices.GetInvoice(ctx, sub.TenantID, inv.ID); err == nil {
					if err := e.noPMNotifier.NotifyNoPaymentMethod(ctx, sub.TenantID, notifyInv); err != nil {
						slog.Warn("no-payment-method notification failed",
							"invoice_id", inv.ID,
							"error", err,
						)
					}
				}
			}
		}
	}

	// Advance billing cycle (or fire scheduled cancel if due). Uses
	// domain.NextBillingPeriodEnd (NOT the legacy interval-only
	// advanceBillingPeriod) so calendar-billing subs whose anchor day
	// drifted from a prior plan-interval change auto-re-align to the
	// next calendar boundary instead of carrying the drifted day
	// forward forever.
	nextPeriodStart := periodEnd
	nextPeriodEnd := domain.NextBillingPeriodEnd(periodEnd, sub.BillingTime, plans[sub.Items[0].PlanID].BillingInterval, e.tenantLocation(ctx, sub.TenantID))

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

// BillOnCreate emits the day-1 invoice for an in_advance subscription
// (ADR-031). Called by subscription.Service.Create immediately after
// the sub is persisted. Returns a zero Invoice + nil when the sub
// has no in_advance items — the cycle path will pick the sub up at
// period close as usual.
//
// Scope of the invoice:
//   - Period: [CurrentBillingPeriodStart, CurrentBillingPeriodEnd].
//     Mid-period creation prorates the base by remaining days
//     (matches the cycle path).
//   - Line items: base fee for each in_advance item. Arrears items
//     contribute nothing — their base waits for cycle close.
//   - Usage: zero. No events have happened yet at t=0.
//   - billing_reason = subscription_create (distinguishes from the
//     subscription_cycle invoice that lands at period close with
//     usage from this same elapsed period).
//
// Idempotent via the standard (subscription_id, period_start,
// period_end) UNIQUE constraint: if BillOnCreate runs twice (e.g. a
// retry after a transient failure), the second call gets
// ErrAlreadyExists and returns the existing invoice's nil state — no
// duplicate. Auto-charge is best-effort: PM ready → synchronous
// charge with 30s timeout; no PM → queue auto_charge_pending and
// fire the no-PM notifier email (same path as the cycle invoice).
func (e *Engine) BillOnCreate(ctx context.Context, sub domain.Subscription) (domain.Invoice, error) {
	ctx, span := telemetry.Tracer("billing").Start(ctx, "billing.BillOnCreate",
		trace.WithAttributes(
			attribute.String("subscription_id", sub.ID),
			attribute.String("tenant_id", sub.TenantID),
			attribute.String("customer_id", sub.CustomerID),
		),
	)
	defer span.End()

	if sub.Status != domain.SubscriptionActive {
		return domain.Invoice{}, nil
	}
	if sub.CurrentBillingPeriodStart == nil || sub.CurrentBillingPeriodEnd == nil {
		return domain.Invoice{}, fmt.Errorf("subscription has no billing period set")
	}

	now := e.effectiveNow(ctx, sub)
	periodStart := *sub.CurrentBillingPeriodStart
	periodEnd := *sub.CurrentBillingPeriodEnd

	// Resolve plans for every item — needed to filter to in_advance
	// items and to read base fee + currency.
	plans := make(map[string]domain.Plan, len(sub.Items))
	for _, it := range sub.Items {
		if _, ok := plans[it.PlanID]; ok {
			continue
		}
		pl, err := e.pricing.GetPlan(ctx, sub.TenantID, it.PlanID)
		if err != nil {
			return domain.Invoice{}, fmt.Errorf("get plan %s: %w", it.PlanID, err)
		}
		plans[it.PlanID] = pl
	}

	// Filter to in_advance items only. If none, no day-1 invoice;
	// the cycle path will handle this sub naturally at period close.
	advanceItems := make([]domain.SubscriptionItem, 0, len(sub.Items))
	for _, it := range sub.Items {
		if plans[it.PlanID].BaseBillTiming == domain.BillInAdvance {
			advanceItems = append(advanceItems, it)
		}
	}
	if len(advanceItems) == 0 {
		return domain.Invoice{}, nil
	}

	// Invoice currency: customer billing profile > tenant settings >
	// first in_advance item's plan currency > "usd". Same precedence
	// as the cycle path.
	invoiceCurrency := plans[advanceItems[0].PlanID].Currency
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

	// Build base-fee line items for in_advance items, with mid-period
	// proration (identical math to billOnePeriod's base loop).
	lineItems := make([]domain.InvoiceLineItem, 0, len(advanceItems))
	subtotal := int64(0)
	periodDays := roundDays(periodEnd.Sub(periodStart))
	for _, it := range advanceItems {
		plan := plans[it.PlanID]
		if plan.BaseAmountCents <= 0 {
			continue
		}
		baseFee := plan.BaseAmountCents * it.Quantity
		description := fmt.Sprintf("%s - base fee (qty %d)", plan.Name, it.Quantity)

		fullCycleDays := roundDays(advanceBillingPeriod(periodStart, plan.BillingInterval).Sub(periodStart))
		if periodDays > 0 && fullCycleDays > 0 && periodDays < fullCycleDays {
			baseFee = money.RoundHalfToEven(plan.BaseAmountCents*it.Quantity*int64(periodDays), int64(fullCycleDays))
			description = fmt.Sprintf("%s - base fee (qty %d, prorated %d/%d days)", plan.Name, it.Quantity, periodDays, fullCycleDays)
		}

		unitAmount := plan.BaseAmountCents
		if it.Quantity > 0 {
			unitAmount = money.RoundHalfToEven(baseFee, it.Quantity)
		}

		baseStartCopy := periodStart
		baseEndCopy := periodEnd
		lineItems = append(lineItems, domain.InvoiceLineItem{
			LineType:           domain.LineTypeBaseFee,
			Description:        description,
			Quantity:           it.Quantity,
			UnitAmountCents:    unitAmount,
			AmountCents:        baseFee,
			TotalAmountCents:   baseFee,
			Currency:           invoiceCurrency,
			TaxCode:            plan.TaxCode,
			BillingPeriodStart: &baseStartCopy,
			BillingPeriodEnd:   &baseEndCopy,
		})
		subtotal += baseFee
	}

	if subtotal <= 0 {
		// All in_advance items had zero base fees. Nothing to bill;
		// don't emit a $0 invoice (matches cycle-path behavior for
		// zero-amount cycles).
		return domain.Invoice{}, nil
	}

	// Apply tax.
	taxApp, err := e.ApplyTaxToLineItems(ctx, sub.TenantID, sub.CustomerID, invoiceCurrency, subtotal, 0, lineItems)
	if err != nil {
		return domain.Invoice{}, fmt.Errorf("apply tax: %w", err)
	}

	netDays := 0
	if e.settings != nil {
		if ts, err := e.settings.Get(ctx, sub.TenantID); err == nil && ts.NetPaymentTerms > 0 {
			netDays = ts.NetPaymentTerms
		}
	}
	dueAt := now.AddDate(0, 0, netDays)

	totalWithTax := taxApp.SubtotalCents - taxApp.DiscountCents + taxApp.TaxAmountCents

	// Mint an invoice number — same path the cycle invoice takes
	// (NextInvoiceNumber via tenant settings). Without this, the
	// day-1 invoice persists with an empty invoice_number and the
	// dashboard H1 renders blank.
	invoiceNumber, err := e.settings.NextInvoiceNumber(ctx, sub.TenantID)
	if err != nil {
		return domain.Invoice{}, fmt.Errorf("mint invoice number: %w", err)
	}

	inv, err := e.invoices.CreateInvoiceWithLineItems(ctx, sub.TenantID, domain.Invoice{
		TenantID:           sub.TenantID,
		CustomerID:         sub.CustomerID,
		SubscriptionID:     sub.ID,
		InvoiceNumber:      invoiceNumber,
		// Tax-deferred + pause-collection gate (matches billOnePeriod).
		// Pre-fix this path hardcoded Finalized regardless of tax;
		// invoices with tax_status=pending finalized with
		// TaxAmountCents=0, lying about authoritative amounts.
		Status:             domain.InvoiceFinalizationStatus(taxApp.TaxStatus, sub.PauseCollection),
		PaymentStatus:      domain.PaymentPending,
		Currency:           invoiceCurrency,
		SubtotalCents:      taxApp.SubtotalCents,
		DiscountCents:      taxApp.DiscountCents,
		TaxRateBP:          taxApp.TaxRateBP,
		TaxName:            taxApp.TaxName,
		TaxCountry:         taxApp.TaxCountry,
		TaxID:              taxApp.TaxID,
		TaxAmountCents:     taxApp.TaxAmountCents,
		TaxProvider:        taxApp.TaxProvider,
		TaxCalculationID:   taxApp.TaxCalculationID,
		TaxReverseCharge:   taxApp.TaxReverseCharge,
		TaxExemptReason:    taxApp.TaxExemptReason,
		TaxStatus:          taxApp.TaxStatus,
		TaxDeferredAt:      taxApp.TaxDeferredAt,
		TaxPendingReason:   taxApp.TaxPendingReason,
		TaxErrorCode:       taxApp.TaxErrorCode,
		TotalAmountCents:   totalWithTax,
		AmountDueCents:     totalWithTax,
		BillingPeriodStart: periodStart,
		BillingPeriodEnd:   periodEnd,
		IssuedAt:           &now,
		DueAt:              &dueAt,
		CreatedAt:          now,
		NetPaymentTermDays: netDays,
		BillingReason:      domain.BillingReasonSubscriptionCreate,
	}, lineItems)
	if err != nil {
		if errors.Is(err, errs.ErrAlreadyExists) {
			slog.Info("subscription_create invoice already exists (idempotent skip)",
				"subscription_id", sub.ID,
				"period_start", periodStart,
				"period_end", periodEnd,
			)
			return domain.Invoice{}, nil
		}
		return domain.Invoice{}, fmt.Errorf("create invoice: %w", err)
	}

	// Commit tax if a provider produced a calculation (same pattern
	// as the cycle path; ManualProvider / NoneProvider no-op).
	if inv.TaxProvider != "" && inv.TaxCalculationID != "" {
		if err := e.CommitTax(ctx, sub.TenantID, inv.ID, inv.TaxCalculationID); err != nil {
			slog.Warn("tax: commit failed after subscription_create invoice",
				"error", err,
				"tenant_id", sub.TenantID,
				"invoice_id", inv.ID,
			)
		}
	}

	// Auto-charge: PM ready → synchronous charge; no PM → queue +
	// notify. Mirrors the post-finalize block in billOnePeriod.
	if e.charger != nil && e.paymentSetups != nil && inv.AmountDueCents > 0 {
		stripeCusID, hasDefaultPM, psErr := e.paymentSetups.ResolveForCharge(ctx, sub.TenantID, sub.CustomerID)
		pmReady := psErr == nil && hasDefaultPM && stripeCusID != ""

		if pmReady {
			chargeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			chargeInv, err := e.invoices.GetInvoice(chargeCtx, sub.TenantID, inv.ID)
			if err == nil && chargeInv.AmountDueCents > 0 {
				if _, err := e.charger.ChargeInvoice(chargeCtx, sub.TenantID, chargeInv, stripeCusID); err != nil {
					slog.Warn("subscription_create auto-charge failed, marking for retry",
						"invoice_id", inv.ID,
						"error", err,
					)
					_ = e.invoices.SetAutoChargePending(ctx, sub.TenantID, inv.ID, true)
				}
			}
		} else {
			_ = e.invoices.SetAutoChargePending(ctx, sub.TenantID, inv.ID, true)
			if e.noPMNotifier != nil {
				if notifyInv, err := e.invoices.GetInvoice(ctx, sub.TenantID, inv.ID); err == nil {
					if err := e.noPMNotifier.NotifyNoPaymentMethod(ctx, sub.TenantID, notifyInv); err != nil {
						slog.Warn("subscription_create no-PM notification failed",
							"invoice_id", inv.ID,
							"error", err,
						)
					}
				}
			}
		}
	}

	slog.Info("subscription_create invoice generated",
		"invoice_id", inv.ID,
		"subscription_id", sub.ID,
		"total_cents", totalWithTax,
		"line_items", len(lineItems),
	)
	return inv, nil
}

// BillFinalOnImmediateCancel emits the final partial-period invoice
// for a sub that was canceled mid-period via the operator's immediate
// Cancel action. Pre-PR-10, mid-period immediate cancels generated NO
// final invoice — partial-period usage was never billed (revenue leak:
// customer could rack up usage and cancel for free). For in_arrears
// plans, the partial-period base was also never billed.
//
// Scope of the invoice:
//   - Period: [current_period_start, canceled_at] — the elapsed
//     portion of the just-canceled cycle.
//   - Lines:
//     • in_arrears base items: prorated by `elapsed / full_cycle`.
//     • in_advance base items: skipped (already paid up-front; the
//       refund of the unused portion is BillOnCancel's job — credit
//       grant to balance).
//     • Usage: aggregated for [periodStart, canceled_at] across every
//       meter referenced by every item. Same shape as a normal cycle
//       invoice's usage section, just with a truncated period_end.
//   - billing_reason = subscription_cancel (distinguishes from the
//     normal subscription_cycle invoice and from subscription_create).
//
// No-op when:
//   - sub.Status != canceled (defensive — caller should have flipped it)
//   - no current period set / canceled_at missing
//   - canceled_at AT or AFTER current_period_end (clean cancel at
//     boundary; the cycle close already fired or will fire normally)
//   - canceled_at AT or BEFORE current_period_start (defensive)
//   - computed subtotal rounds to zero (no in_arrears base AND no
//     usage events; matches BillOnCreate's $0-invoice-skip pattern)
//
// Idempotent via the standard (subscription_id, period_start,
// period_end) UNIQUE constraint — the period_end is canceled_at which
// is durably stamped, so a retry against the same canceled sub
// returns the existing-invoice idempotent skip.
//
// Auto-charge: attempted synchronously when a PM is ready, mirrors
// BillOnCreate's post-finalize path. Without a PM, the invoice is
// marked auto_charge_pending and the no-PM notifier fires. Dunning
// takes over from there on a failed charge.
func (e *Engine) BillFinalOnImmediateCancel(ctx context.Context, sub domain.Subscription) (domain.Invoice, error) {
	ctx, span := telemetry.Tracer("billing").Start(ctx, "billing.BillFinalOnImmediateCancel",
		trace.WithAttributes(
			attribute.String("subscription_id", sub.ID),
			attribute.String("tenant_id", sub.TenantID),
		),
	)
	defer span.End()

	if sub.Status != domain.SubscriptionCanceled {
		return domain.Invoice{}, nil
	}
	if sub.CurrentBillingPeriodStart == nil || sub.CurrentBillingPeriodEnd == nil {
		return domain.Invoice{}, nil
	}
	if sub.CanceledAt == nil {
		return domain.Invoice{}, nil
	}
	periodStart := *sub.CurrentBillingPeriodStart
	periodEnd := *sub.CurrentBillingPeriodEnd
	canceledAt := *sub.CanceledAt

	// Only mid-period cancels need a final invoice. canceled_at at or
	// after period_end means the cycle close handles the period
	// normally; canceled_at at or before period_start is defensive.
	if !canceledAt.After(periodStart) || !canceledAt.Before(periodEnd) {
		return domain.Invoice{}, nil
	}

	// Resolve plans for every item — needed for in_arrears proration
	// math, currency resolution, and usage-meter discovery.
	plans := make(map[string]domain.Plan, len(sub.Items))
	for _, it := range sub.Items {
		if _, ok := plans[it.PlanID]; ok {
			continue
		}
		pl, err := e.pricing.GetPlan(ctx, sub.TenantID, it.PlanID)
		if err != nil {
			return domain.Invoice{}, fmt.Errorf("get plan %s: %w", it.PlanID, err)
		}
		plans[it.PlanID] = pl
	}
	if len(sub.Items) == 0 {
		return domain.Invoice{}, nil
	}

	// Invoice currency: billing profile > tenant settings > first
	// item's plan currency > "usd". Same precedence as billOnePeriod
	// and BillOnCreate.
	invoiceCurrency := plans[sub.Items[0].PlanID].Currency
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

	// Build base lines: segment-aware in_arrears billing over the
	// partial period [periodStart, canceledAt]. in_advance items are
	// explicitly skipped — their base for the just-canceled period
	// was already paid at period start (BillOnCreate or prior cycle),
	// and the unused portion will be refunded by BillOnCancel as a
	// credit grant.
	//
	// Segment-aware: if the customer changed plan or quantity (or
	// added/removed an item) between periodStart and canceledAt, each
	// segment is billed at its own rate × (segment_days / cycle_days).
	// No mid-period changes → single segment from periodStart to
	// canceledAt, matching the pre-segment-aware single-line behavior.
	itemChanges, _ := e.subs.ListItemChangesInPeriod(ctx, sub.TenantID, sub.ID, periodStart, canceledAt)
	changesByItem := map[string][]domain.SubscriptionItemChange{}
	for _, c := range itemChanges {
		changesByItem[c.SubscriptionItemID] = append(changesByItem[c.SubscriptionItemID], c)
	}
	for _, c := range itemChanges {
		for _, pid := range []string{c.FromPlanID, c.ToPlanID} {
			if pid == "" {
				continue
			}
			if _, ok := plans[pid]; ok {
				continue
			}
			pl, err := e.pricing.GetPlan(ctx, sub.TenantID, pid)
			if err != nil {
				continue
			}
			plans[pid] = pl
		}
	}
	itemsByID := map[string]domain.SubscriptionItem{}
	for _, it := range sub.Items {
		itemsByID[it.ID] = it
	}

	lineItems := make([]domain.InvoiceLineItem, 0, len(sub.Items))
	subtotal := int64(0)
	periodDays := roundDays(canceledAt.Sub(periodStart))

	// Pass 1: items currently on the sub at cancel time.
	for _, it := range sub.Items {
		plan := plans[it.PlanID]
		if plan.BaseBillTiming == domain.BillInAdvance {
			continue
		}
		itemForSeg := it
		segments := itemBaseSegments(&itemForSeg, changesByItem[it.ID], periodStart, canceledAt)
		for _, seg := range segments {
			segPlan, ok := plans[seg.planID]
			if !ok || segPlan.BaseAmountCents <= 0 || segPlan.BaseBillTiming == domain.BillInAdvance {
				continue
			}
			emitBaseSegmentLine(seg, segPlan, periodStart, periodDays, invoiceCurrency, &lineItems, &subtotal)
		}
	}

	// Pass 2: items removed between periodStart and canceledAt (in the
	// change log but not on sub.Items now). Bill their pre-remove
	// segments at the respective plans' rates.
	for itemID, changes := range changesByItem {
		if _, stillPresent := itemsByID[itemID]; stillPresent {
			continue
		}
		segments := itemBaseSegments(nil, changes, periodStart, canceledAt)
		for _, seg := range segments {
			segPlan, ok := plans[seg.planID]
			if !ok || segPlan.BaseAmountCents <= 0 || segPlan.BaseBillTiming == domain.BillInAdvance {
				continue
			}
			emitBaseSegmentLine(seg, segPlan, periodStart, periodDays, invoiceCurrency, &lineItems, &subtotal)
		}
	}

	// Build usage lines: aggregate every meter referenced by every
	// plan that was active at any point in [periodStart, canceledAt].
	// Same aggregation surface the cycle path uses; only the period
	// boundaries differ. Iterates the full `plans` map (current items'
	// plans + hydrated change-log plans) so segment-aware billing
	// picks up meters that existed on now-removed plans.
	meterAggs := make(map[string]string)
	for _, pl := range plans {
		for _, meterID := range pl.MeterIDs {
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
	usageTotals := make(map[string]decimal.Decimal)
	if len(meterAggs) > 0 {
		totals, err := e.usage.AggregateForBillingPeriodByAgg(ctx, sub.TenantID, sub.CustomerID, meterAggs, periodStart, canceledAt)
		if err != nil {
			return domain.Invoice{}, fmt.Errorf("aggregate usage on cancel: %w", err)
		}
		usageTotals = totals
	}

	// Segment-aware usage billing over the partial period
	// [periodStart, canceledAt]. Mirrors the billOnePeriod shape:
	// each meter bills only for the time it was active on the sub.
	meterIntervals := map[string][]usageInterval{}
	addMeterInterval := func(mid string, start, end time.Time) {
		meterIntervals[mid] = append(meterIntervals[mid], usageInterval{start, end})
	}
	for _, it := range sub.Items {
		itemForSeg := it
		segments := itemBaseSegments(&itemForSeg, changesByItem[it.ID], periodStart, canceledAt)
		if len(segments) == 0 {
			for _, mid := range plans[it.PlanID].MeterIDs {
				addMeterInterval(mid, periodStart, canceledAt)
			}
			continue
		}
		for _, seg := range segments {
			segPlan, ok := plans[seg.planID]
			if !ok {
				continue
			}
			for _, mid := range segPlan.MeterIDs {
				addMeterInterval(mid, seg.start, seg.end)
			}
		}
	}
	for itemID, changes := range changesByItem {
		if _, stillPresent := itemsByID[itemID]; stillPresent {
			continue
		}
		segments := itemBaseSegments(nil, changes, periodStart, canceledAt)
		for _, seg := range segments {
			segPlan, ok := plans[seg.planID]
			if !ok {
				continue
			}
			for _, mid := range segPlan.MeterIDs {
				addMeterInterval(mid, seg.start, seg.end)
			}
		}
	}

	intervalAggCache := map[string]map[string]decimal.Decimal{}
	intervalKey := func(iv usageInterval) string {
		return iv.start.UTC().Format(time.RFC3339Nano) + "|" + iv.end.UTC().Format(time.RFC3339Nano)
	}

	for meterID, ivs := range meterIntervals {
		merged := mergeUsageIntervals(ivs)
		for _, iv := range merged {
			var quantity decimal.Decimal
			fullPeriod := iv.start.Equal(periodStart) && iv.end.Equal(canceledAt)
			if fullPeriod {
				quantity = usageTotals[meterID]
			} else {
				key := intervalKey(iv)
				totals, cached := intervalAggCache[key]
				if !cached {
					t, err := e.usage.AggregateForBillingPeriodByAgg(ctx, sub.TenantID, sub.CustomerID, meterAggs, iv.start, iv.end)
					if err != nil {
						return domain.Invoice{}, fmt.Errorf("aggregate usage on cancel for segment [%v, %v): %w", iv.start, iv.end, err)
					}
					totals = t
					intervalAggCache[key] = t
				}
				quantity = totals[meterID]
			}
			if quantity.IsZero() {
				continue
			}
			meter, err := e.pricing.GetMeter(ctx, sub.TenantID, meterID)
			if err != nil {
				return domain.Invoice{}, fmt.Errorf("get meter %s on cancel: %w", meterID, err)
			}
			if meter.RatingRuleVersionID == "" {
				continue
			}
			linkedRule, err := e.pricing.GetRatingRule(ctx, sub.TenantID, meter.RatingRuleVersionID)
			if err != nil {
				return domain.Invoice{}, fmt.Errorf("get rating rule for meter %s on cancel: %w", meterID, err)
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
				return domain.Invoice{}, fmt.Errorf("compute amount for meter %s on cancel: %w", meterID, err)
			}
			unitAmount := decimal.NewFromInt(amount).Div(quantity).RoundBank(0).IntPart()
			ivStart := iv.start
			ivEnd := iv.end
			lineItems = append(lineItems, domain.InvoiceLineItem{
				LineType:            domain.LineTypeUsage,
				MeterID:             meterID,
				Description:         fmt.Sprintf("%s (%s) - canceled mid-period", meter.Name, meter.Unit),
				Quantity:            quantity.IntPart(),
				UnitAmountCents:     unitAmount,
				AmountCents:         amount,
				TotalAmountCents:    amount,
				Currency:            invoiceCurrency,
				PricingMode:         string(rule.Mode),
				RatingRuleVersionID: rule.ID,
				BillingPeriodStart:  &ivStart,
				BillingPeriodEnd:    &ivEnd,
			})
			subtotal += amount
		}
	}

	if subtotal <= 0 {
		// Nothing to bill — no in_arrears base AND no usage. The
		// customer canceled before they consumed anything billable.
		// Skip the $0 invoice (matches BillOnCreate's behavior).
		return domain.Invoice{}, nil
	}

	// Apply tax.
	taxApp, err := e.ApplyTaxToLineItems(ctx, sub.TenantID, sub.CustomerID, invoiceCurrency, subtotal, 0, lineItems)
	if err != nil {
		return domain.Invoice{}, fmt.Errorf("apply tax on cancel: %w", err)
	}

	netDays := 0
	if e.settings != nil {
		if ts, err := e.settings.Get(ctx, sub.TenantID); err == nil && ts.NetPaymentTerms > 0 {
			netDays = ts.NetPaymentTerms
		}
	}
	now := e.effectiveNow(ctx, sub)
	dueAt := now.AddDate(0, 0, netDays)
	totalWithTax := taxApp.SubtotalCents - taxApp.DiscountCents + taxApp.TaxAmountCents

	invoiceNumber, err := e.settings.NextInvoiceNumber(ctx, sub.TenantID)
	if err != nil {
		return domain.Invoice{}, fmt.Errorf("mint invoice number on cancel: %w", err)
	}

	inv, err := e.invoices.CreateInvoiceWithLineItems(ctx, sub.TenantID, domain.Invoice{
		TenantID:           sub.TenantID,
		CustomerID:         sub.CustomerID,
		SubscriptionID:     sub.ID,
		InvoiceNumber:      invoiceNumber,
		// Tax-deferred + pause-collection gate (matches billOnePeriod).
		// Pre-fix this path hardcoded Finalized regardless of tax;
		// invoices with tax_status=pending finalized with
		// TaxAmountCents=0, lying about authoritative amounts.
		Status:             domain.InvoiceFinalizationStatus(taxApp.TaxStatus, sub.PauseCollection),
		PaymentStatus:      domain.PaymentPending,
		Currency:           invoiceCurrency,
		SubtotalCents:      taxApp.SubtotalCents,
		DiscountCents:      taxApp.DiscountCents,
		TaxRateBP:          taxApp.TaxRateBP,
		TaxName:            taxApp.TaxName,
		TaxCountry:         taxApp.TaxCountry,
		TaxID:              taxApp.TaxID,
		TaxAmountCents:     taxApp.TaxAmountCents,
		TaxProvider:        taxApp.TaxProvider,
		TaxCalculationID:   taxApp.TaxCalculationID,
		TaxReverseCharge:   taxApp.TaxReverseCharge,
		TaxExemptReason:    taxApp.TaxExemptReason,
		TaxStatus:          taxApp.TaxStatus,
		TaxDeferredAt:      taxApp.TaxDeferredAt,
		TaxPendingReason:   taxApp.TaxPendingReason,
		TaxErrorCode:       taxApp.TaxErrorCode,
		TotalAmountCents:   totalWithTax,
		AmountDueCents:     totalWithTax,
		BillingPeriodStart: periodStart,
		BillingPeriodEnd:   canceledAt,
		IssuedAt:           &now,
		DueAt:              &dueAt,
		CreatedAt:          now,
		NetPaymentTermDays: netDays,
		BillingReason:      domain.BillingReasonSubscriptionCancel,
	}, lineItems)
	if err != nil {
		if errors.Is(err, errs.ErrAlreadyExists) {
			slog.Info("subscription_cancel final invoice already exists (idempotent skip)",
				"subscription_id", sub.ID,
				"period_start", periodStart,
				"canceled_at", canceledAt,
			)
			return domain.Invoice{}, nil
		}
		return domain.Invoice{}, fmt.Errorf("create final-on-cancel invoice: %w", err)
	}

	if inv.TaxProvider != "" && inv.TaxCalculationID != "" {
		if err := e.CommitTax(ctx, sub.TenantID, inv.ID, inv.TaxCalculationID); err != nil {
			slog.Warn("tax: commit failed after final-on-cancel invoice",
				"error", err, "tenant_id", sub.TenantID, "invoice_id", inv.ID)
		}
	}

	// Auto-charge: mirrors the post-finalize block in billOnePeriod /
	// BillOnCreate. PM ready → synchronous attempt; no PM → queue +
	// notify (dunning takes over on a real failure).
	if e.charger != nil && e.paymentSetups != nil && inv.AmountDueCents > 0 {
		stripeCusID, hasDefaultPM, psErr := e.paymentSetups.ResolveForCharge(ctx, sub.TenantID, sub.CustomerID)
		pmReady := psErr == nil && hasDefaultPM && stripeCusID != ""
		if pmReady {
			chargeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			chargeInv, err := e.invoices.GetInvoice(chargeCtx, sub.TenantID, inv.ID)
			if err == nil && chargeInv.AmountDueCents > 0 {
				if _, err := e.charger.ChargeInvoice(chargeCtx, sub.TenantID, chargeInv, stripeCusID); err != nil {
					slog.Warn("final-on-cancel auto-charge failed, marking for retry",
						"invoice_id", inv.ID, "error", err)
					_ = e.invoices.SetAutoChargePending(ctx, sub.TenantID, inv.ID, true)
				}
			}
		} else {
			_ = e.invoices.SetAutoChargePending(ctx, sub.TenantID, inv.ID, true)
			if e.noPMNotifier != nil {
				if notifyInv, err := e.invoices.GetInvoice(ctx, sub.TenantID, inv.ID); err == nil {
					if err := e.noPMNotifier.NotifyNoPaymentMethod(ctx, sub.TenantID, notifyInv); err != nil {
						slog.Warn("final-on-cancel no-PM notification failed",
							"invoice_id", inv.ID, "error", err)
					}
				}
			}
		}
	}

	slog.Info("subscription_cancel final invoice generated",
		"invoice_id", inv.ID,
		"subscription_id", sub.ID,
		"period_start", periodStart,
		"canceled_at", canceledAt,
		"total_cents", totalWithTax,
		"line_items", len(lineItems),
	)
	return inv, nil
}


// BillOnCancel emits the cancel proration credit for an in_advance
// subscription that was canceled before its current period closed
// (ADR-031). The base for the current period was already billed —
// either on day 1 via BillOnCreate or at the prior cycle close — so
// the unused portion (canceled_at → period_end) needs to flow back
// to the customer.
//
// Refund mode: credit grant to the customer's balance. Operator can
// separately issue a credit note against the original invoice for a
// PM refund if desired (matches Stripe's out_of_band vs payment
// refund choice). Granting to balance is the safer default — no
// Stripe round-trip, applies automatically to future invoices.
//
// No-op when:
//   - creditGranter not wired (production wires; narrow unit tests skip)
//   - sub.Status != canceled (defensive — caller should have flipped it)
//   - no current period set
//   - cancel_at at/after period_end (clean cancel, no proration owed)
//   - no item's plan is in_advance (every base was arrears, nothing prebilled)
//   - computed unused amount rounds to zero
//
// Idempotency: not provided. Cancel is called once; if BillOnCancel
// fails after the cancel succeeds, the operator can manually issue
// a credit grant from the dashboard. Re-calling Cancel against an
// already-canceled sub returns the existing canceled state before
// reaching the biller (so BillOnCancel doesn't run twice in normal
// retry).
// Returns (credit_cents, error). credit_cents is the cents-amount of
// the cancel-proration credit granted (0 when no credit applied —
// in_arrears sub, clean cancel, unpaid source invoice, etc.). The
// caller surfaces this to the activity timeline so operators see the
// "Cancel proration credit: $X.XX" line linked to the cancel event.
func (e *Engine) BillOnCancel(ctx context.Context, sub domain.Subscription) (int64, error) {
	ctx, span := telemetry.Tracer("billing").Start(ctx, "billing.BillOnCancel",
		trace.WithAttributes(
			attribute.String("subscription_id", sub.ID),
			attribute.String("tenant_id", sub.TenantID),
		),
	)
	defer span.End()

	if e.creditGranter == nil {
		return 0, nil
	}
	if sub.Status != domain.SubscriptionCanceled {
		return 0, nil
	}
	if sub.CurrentBillingPeriodStart == nil || sub.CurrentBillingPeriodEnd == nil {
		return 0, nil
	}
	if sub.CanceledAt == nil {
		return 0, fmt.Errorf("canceled sub has no canceled_at")
	}

	periodStart := *sub.CurrentBillingPeriodStart
	periodEnd := *sub.CurrentBillingPeriodEnd
	cancelAt := *sub.CanceledAt

	// Clean cancel (at or after period end): period was billed normally,
	// no unused portion. Cancel-before-period-start is defensive — you
	// can't cancel before sub start.
	if !cancelAt.Before(periodEnd) || !cancelAt.After(periodStart) {
		return 0, nil
	}

	// Day-based math to avoid int64 overflow. Nanosecond math overflows
	// for ~$36+ base fees on a full-month proration. Same pattern as
	// emitBaseSegmentLine — denominator is the FULL plan-interval cycle
	// (not the current period's length), since the customer paid
	// baseFee × periodDays/fullCycleDays for a stub. Using periodDays
	// as the denominator over-refunds whenever periodDays<fullCycleDays.
	unusedDays := roundDays(periodEnd.Sub(cancelAt))
	if unusedDays <= 0 {
		return 0, nil
	}

	totalUnused := int64(0)
	for _, it := range sub.Items {
		plan, err := e.pricing.GetPlan(ctx, sub.TenantID, it.PlanID)
		if err != nil {
			return 0, fmt.Errorf("get plan %s: %w", it.PlanID, err)
		}
		if plan.BaseBillTiming != domain.BillInAdvance || plan.BaseAmountCents <= 0 {
			continue
		}
		fullCycleDays := roundDays(advanceBillingPeriod(periodStart, plan.BillingInterval).Sub(periodStart))
		if fullCycleDays <= 0 {
			continue
		}
		baseFee := plan.BaseAmountCents * it.Quantity
		unused := money.RoundHalfToEven(baseFee*int64(unusedDays), int64(fullCycleDays))
		if unused > 0 {
			totalUnused += unused
		}
	}

	if totalUnused <= 0 {
		return 0, nil
	}

	// Paid-check gate: a "refund-style" credit only makes financial sense
	// if the customer actually paid the in_advance invoice for this
	// period. Without this gate, a cancel on an in_advance sub whose
	// day-1 invoice was never paid (failed card, no PM yet) would still
	// emit a credit grant that applies against future invoices — giving
	// the customer money they never put in. Industry parity: Chargebee
	// Adjustment credit shape (reduces unpaid invoice) and Stripe's
	// `proration_behavior=none` recommendation for unpaid latest invoice.
	//
	// Velox's pre-launch choice: skip the credit grant entirely when the
	// source invoice is missing or not paid. The unpaid invoice will be
	// voided / uncollected through the normal dunning path; operator
	// can manually issue a credit grant if needed. Defaulting to "no
	// credit when in doubt" matches feedback_billing_accuracy.
	if e.invoices != nil {
		src, lookupErr := e.invoices.FindBaseInvoiceForPeriod(ctx, sub.TenantID, sub.ID, periodStart)
		if lookupErr != nil {
			slog.WarnContext(ctx, "cancel proration: source in_advance invoice not found; skipping credit grant",
				"subscription_id", sub.ID,
				"customer_id", sub.CustomerID,
				"period_start", periodStart,
				"error", lookupErr,
			)
			return 0, nil
		}
		if src.PaymentStatus != domain.PaymentSucceeded {
			slog.InfoContext(ctx, "cancel proration: source in_advance invoice not paid; skipping credit grant",
				"subscription_id", sub.ID,
				"customer_id", sub.CustomerID,
				"source_invoice_id", src.ID,
				"source_payment_status", src.PaymentStatus,
			)
			return 0, nil
		}
	}

	_, err := e.creditGranter.Grant(ctx, sub.TenantID, credit.GrantInput{
		CustomerID:           sub.CustomerID,
		AmountCents:          totalUnused,
		SourceSubscriptionID: sub.ID,
		Description: fmt.Sprintf("Cancel proration — unused portion of %s base fee (period %s to %s, canceled %s)",
			sub.Code,
			periodStart.UTC().Format("2006-01-02"),
			periodEnd.UTC().Format("2006-01-02"),
			cancelAt.UTC().Format("2006-01-02")),
		At: cancelAt,
	})
	if err != nil {
		return 0, fmt.Errorf("cancel proration credit grant: %w", err)
	}

	slog.Info("cancel proration credit issued",
		"subscription_id", sub.ID,
		"customer_id", sub.CustomerID,
		"amount_cents", totalUnused,
		"period_start", periodStart,
		"period_end", periodEnd,
		"canceled_at", cancelAt,
	)
	return totalUnused, nil
}

// BillOnPlanSwapImmediate issues the refund credit for the unused
// portion of an in_advance billed period when a sub's plan is swapped
// to a different cadence/interval mid-period. Returns the cents amount
// granted (0 when none).
//
// Mirrors BillOnCancel's refund math but takes `at` as a parameter and
// does not gate on canceled status — the caller (Service.UpdateItem)
// invokes this BEFORE applying the plan swap, while the OLD plan is
// still on the items so plan lookups resolve the outgoing rate.
//
// No-op when:
//   - creditGranter not wired (production wires; narrow unit tests skip)
//   - no current period set
//   - at at/after period_end OR at/before period_start (clean swap)
//   - no item's plan is in_advance (nothing prebilled to refund)
//   - source in_advance invoice not paid (mirrors BillOnCancel paid-check)
//   - computed unused amount rounds to zero
//
// Caller is responsible for applying the plan swap and updating the
// billing period AFTER this call — this method only handles the
// refund for the OLD in_advance portion.
func (e *Engine) BillOnPlanSwapImmediate(ctx context.Context, sub domain.Subscription, at time.Time) (int64, error) {
	ctx, span := telemetry.Tracer("billing").Start(ctx, "billing.BillOnPlanSwapImmediate",
		trace.WithAttributes(
			attribute.String("subscription_id", sub.ID),
			attribute.String("tenant_id", sub.TenantID),
		),
	)
	defer span.End()

	if e.creditGranter == nil {
		return 0, nil
	}
	if sub.CurrentBillingPeriodStart == nil || sub.CurrentBillingPeriodEnd == nil {
		return 0, nil
	}

	periodStart := *sub.CurrentBillingPeriodStart
	periodEnd := *sub.CurrentBillingPeriodEnd

	if !at.Before(periodEnd) || !at.After(periodStart) {
		return 0, nil
	}

	// Denominator is the FULL plan-interval cycle (mirrors
	// emitBaseSegmentLine / BillOnCancel). On a stub period the
	// customer paid baseFee × periodDays/fullCycleDays, so refunding
	// baseFee × unusedDays/periodDays over-credits whenever
	// periodDays<fullCycleDays.
	unusedDays := roundDays(periodEnd.Sub(at))
	if unusedDays <= 0 {
		return 0, nil
	}

	totalUnused := int64(0)
	for _, it := range sub.Items {
		plan, err := e.pricing.GetPlan(ctx, sub.TenantID, it.PlanID)
		if err != nil {
			return 0, fmt.Errorf("get plan %s: %w", it.PlanID, err)
		}
		if plan.BaseBillTiming != domain.BillInAdvance || plan.BaseAmountCents <= 0 {
			continue
		}
		fullCycleDays := roundDays(advanceBillingPeriod(periodStart, plan.BillingInterval).Sub(periodStart))
		if fullCycleDays <= 0 {
			continue
		}
		baseFee := plan.BaseAmountCents * it.Quantity
		unused := money.RoundHalfToEven(baseFee*int64(unusedDays), int64(fullCycleDays))
		if unused > 0 {
			totalUnused += unused
		}
	}

	if totalUnused <= 0 {
		return 0, nil
	}

	if e.invoices != nil {
		src, lookupErr := e.invoices.FindBaseInvoiceForPeriod(ctx, sub.TenantID, sub.ID, periodStart)
		if lookupErr != nil {
			slog.WarnContext(ctx, "plan-swap refund: source in_advance invoice not found; skipping credit grant",
				"subscription_id", sub.ID,
				"customer_id", sub.CustomerID,
				"period_start", periodStart,
				"error", lookupErr,
			)
			return 0, nil
		}
		if src.PaymentStatus != domain.PaymentSucceeded {
			slog.InfoContext(ctx, "plan-swap refund: source in_advance invoice not paid; skipping credit grant",
				"subscription_id", sub.ID,
				"customer_id", sub.CustomerID,
				"source_invoice_id", src.ID,
				"source_payment_status", src.PaymentStatus,
			)
			return 0, nil
		}
	}

	_, err := e.creditGranter.Grant(ctx, sub.TenantID, credit.GrantInput{
		CustomerID:           sub.CustomerID,
		AmountCents:          totalUnused,
		SourceSubscriptionID: sub.ID,
		Description: fmt.Sprintf("Plan-swap refund — unused portion of %s base fee (period %s to %s, swapped %s)",
			sub.Code,
			periodStart.UTC().Format("2006-01-02"),
			periodEnd.UTC().Format("2006-01-02"),
			at.UTC().Format("2006-01-02")),
		At: at,
	})
	if err != nil {
		return 0, fmt.Errorf("plan-swap refund credit grant: %w", err)
	}

	slog.Info("plan-swap refund credit issued",
		"subscription_id", sub.ID,
		"customer_id", sub.CustomerID,
		"amount_cents", totalUnused,
		"period_start", periodStart,
		"period_end", periodEnd,
		"swapped_at", at,
	)
	return totalUnused, nil
}

// RetryPendingCharges picks up invoices flagged for auto-charge retry
// and attempts to charge them. CRON path — the wall-clock scheduler
// calls this every tick. ADR-029 Phase 1: clock-pinned invoices are
// excluded from this query and are processed instead by
// RetryPendingChargesForClock during catchup.
func (e *Engine) RetryPendingCharges(ctx context.Context, limit int) (int, []error) {
	if e.charger == nil || e.paymentSetups == nil {
		return 0, nil
	}

	pending, err := e.invoices.ListAutoChargePending(ctx, limit)
	if err != nil {
		return 0, []error{fmt.Errorf("list pending charges: %w", err)}
	}
	return e.processAutoCharge(ctx, pending)
}

// RetryPendingChargesForClock is the catchup-path counterpart to
// RetryPendingCharges. Called by the test-clock catchup orchestrator
// after period generation, so any invoice whose customer attached a
// PM since the last advance fires its charge as part of THIS Advance,
// not on a future wall-clock tick. ADR-029 Phase 1.
//
// Return shape matches RetryPendingCharges (count + per-invoice
// errors) so the orchestrator can log the same telemetry shape per
// catchup phase.
func (e *Engine) RetryPendingChargesForClock(ctx context.Context, tenantID, clockID string, limit int) (int, []error) {
	if e.charger == nil || e.paymentSetups == nil {
		return 0, nil
	}
	pending, err := e.invoices.ListAutoChargePendingForClock(ctx, tenantID, clockID, limit)
	if err != nil {
		return 0, []error{fmt.Errorf("list pending charges for clock %s: %w", clockID, err)}
	}
	return e.processAutoCharge(ctx, pending)
}

// processAutoCharge is the shared body of RetryPendingCharges and
// RetryPendingChargesForClock — once the candidate list is fetched,
// the per-invoice charge loop is identical for cron and catchup.
//
// Error classification: a card decline (Stripe 402 with a typed
// decline_code) is an EXPECTED business outcome, not a catchup
// failure. ChargeInvoice already inline-fires StartDunning for the
// declined invoice and stamps invoice.payment_status='failed' →
// dunning takes over from there. Returning the decline as an error
// would push the test-clock catchup into 'internal_failure' for what
// is supposed to be a normal payment-failure flow.
//
// Only Unknown (5xx, network drop, timeout — outcome unresolved) and
// non-Stripe errors (config, infrastructure) escalate to the caller.
// Unknown errors return because the reconciler needs to follow up on
// the ambiguous PaymentIntent; the catchup orchestrator surfaces them
// to the operator. Industry parity: Stripe Test Clocks don't fail
// when a tester uses a decline-card; they record the decline and
// move on.
func (e *Engine) processAutoCharge(ctx context.Context, pending []domain.Invoice) (int, []error) {
	charged := 0
	var errs []error
	for _, inv := range pending {
		stripeCusID, hasDefaultPM, err := e.paymentSetups.ResolveForCharge(ctx, inv.TenantID, inv.CustomerID)
		if err != nil || !hasDefaultPM || stripeCusID == "" {
			continue
		}

		chargeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if _, err := e.charger.ChargeInvoice(chargeCtx, inv.TenantID, inv, stripeCusID); err != nil {
			cancel()
			// Card decline: expected outcome. ChargeInvoice already
			// stamped invoice.payment_status='failed' and fired
			// inline StartDunning (closes the Phase 3 → Phase 5
			// webhook race per stripe.go:401-426). Don't push the
			// catchup to internal_failure for a normal payment
			// failure. SetAutoChargePending(false) too so the next
			// catchup run doesn't pick the same invoice — dunning's
			// own retry schedule drives subsequent attempts.
			var pe *payment.PaymentError
			if errors.As(err, &pe) && pe.DeclineCode != "" {
				_ = e.invoices.SetAutoChargePending(ctx, inv.TenantID, inv.ID, false)
				slog.Info("auto-charge declined; dunning will retry on schedule",
					"invoice_id", inv.ID, "decline_code", pe.DeclineCode)
				continue
			}
			// Transient (breaker open / pre-Stripe timeout): skip
			// this tick without bumping dunning. The next catchup
			// will retry.
			if errors.Is(err, payment.ErrPaymentTransient) {
				slog.Info("auto-charge skipped; transient breaker/timeout",
					"invoice_id", inv.ID)
				continue
			}
			// Everything else (Unknown PaymentError or non-Stripe
			// error) escalates — the catchup operator gets visible
			// feedback to investigate.
			errs = append(errs, fmt.Errorf("charge invoice %s: %w", inv.ID, err))
			continue
		}
		cancel()

		_ = e.invoices.SetAutoChargePending(ctx, inv.TenantID, inv.ID, false)
		charged++
		slog.Info("auto-charge retry succeeded", "invoice_id", inv.ID)
	}
	return charged, errs
}

// RetryTaxForInvoice re-runs ApplyTaxToLineItems against an invoice
// that's currently parked at tax_status in (pending, failed) and
// persists the new decision atomically. Backs the operator-triggered
// "Retry tax" action surfaced by the unified Attention shape.
//
// Idempotent under retry: each call increments tax_retry_count and
// rewrites the per-line and invoice-level tax fields. A retry that
// succeeds clears TaxPendingReason / TaxErrorCode and unblocks
// finalize. A retry that fails again writes the new typed code so
// the dashboard banner refreshes — operators get an immediate signal
// of whether their fix worked, without waiting on the background
// retry worker.
//
// Gates (defence in depth — postgres re-asserts under FOR UPDATE):
//   - invoice must be draft
//   - tax_status must be pending or failed
//   - subscription is loaded if present (so jurisdiction-by-plan-tax-
//     code logic in ApplyTaxToLineItems sees the same inputs as the
//     original cycle build)
func (e *Engine) RetryTaxForInvoice(ctx context.Context, tenantID, invoiceID string) (domain.Invoice, error) {
	inv, err := e.invoices.GetInvoice(ctx, tenantID, invoiceID)
	if err != nil {
		return domain.Invoice{}, err
	}
	if inv.Status != domain.InvoiceDraft {
		return domain.Invoice{}, errs.InvalidState(fmt.Sprintf(
			"tax retry only valid on draft invoices (current: %s)", inv.Status))
	}
	if inv.TaxStatus != domain.InvoiceTaxPending && inv.TaxStatus != domain.InvoiceTaxFailed {
		return domain.Invoice{}, errs.InvalidState(fmt.Sprintf(
			"tax retry only valid when tax_status in (pending, failed) (current: %s)", inv.TaxStatus))
	}

	items, err := e.invoices.ListLineItems(ctx, tenantID, invoiceID)
	if err != nil {
		return domain.Invoice{}, fmt.Errorf("list line items: %w", err)
	}

	taxApp, err := e.ApplyTaxToLineItems(ctx, tenantID, inv.CustomerID, inv.Currency,
		inv.SubtotalCents, inv.DiscountCents, items)
	if err != nil {
		return domain.Invoice{}, fmt.Errorf("recompute tax: %w", err)
	}

	totalWithTax := inv.SubtotalCents - inv.DiscountCents + taxApp.TaxAmountCents
	if totalWithTax < 0 {
		totalWithTax = 0
	}

	update := domain.InvoiceTaxRetryUpdate{
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
		TaxErrorCode:     taxApp.TaxErrorCode,
		TotalAmountCents: totalWithTax,
		TaxNextRetryAt:   nextTaxRetry(ctx, taxApp.TaxStatus, taxApp.TaxErrorCode, inv.TaxRetryCount),
	}

	return e.invoices.UpdateTaxAtomic(ctx, tenantID, invoiceID, update, items)
}

// nextTaxRetry decides whether the reconciler should pick this row
// up again, and when. Three outcomes encoded as a return:
//
//   - ok / exempt: nil → "ready now" (the row leaves the retryable
//     filter anyway because tax_status flips out of pending/failed,
//     so the value doesn't matter; nil keeps the column tidy).
//   - retryable failure (provider_outage / unknown) under cap:
//     time.Now + taxRetryBackoff(attempts). The next reconciler
//     tick that crosses this timestamp picks it up.
//   - non-retryable failure (auth / customer_data / jurisdiction /
//     provider_not_configured): nil. The reconciler skips because
//     the code is outside taxRetryableCodes; nil keeps the row out
//     of any future retry timing logic.
//   - retryable but at-or-over cap: nil. The cap-check in
//     ListPendingTaxRetry stops fetching this row; nil is correct
//     because there's no next retry to schedule.
//
// `attempts` is the number of retries the row has already had
// (i.e. inv.TaxRetryCount BEFORE this retry runs). UpdateTaxAtomic
// increments it server-side.
//
// ctx carries effective-now via clock.WithEffectiveNow on
// clock-pinned invoices; without binding, falls back to wall-clock.
// Catchup's ListPendingTaxRetryForClock intentionally ignores
// `tax_next_retry_at` (see invoice/postgres.go:1406-1411), so the
// stamp's domain doesn't load-bear for clock-pinned scheduling — but
// keeping it in the simulation domain matches the rest of the row's
// timestamps for dashboard consistency (ADR-030).
func nextTaxRetry(ctx context.Context, status domain.InvoiceTaxStatus, errCode string, attempts int) *time.Time {
	if status == domain.InvoiceTaxOK {
		return nil
	}
	retryable := false
	for _, c := range taxRetryableCodes() {
		if c == errCode {
			retryable = true
			break
		}
	}
	if !retryable {
		return nil
	}
	// attempts here is BEFORE the increment that UpdateTaxAtomic
	// applies, so the next attempt index is `attempts` (0-based
	// schedule).
	if attempts >= maxTaxRetryAttempts-1 {
		// This was the final attempt; no next retry to schedule.
		return nil
	}
	t := clock.Now(ctx).Add(taxRetryBackoff(attempts))
	return &t
}

// advanceBillingPeriod is the legacy interval-only roll-forward —
// preserves the input timestamp's day-of-month for monthly intervals.
// Kept for non-cycle-close contexts (segment-length computation,
// in_advance base coverage projection, cancel-proration math) where
// we want the natural interval roll regardless of billing_time.
//
// **Cycle-close MUST use domain.NextBillingPeriodEnd instead** — that
// helper honors billing_time so calendar-billing subs auto-re-anchor
// after plan-interval changes drift the anchor day-of-month.
func advanceBillingPeriod(from time.Time, interval domain.BillingInterval) time.Time {
	switch interval {
	case domain.BillingYearly:
		return from.AddDate(1, 0, 0)
	default:
		return from.AddDate(0, 1, 0)
	}
}

// tenantLocation resolves the tenant's preferred timezone for cycle-
// close calendar-boundary snapping. Mirrors subscription.Service's
// tenantLocation. Failures collapse to UTC — the snap is a UX
// alignment, not a correctness invariant.
func (e *Engine) tenantLocation(ctx context.Context, tenantID string) *time.Location {
	if e.settings == nil {
		return time.UTC
	}
	ts, err := e.settings.Get(ctx, tenantID)
	if err != nil || ts.Timezone == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(ts.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}
