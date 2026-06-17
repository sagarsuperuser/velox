package domain

import (
	"encoding/json"
	"time"

	"github.com/shopspring/decimal"
)

type InvoiceStatus string

const (
	InvoiceDraft     InvoiceStatus = "draft"
	InvoiceFinalized InvoiceStatus = "finalized"
	InvoicePaid      InvoiceStatus = "paid"
	InvoiceVoided    InvoiceStatus = "voided"
	// InvoiceUncollectible marks an invoice as no-further-collection-
	// attempted while preserving it in financial reporting (Stripe-
	// standard semantics; distinct from Voided which annuls the
	// invoice). Set by dunning's mark_uncollectible final_action and
	// by operator action. ADR-036 amendment.
	InvoiceUncollectible InvoiceStatus = "uncollectible"
)

// InvoiceBillingReason classifies the trigger that produced an invoice.
// Mirrors Stripe's invoice.billing_reason. Persisted as nullable on
// the invoices table — legacy rows pre-FEAT-Week5c are NULL.
//
// SubscriptionCycle is the natural-cycle end invoice the cycle scan
// emits at period boundaries. SubscriptionCreate is the prorated
// initial invoice when a subscription starts mid-cycle. Manual is an
// operator-initiated standalone invoice. Threshold is the hard-cap
// early finalize fired by the threshold scan tick.
type InvoiceBillingReason string

const (
	BillingReasonSubscriptionCycle  InvoiceBillingReason = "subscription_cycle"
	BillingReasonSubscriptionCreate InvoiceBillingReason = "subscription_create"
	// BillingReasonSubscriptionCancel is the final partial-period invoice
	// emitted at mid-period immediate cancel — covers in_arrears prorated
	// base + usage from current_period_start to canceled_at. Distinct
	// from subscription_cycle so reporting / dashboard can label this as
	// a cancel-time true-up, not a normal cycle close.
	BillingReasonSubscriptionCancel InvoiceBillingReason = "subscription_cancel"
	// BillingReasonSubscriptionUpdate is the immediate proration invoice cut
	// by a mid-period subscription change — plan upgrade, quantity increase,
	// or item add (Stripe stamps the same `subscription_update` reason for
	// all of these). Previously these invoices persisted a NULL reason, so
	// the dashboard couldn't label what triggered them.
	BillingReasonSubscriptionUpdate InvoiceBillingReason = "subscription_update"
	BillingReasonManual             InvoiceBillingReason = "manual"
	BillingReasonThreshold          InvoiceBillingReason = "threshold"
)

type InvoicePaymentStatus string

const (
	PaymentPending    InvoicePaymentStatus = "pending"
	PaymentProcessing InvoicePaymentStatus = "processing"
	PaymentSucceeded  InvoicePaymentStatus = "succeeded"
	PaymentFailed     InvoicePaymentStatus = "failed"
	// PaymentUnknown marks a Stripe charge attempt that returned an ambiguous
	// error (5xx, timeout, connection reset) where we cannot tell from the
	// response whether Stripe actually processed the charge. A reconciler
	// resolves these by querying Stripe after a cool-off window.
	PaymentUnknown InvoicePaymentStatus = "unknown"
)

// InvoiceTaxStatus tracks whether tax has been successfully calculated for
// an invoice. The happy path is ok: calculation succeeded (including
// zero-tax outcomes from none/manual/exempt/reverse-charge). Pending means
// calculation failed transiently and a retry worker will try again.
// Failed means retries have been exhausted — operators resolve manually.
//
// Invoices in pending or failed are blocked from finalize: sending a wrong
// tax amount creates audit/compliance exposure, so we defer rather than
// silently fall back to an incorrect rate.
type InvoiceTaxStatus string

const (
	InvoiceTaxOK      InvoiceTaxStatus = "ok"
	InvoiceTaxPending InvoiceTaxStatus = "pending"
	InvoiceTaxFailed  InvoiceTaxStatus = "failed"
)

