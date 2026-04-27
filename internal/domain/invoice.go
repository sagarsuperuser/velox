package domain

import "time"

type InvoiceStatus string

const (
	InvoiceDraft     InvoiceStatus = "draft"
	InvoiceFinalized InvoiceStatus = "finalized"
	InvoicePaid      InvoiceStatus = "paid"
	InvoiceVoided    InvoiceStatus = "voided"
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
	TaxRateBP      int64                `json:"tax_rate_bp"` // Basis points (1850 = 18.50%)
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
	TaxStatus             InvoiceTaxStatus `json:"tax_status,omitempty"`
	TaxDeferredAt         *time.Time       `json:"tax_deferred_at,omitempty"`
	TaxRetryCount         int              `json:"tax_retry_count,omitempty"`
	TaxPendingReason      string           `json:"tax_pending_reason,omitempty"`
	TotalAmountCents      int64            `json:"total_amount_cents"`
	AmountDueCents        int64            `json:"amount_due_cents"`
	AmountPaidCents       int64            `json:"amount_paid_cents"`
	CreditsAppliedCents   int64            `json:"credits_applied_cents"`
	BillingPeriodStart    time.Time        `json:"billing_period_start"`
	BillingPeriodEnd      time.Time        `json:"billing_period_end"`
	IssuedAt              *time.Time       `json:"issued_at,omitempty"`
	DueAt                 *time.Time       `json:"due_at,omitempty"`
	PaidAt                *time.Time       `json:"paid_at,omitempty"`
	VoidedAt              *time.Time       `json:"voided_at,omitempty"`
	StripePaymentIntentID string           `json:"stripe_payment_intent_id,omitempty"`
	LastPaymentError      string           `json:"last_payment_error,omitempty"`
	PaymentOverdue        bool             `json:"payment_overdue"`
	AutoChargePending     bool             `json:"auto_charge_pending,omitempty"`
	PDFObjectKey          string           `json:"-"`
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

// InvoiceDiscountUpdate is the snapshot ApplyDiscountAtomic writes to a
// draft invoice: the coupon discount plus the tax recompute that follows
// from the new (subtotal - discount) base. Per-line tax fields travel
// with the line items passed alongside. SubtotalCents is included
// because tax-inclusive mode requires the caller to resolve the final net
// subtotal before persistence.
//
// Lives in domain so invoice.Store (producer of the SQL) and the billing
// engine (producer of the tax recompute) can agree on the shape without
// importing each other.
type InvoiceDiscountUpdate struct {
	SubtotalCents    int64
	DiscountCents    int64
	TaxAmountCents   int64
	TaxRateBP        int64
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
}

type InvoiceLineItem struct {
	ID                  string              `json:"id"`
	InvoiceID           string              `json:"invoice_id"`
	TenantID            string              `json:"tenant_id,omitempty"`
	LineType            InvoiceLineItemType `json:"line_type"`
	MeterID             string              `json:"meter_id,omitempty"`
	Description         string              `json:"description"`
	Quantity            int64               `json:"quantity"`
	UnitAmountCents     int64               `json:"unit_amount_cents"`
	AmountCents         int64               `json:"amount_cents"`
	TaxRateBP           int64               `json:"tax_rate_bp"` // Basis points (1850 = 18.50%)
	TaxAmountCents      int64               `json:"tax_amount_cents"`
	TaxJurisdiction     string              `json:"tax_jurisdiction,omitempty"`
	TaxCode             string              `json:"tax_code,omitempty"`
	TotalAmountCents    int64               `json:"total_amount_cents"`
	Currency            string              `json:"currency"`
	PricingMode         string              `json:"pricing_mode,omitempty"`
	RatingRuleVersionID string              `json:"rating_rule_version_id,omitempty"`
	BillingPeriodStart  *time.Time          `json:"billing_period_start,omitempty"`
	BillingPeriodEnd    *time.Time          `json:"billing_period_end,omitempty"`
	Metadata            map[string]any      `json:"metadata,omitempty"`
	CreatedAt           time.Time           `json:"created_at"`
}