// InvoiceFinalizationStatus returns the invoice Status that must be
// stamped at creation time given the tax + pause-collection state.
// Single source of truth across all four invoice-emitting paths
// (engine.billOnePeriod, engine.BillOnCreate,
// engine.BillFinalOnImmediateCancel, subscription.handleItemProration)
// so the rule "tax_status=pending OR pause_collection set → draft"
// can't drift between flows.
//
//   - Tax pending: a finalized invoice implies authoritative amounts.
//     With tax deferred, the TotalAmountCents reflects subtotal +
//     zero tax — incorrect at finalize time. The retry worker
//     transitions draft → finalized when calculation succeeds.
//   - Pause-collection set: Stripe-parity behavior — cycle continues
//     and line items are captured, but the operator/customer-facing
//     finalize/charge/dunn flow is skipped until pause is cleared.
//
// pauseCollection is the *PauseCollection pointer from the
// subscription (nil = collection running normally).
func InvoiceFinalizationStatus(taxStatus InvoiceTaxStatus, pauseCollection *PauseCollection) InvoiceStatus {
	if taxStatus == InvoiceTaxPending {
		return InvoiceDraft
	}
	if pauseCollection != nil {
		return InvoiceDraft
	}
	return InvoiceFinalized
}

type Invoice struct {
	ID             string               `json:"id"`
	TenantID       string               `json:"tenant_id,omitempty"`
	CustomerID     string               `json:"customer_id"`
	SubscriptionID string               `json:"subscription_id"`
	InvoiceNumber  string               `json:"invoice_number"`
	Status         InvoiceStatus        `json:"status"`
	PaymentStatus  InvoicePaymentStatus `json:"payment_status"`
	Currency       string               `json:"currency"`
	SubtotalCents  int64                `json:"subtotal_cents"`
	DiscountCents  int64                `json:"discount_cents"`
	TaxAmountCents int64                `json:"tax_amount_cents"`
	TaxRate        float64              `json:"tax_rate"` // Percent rate (4-decimal precision via NUMERIC(7,4)). 7.25 = 7.25%. ADR-042/043.
	TaxName        string               `json:"tax_name,omitempty"`
	TaxCountry     string               `json:"tax_country,omitempty"`
	TaxID          string               `json:"tax_id,omitempty"`
	// Durable audit snapshot of the tax decision. Written once at invoice
	// build time and never mutated so a finalized invoice remains
	// reconstructable even after the upstream provider's tax_calculation
	// expires (Stripe Tax: 24 h). TaxCalculationID is the provider ref used
	// by Commit to create a tax_transaction at finalize time.
	TaxProvider      string `json:"tax_provider,omitempty"`
	TaxCalculationID string `json:"tax_calculation_id,omitempty"`
	// TaxTransactionID is the committed upstream transaction reference
	// (Stripe Tax: tx_xxx), captured at finalize time. Durable upstream
	// record of the tax decision and the handle the provider needs for
	// a later reversal when a credit note is issued against this invoice.
	// Empty for providers without durable state (none, manual) and for
	// invoices whose finalize has not committed tax yet.
	TaxTransactionID string `json:"tax_transaction_id,omitempty"`
	TaxReverseCharge bool   `json:"tax_reverse_charge,omitempty"`
	TaxExemptReason  string `json:"tax_exempt_reason,omitempty"`
	// TaxStatus gates finalize: only invoices with TaxStatus=ok are
	// finalizable. Pending/failed invoices carry no committed tax yet and
	// are either awaiting retry or awaiting operator intervention.
	TaxStatus        InvoiceTaxStatus `json:"tax_status,omitempty"`
	TaxDeferredAt    *time.Time       `json:"tax_deferred_at,omitempty"`
	TaxRetryCount    int              `json:"tax_retry_count,omitempty"`
	TaxPendingReason string           `json:"tax_pending_reason,omitempty"`
	// TaxErrorCode is the typed classification of TaxPendingReason —
	// one of customer_data_invalid / jurisdiction_unsupported /
	// provider_outage / provider_auth / unknown. Lets the operator
	// UX branch on cause (fix-customer-data vs wait-for-provider) and
	// lets webhook consumers route alerts. Empty for invoices defered
	// before this column existed (migration 0067).
	TaxErrorCode string `json:"tax_error_code,omitempty"`
	// TaxNextRetryAt is the soonest the background reconciler may
	// re-run tax calculation. NULL means "ready now" (either never
	// tried or operator-driven retry just reset the schedule).
	// Future timestamp means a previous attempt failed with a
	// retryable code and the next attempt should wait until this
	// time per the exponential backoff curve in
	// internal/billing/tax_retry.go. Migration 0074. ADR-017.
	TaxNextRetryAt *time.Time `json:"tax_next_retry_at,omitempty"`
	// PaymentCardBrand / PaymentCardLast4 capture the card used to
	// settle this invoice — populated at payment_intent.succeeded
	// time by looking up the PI's payment_method against the
	// payment_methods table. Empty when the PM is unknown to us
	// (one-off Checkout cards). Migration 0077. Surfaces in the
	// activity timeline as a sub-line on the "Invoice paid" row.
	// ADR-020.
	PaymentCardBrand string `json:"payment_card_brand,omitempty"`
	PaymentCardLast4 string `json:"payment_card_last4,omitempty"`
	// Attention is the unified "needs operator attention" surface,
	// computed on read by ClassifyInvoiceAttention. Never persisted —
	// always derived from the durable fields above (tax_status,
	// tax_error_code, payment_status, last_payment_error,
	// payment_overdue) plus due_at. Nil when the invoice is healthy
	// (terminal-state or no failure mode active). See
	// docs/adr/009-invoice-attention.md for the wire-shape contract.
	Attention           *Attention `json:"attention,omitempty"`
	TotalAmountCents    int64      `json:"total_amount_cents"`
	AmountDueCents      int64      `json:"amount_due_cents"`
	AmountPaidCents     int64      `json:"amount_paid_cents"`
	CreditsAppliedCents int64      `json:"credits_applied_cents"`
	BillingPeriodStart  time.Time  `json:"billing_period_start"`
	BillingPeriodEnd    time.Time  `json:"billing_period_end"`
	// BillingPeriodDisplay is the human period string with the INCLUSIVE last
	// covered day ("Jun 1, 2028 – Jun 30, 2028"), rendered date-only in the
	// tenant TZ — the industry-standard period display (ADR-050 follow-up).
	// Computed on read (never persisted); the raw half-open
	// BillingPeriodStart/End above are unchanged (SDK contract). Empty for
	// one-off / no-period invoices so callers omit the period row. Every
	// surface (PDF, hosted, dashboard, list) shows this one value verbatim so
	// it can't drift across the Go and TS runtimes.
	BillingPeriodDisplay  string     `json:"billing_period_display,omitempty"`
	IssuedAt              *time.Time `json:"issued_at,omitempty"`
	DueAt                 *time.Time `json:"due_at,omitempty"`
	PaidAt                *time.Time `json:"paid_at,omitempty"`
	VoidedAt              *time.Time `json:"voided_at,omitempty"`
	UncollectibleAt       *time.Time `json:"uncollectible_at,omitempty"`
	StripePaymentIntentID string     `json:"stripe_payment_intent_id,omitempty"`
	LastPaymentError      string     `json:"last_payment_error,omitempty"`
	PaymentOverdue        bool       `json:"payment_overdue"`
	AutoChargePending     bool       `json:"auto_charge_pending,omitempty"`
	PDFObjectKey          string     `json:"-"`
	// PublicToken is the hosted-invoice-URL credential (Stripe-parity
	// hosted_invoice_url). Generated at finalize; drafts have an empty
	// token. Rotatable via the rotate-public-token endpoint if the URL
	// ever leaks. Omitted from JSON when empty so list responses stay
	// clean on pre-addendum invoices that haven't been backfilled.
	PublicToken        string         `json:"public_token,omitempty"`
	NetPaymentTermDays int            `json:"net_payment_term_days"`
	Memo               string         `json:"memo,omitempty"`
	Footer             string         `json:"footer,omitempty"`
	Metadata           map[string]any `json:"metadata,omitempty"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`

	// SourcePlanChangedAt + SourceSubscriptionItemID + SourceChangeType, when
	// set, identify this invoice as a proration artifact generated by an
	// immediate item mutation (plan change, quantity change, item add, item
	// remove). Combined with TenantID + SubscriptionID they form the natural
	// key used to dedup retries of proration generation (see migrations 0026
	// + 0027 — the item id + change_type disambiguate simultaneous mutations
	// of different kinds that coincidentally share a wall-clock timestamp).
	SourcePlanChangedAt      *time.Time     `json:"source_plan_changed_at,omitempty"`
	SourceSubscriptionItemID string         `json:"source_subscription_item_id,omitempty"`
	SourceChangeType         ItemChangeType `json:"source_change_type,omitempty"`

	// BillingReason classifies the trigger that produced this invoice.
	// Stamped at create time and never mutated. Threshold-fired invoices
	// (BillingReasonThreshold) participate in the partial unique index
	// on (tenant, subscription, billing_period_start) so the threshold
	// scan re-tick is idempotent under retry.
	BillingReason InvoiceBillingReason `json:"billing_reason,omitempty"`

	// StripeInvoiceID is the source Stripe invoice id (in_xxx) populated by
	// the velox-import CLI when an invoice was imported from a Stripe
	// account. Empty for Velox-native invoices. The partial unique index
	// (idx_invoices_stripe_invoice_id) enforces dedup so the importer is
	// idempotent on rerun without ever overwriting an existing row. See
	// migration 0063 for the column + index definition and
	// internal/importstripe/invoice_importer.go for the lookup path.
	StripeInvoiceID string `json:"stripe_invoice_id,omitempty"`

	// IsSimulated records, at write time, whether this invoice's domain
	// timestamps (created/issued/due/paid) were stamped on a test clock's
	// simulated time rather than wall-clock. Set true when the creating
	// context was bound to a frozen clock (engine: the subscription carries a
	// test_clock_id; manual composer: the customer is clock-pinned). The
	// activity timeline and invoice header read this authoritative flag to
	// render the "simulated" badge — NEVER a timestamp-vs-wall-clock heuristic
	// or a read-time re-derivation from the (mutable) parent's test_clock_id.
	// Always false in live mode (test clocks are test-mode only).
	IsSimulated bool `json:"is_simulated"`
}

// ItemChangeType classifies per-item proration artifacts so the dedup index
// distinguishes a plan-change and a quantity-change that happen to share the
// same (subscription, item, timestamp). Values mirror the CHECK constraint in
// migration 0027.
type ItemChangeType string

const (
	ItemChangeTypePlan     ItemChangeType = "plan"
	ItemChangeTypeQuantity ItemChangeType = "quantity"
	ItemChangeTypeAdd      ItemChangeType = "add"
	ItemChangeTypeRemove   ItemChangeType = "remove"
)

type InvoiceLineItemType string

const (
	LineTypeBaseFee  InvoiceLineItemType = "base_fee"
	LineTypeUsage    InvoiceLineItemType = "usage"
	LineTypeAddOn    InvoiceLineItemType = "add_on"
	LineTypeDiscount InvoiceLineItemType = "discount"
	LineTypeTax      InvoiceLineItemType = "tax"
)

// InvoiceTaxRetryUpdate is the snapshot UpdateTaxAtomic writes to a
// pending or failed invoice when an operator (or the retry worker)
// triggers a tax recompute. Carries the new tax decision plus the
// resulting headline totals; per-line tax stamps travel via the
// lineItems argument.
//
// Empty TaxCalculationID + TaxStatus=pending|failed signals the
// retry itself failed — the row stays blocked from finalize and the
// dashboard banner picks up the new error code.
type InvoiceTaxRetryUpdate struct {
	// SubtotalCents / DiscountCents carry the net values read off the tax
	// application. In tax-inclusive mode the provider carves tax out of the
	// gross, so these are smaller than the operator-entered gross; persisting
	// them keeps subtotal − discount + tax == gross. In exclusive mode they
	// equal the stored header values (a no-op write).
	SubtotalCents    int64
	DiscountCents    int64
	TaxAmountCents   int64
	TaxRate          float64 // ADR-042/043: percent rate (4-decimal precision).
	TaxName          string
	TaxCountry       string
	TaxID            string
	TaxProvider      string
	TaxCalculationID string
	TaxReverseCharge bool
	TaxExemptReason  string
	TaxStatus        InvoiceTaxStatus
	TaxDeferredAt    *time.Time
	TaxPendingReason string
	TaxErrorCode     string
	TotalAmountCents int64
	// TaxNextRetryAt schedules the next reconciler attempt. nil
	// means "ready now"; a future timestamp gates the row out of
	// the worker's scan until the backoff window expires. Set
	// per the exponential schedule in internal/billing/tax_retry.go
	// when Status remains pending/failed; cleared (nil) when
	// status flips to ok/exempt. Auto-finalize on a clean tax
	// outcome happens at the service layer (so it shares the same
	// public-token + tax-commit side-effects as manual Finalize),
	// not inside this atomic update.
	TaxNextRetryAt *time.Time
}

type InvoiceLineItem struct {
	ID          string              `json:"id"`
	InvoiceID   string              `json:"invoice_id"`
	TenantID    string              `json:"tenant_id,omitempty"`
	LineType    InvoiceLineItemType `json:"line_type"`
	MeterID     string              `json:"meter_id,omitempty"`
	Description string              `json:"description"`
	Quantity    int64               `json:"quantity"`
	// QuantityDecimal is the exact (possibly fractional) usage quantity. The
	// integer Quantity above is it truncated, kept for back-compat (Stripe
	// `quantity_decimal` / Chargebee `quantity_in_decimal` parity). Zero means
	// "no decimal quantity — use Quantity" (base-fee/proration/manual lines).
	// The line amount stays whole cents (AmountCents); this only restores the
	// quantity × unit = amount reconciliation for fractional usage.
	QuantityDecimal decimal.Decimal `json:"quantity_decimal"`
	UnitAmountCents int64           `json:"unit_amount_cents"`
	AmountCents     int64           `json:"amount_cents"`
	TaxRate         float64         `json:"tax_rate"` // Percent rate (4-decimal precision). ADR-042/043.
	TaxAmountCents  int64           `json:"tax_amount_cents"`
	TaxJurisdiction string          `json:"tax_jurisdiction,omitempty"`
	TaxCode         string          `json:"tax_code,omitempty"`
	// TaxabilityReason carries the Stripe-canonical structured reason
	// (e.g. "standard_rated", "reverse_charge", "not_collecting",
	// "customer_exempt", "product_exempt", "zero_rated"). The dashboard
	// renders a badge for non-trivial values, and the PDF appends an
	// exemption legend when at least one line is customer- or
	// product-exempt. Empty for non-Stripe providers.
	TaxabilityReason    string         `json:"tax_reason,omitempty"`
	TotalAmountCents    int64          `json:"total_amount_cents"`
	Currency            string         `json:"currency"`
	PricingMode         string         `json:"pricing_mode,omitempty"`
	RatingRuleVersionID string         `json:"rating_rule_version_id,omitempty"`
	BillingPeriodStart  *time.Time     `json:"billing_period_start,omitempty"`
	BillingPeriodEnd    *time.Time     `json:"billing_period_end,omitempty"`
	Metadata            map[string]any `json:"metadata,omitempty"`
	CreatedAt           time.Time      `json:"created_at"`
}

// EffectiveUnitAmountDecimal is the per-unit price in DECIMAL CENTS, derived
// from the line's amount and quantity (amount_cents / quantity). It is the
// honest, full-precision unit price that reconciles with the rounded line
// amount (quantity × unit ≈ amount) and, unlike the whole-cent UnitAmountCents,
// does NOT collapse a sub-cent rate to 0 — e.g. 1000 units billed $3.00 →
// 0.3¢/unit = $0.003, not $0.00. It is derived on read from authoritative
// persisted values (never stored), so it can never drift from the amount;
// this is deliberately the EFFECTIVE rate, which stays well-defined for
// blended/tiered/multi-dimensional lines that have no single nominal rate.
// Precision is capped at 12 decimal places (Stripe unit_amount_decimal
// parity). Returns 0 when the quantity is 0 (no meaningful per-unit price).
func (li InvoiceLineItem) EffectiveUnitAmountDecimal() decimal.Decimal {
	qty := li.QuantityDecimal
	if qty.IsZero() {
		qty = decimal.NewFromInt(li.Quantity)
	}
	if qty.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero
	}
	return decimal.NewFromInt(li.AmountCents).DivRound(qty, 12)
}

// MarshalJSON augments the wire form with unit_amount_decimal — the
// full-precision per-unit price (decimal cents) computed by
// EffectiveUnitAmountDecimal — so dashboards and API clients can render
// sub-cent rates without the whole-cent unit_amount_cents collapsing them to
// "$0.00" (ADR-054). Computed on the fly, never persisted.
func (li InvoiceLineItem) MarshalJSON() ([]byte, error) {
	type alias InvoiceLineItem
	return json.Marshal(struct {
		alias
		UnitAmountDecimal string `json:"unit_amount_decimal"`
	}{alias(li), li.EffectiveUnitAmountDecimal().String()})
}
