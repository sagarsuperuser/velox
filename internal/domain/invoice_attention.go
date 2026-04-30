package domain

import "time"

// AttentionSeverity is the urgency of an Attention surface. Operators
// sort/filter on this and the dashboard renders one colour per level.
//
// Priority (descending): Critical > Warning > Info.
//
// Severity is implicit elsewhere in the industry (Stripe/Recurly convey
// it via lifecycle state); shipping it explicit is a small "industry-
// grade plus" that drives chip colour and queue ordering without
// breaking precedent.
type AttentionSeverity string

const (
	AttentionSeverityInfo     AttentionSeverity = "info"
	AttentionSeverityWarning  AttentionSeverity = "warning"
	AttentionSeverityCritical AttentionSeverity = "critical"
)

// AttentionReason is the closed, typed code naming *why* an invoice
// needs operator attention. Public-API contract — once shipped, codes
// are never repurposed; deprecations keep the code reserved and add a
// successor.
//
// Closed enum so the dashboard can switch on it for icon, copy, and
// CTA layout. The open variant — Attention.Code — carries the
// underlying provider-specific reason (e.g. tax.customer_data_invalid)
// for programmatic clients.
//
// Anchored to industry precedent: Stripe's invoice event-name
// granularity (invoice.payment_failed, invoice.finalization_failed),
// Chargebee's dunning_status, Lago's payment_status. Five codes today
// matching the signal we actually have; the enum is open for
// extension as we ship dunning, disputes, and 3DS support.
//
// Reserved-but-not-yet-emitted (planned, do not reuse):
//
//	finalization_failed         — non-tax finalization error
//	payment_action_required     — 3DS / SCA pending
//	payment_method_required     — no PM on file
//	dunning_exhausted           — Chargebee parity, post-dunning
//	dispute_lost                — Lago parity
type AttentionReason string

const (
	// AttentionReasonTaxCalculationFailed: Stripe Tax (or another
	// provider) couldn't compute tax for this invoice. Underlying cause
	// (jurisdiction unsupported, provider outage, auth, unknown) lives
	// in Attention.Code. Severity = Warning while retries are pending,
	// Critical once exhausted.
	AttentionReasonTaxCalculationFailed AttentionReason = "tax_calculation_failed"

	// AttentionReasonTaxLocationRequired: customer's billing profile is
	// missing or malformed in a way the tax provider needs (postal_code
	// for US, country, tax_id). Distinguished from the generic
	// tax_calculation_failed because the operator action is concrete:
	// edit the customer's address. Mirrors Stripe's
	// `customer_tax_location_invalid` / `requires_location_inputs`.
	AttentionReasonTaxLocationRequired AttentionReason = "tax_location_required"

	// AttentionReasonPaymentFailed: Stripe (or another payment provider)
	// declined the charge. Underlying decline_code in Attention.Code,
	// human message in Attention.Message.
	AttentionReasonPaymentFailed AttentionReason = "payment_failed"

	// AttentionReasonPaymentUnconfirmed: charge attempt returned an
	// ambiguous error (5xx, timeout) — the reconciler will resolve.
	// Velox-specific (no direct industry parity); shipped because the
	// PaymentIntent + reconciler architecture creates this state and
	// operators benefit from a quiet "the system has it" signal rather
	// than silence.
	AttentionReasonPaymentUnconfirmed AttentionReason = "payment_unconfirmed"

	// AttentionReasonOverdue: invoice is past its due_at and remains
	// unpaid. Mirrors Lago's `payment_overdue` boolean and Stripe's
	// `invoice.overdue` event. Distinct from payment_failed — overdue
	// can occur without a charge attempt (e.g. send_invoice collection).
	AttentionReasonOverdue AttentionReason = "overdue"

	// AttentionReasonPaymentProcessing: charge attempt is in flight at
	// the provider — Stripe has accepted the PaymentIntent but hasn't
	// returned a final outcome. Self-resolves; no operator action.
	// Stripe parity: `processing` status on the payment_intent.
	AttentionReasonPaymentProcessing AttentionReason = "payment_processing"

	// AttentionReasonPaymentScheduled: invoice is finalized and unpaid
	// with auto_charge_pending=true — the scheduler will fire the auto-
	// charge on its next tick. Operators can short-circuit with
	// "Charge now". Mirrors Stripe's "Awaiting payment" with auto-retry
	// scheduled state.
	AttentionReasonPaymentScheduled AttentionReason = "payment_scheduled"

	// AttentionReasonAwaitingPayment: invoice is finalized and unpaid,
	// no charge attempt has fired yet (no PaymentIntent or
	// auto_charge_pending=false). Customer-pay collection mode, or a
	// pre-first-charge window. Operator can trigger a charge or send a
	// reminder. Stripe parity: `open` invoice with no payment activity.
	AttentionReasonAwaitingPayment AttentionReason = "awaiting_payment"
)

// AttentionAction names the operator's recommended next step. Closed
// enum because every code maps to a concrete server endpoint or a
// concrete frontend route, and audit logs key off the code. Mirrors
// the Stripe / Recurly / Chargebee verb cluster (Retry / Edit / Mark
// Paid / Void / Stop Dunning).
type AttentionAction string

const (
	AttentionActionEditBillingProfile AttentionAction = "edit_billing_profile"
	AttentionActionRetryTax           AttentionAction = "retry_tax"
	AttentionActionRetryPayment       AttentionAction = "retry_payment"
	AttentionActionWaitProvider       AttentionAction = "wait_provider"
	AttentionActionRotateAPIKey       AttentionAction = "rotate_api_key"
	AttentionActionReconcilePayment   AttentionAction = "reconcile_payment"
	AttentionActionReviewRegistration AttentionAction = "review_registration"
	// AttentionActionChargeNow triggers an immediate auto-charge (calls
	// the existing collect-payment endpoint). Operators use this to
	// short-circuit the scheduler when they want to bill now.
	AttentionActionChargeNow AttentionAction = "charge_now"
	// AttentionActionSendReminder dispatches an invoice email to the
	// customer (calls the existing send-invoice-email endpoint). Used
	// in customer-pay collection mode when the invoice is sitting
	// unpaid and a nudge is appropriate.
	AttentionActionSendReminder AttentionAction = "send_reminder"
)

// AttentionActionItem is one entry in Attention.Actions. Frontend
// renders one button per item; first item is the primary CTA, the
// rest are secondary. Label is a default rendering — UIs are free to
// substitute localized copy.
type AttentionActionItem struct {
	Code  AttentionAction `json:"code"`
	Label string          `json:"label,omitempty"`
}

// Attention is the unified "this invoice needs operator attention"
// surface. Computed on read by ClassifyInvoiceAttention; never
// persisted. Durable fields (tax_status, tax_error_code,
// payment_status, last_payment_error, payment_overdue) are the source
// of truth.
//
// Wire shape mirrors Stripe's last_finalization_error /
// last_payment_error envelope (`code`, `message`, `doc_url`, `param`)
// for parity with what design partners already integrate against, plus
// three Velox additions:
//
//   - reason   — closed enum the dashboard switches on
//   - severity — closed three-value enum for sort/filter
//   - actions  — prescribed verbs so third-party UIs and audit logs
//     have a machine-readable affordance for "what to do next"
//
// On the wire, the field is omitted entirely when the invoice is
// healthy (matches Stripe's `last_finalization_error: null` ergonomic
// — clients render `if (invoice.attention)` without nested checks).
type Attention struct {
	Severity AttentionSeverity     `json:"severity"`
	Reason   AttentionReason       `json:"reason"`
	Message  string                `json:"message"`
	Actions  []AttentionActionItem `json:"actions,omitempty"`

	// Code is the open, dotted, provider-specific code for programmatic
	// clients. New codes ship without a major-version bump. Examples:
	// tax.customer_data_invalid, tax.provider_outage, payment.declined,
	// lifecycle.overdue. Stripe parity (their `code` field is also open).
	Code string `json:"code,omitempty"`

	// DocURL deep-links to the operator-facing documentation page for
	// this reason. Stripe parity. Empty when no doc page exists yet.
	DocURL string `json:"doc_url,omitempty"`

	// Param points at the entity field the operator must edit to
	// resolve the issue, in dotted-path form (e.g.
	// "customer.address.postal_code"). Stripe parity — lets the
	// dashboard's Edit CTA deep-link to the broken field. Empty when
	// the resolution isn't field-scoped.
	Param string `json:"param,omitempty"`

	// Detail is the raw provider payload (Stripe Tax JSON envelope,
	// last payment error message). Disclosed in a collapsible section
	// for diagnostic depth without polluting the headline.
	Detail string `json:"detail,omitempty"`

	// Since is when the attention condition started — operators triage
	// by age. tax_deferred_at for tax reasons; due_at for overdue;
	// updated_at as a proxy for payment_failed/unconfirmed (we don't
	// track a precise failed_at timestamp).
	Since *time.Time `json:"since,omitempty"`
}

// docBaseURL is the doc site root for operator-facing error pages. Kept
// as a const so the SDK and the dashboard agree without env coupling.
const docBaseURL = "https://docs.velox.dev/errors/"

// ClassifyInvoiceAttention returns the most-pressing reason this
// invoice needs operator attention, or nil if everything is healthy.
//
// Priority order (first match wins, descending urgency):
//  1. Tax failed          — blocks finalize, Critical
//  2. Payment failed      — money flow broken, Critical
//  3. Tax pending         — engine retrying, Warning
//  4. Overdue             — needs collection action, Warning
//  5. Payment unconfirmed — reconciler resolves, Info
//  6. Payment processing  — charge in flight at provider, Info
//  7. Payment scheduled   — auto-charge will fire on next tick, Info
//  8. Awaiting payment    — finalized, no charge attempted yet, Info
//
// Terminal-state invoices (paid, voided) never raise attention.
// Drafts also skip — the page itself communicates draft state and
// adding a banner would be redundant noise.
//
// Pure function — no I/O, deterministic on the input Invoice. Tested
// in invoice_attention_test.go across the cartesian product of
// (status, tax_status, tax_error_code, payment_status,
// payment_overdue, auto_charge_pending).
func ClassifyInvoiceAttention(inv Invoice) *Attention {
	if inv.Status == InvoicePaid || inv.Status == InvoiceVoided {
		return nil
	}
	switch {
	case inv.TaxStatus == InvoiceTaxFailed:
		return classifyTaxAttention(inv, AttentionSeverityCritical)
	case inv.PaymentStatus == PaymentFailed:
		return classifyPaymentFailure(inv)
	case inv.TaxStatus == InvoiceTaxPending:
		return classifyTaxAttention(inv, AttentionSeverityWarning)
	case inv.PaymentOverdue:
		return classifyOverdue(inv)
	case inv.PaymentStatus == PaymentUnknown:
		return classifyPaymentUnconfirmed(inv)
	case inv.PaymentStatus == PaymentProcessing:
		return classifyPaymentProcessing(inv)
	case inv.Status == InvoiceFinalized && inv.PaymentStatus == PaymentPending && inv.AutoChargePending:
		return classifyPaymentScheduled(inv)
	case inv.Status == InvoiceFinalized && inv.PaymentStatus == PaymentPending:
		return classifyAwaitingPayment(inv)
	}
	return nil
}

// classifyTaxAttention branches on the typed tax_error_code persisted
// by internal/tax.Classify. Sub-cases differ in three dimensions:
// Reason (location-vs-generic), action verbs, and `param` pointer. The
// open `code` carries the full granularity (one per error_code value).
func classifyTaxAttention(inv Invoice, severity AttentionSeverity) *Attention {
	errorCode := inv.TaxErrorCode
	if errorCode == "" {
		errorCode = "unknown"
	}

	att := &Attention{
		Severity: severity,
		Detail:   inv.TaxPendingReason,
		Since:    inv.TaxDeferredAt,
		Code:     "tax." + errorCode,
	}

	switch errorCode {
	case "customer_data_invalid":
		att.Reason = AttentionReasonTaxLocationRequired
		att.Message = "The customer's billing profile is missing or malformed data the tax provider requires (postal code, country, or tax ID)."
		att.Param = "customer.address.postal_code"
		att.DocURL = docBaseURL + "tax-location-required"
		att.Actions = []AttentionActionItem{
			{Code: AttentionActionEditBillingProfile, Label: "Edit billing profile"},
			{Code: AttentionActionRetryTax, Label: "Retry tax"},
		}
	case "jurisdiction_unsupported":
		att.Reason = AttentionReasonTaxCalculationFailed
		att.Message = "The provider can't compute tax for this customer's region. Register with the provider in that jurisdiction or override tax manually."
		att.DocURL = docBaseURL + "tax-jurisdiction-unsupported"
		att.Actions = []AttentionActionItem{
			{Code: AttentionActionReviewRegistration, Label: "Review tax registration"},
			{Code: AttentionActionRetryTax, Label: "Retry tax"},
		}
	case "provider_outage":
		att.Reason = AttentionReasonTaxCalculationFailed
		att.Message = "The tax provider is temporarily unreachable. The engine will retry automatically."
		att.DocURL = docBaseURL + "tax-provider-outage"
		att.Actions = []AttentionActionItem{
			{Code: AttentionActionWaitProvider, Label: "Check provider status"},
			{Code: AttentionActionRetryTax, Label: "Retry now"},
		}
	case "provider_auth":
		att.Reason = AttentionReasonTaxCalculationFailed
		att.Message = "The configured tax-provider API key is invalid or revoked. Rotate the key in Settings."
		att.DocURL = docBaseURL + "tax-provider-auth"
		att.Actions = []AttentionActionItem{
			{Code: AttentionActionRotateAPIKey, Label: "Rotate API key"},
			{Code: AttentionActionRetryTax, Label: "Retry tax"},
		}
	default:
		att.Reason = AttentionReasonTaxCalculationFailed
		att.Message = "Tax calculation failed for an unrecognised reason. See the raw provider response below."
		att.DocURL = docBaseURL + "tax-calculation-failed"
		att.Actions = []AttentionActionItem{
			{Code: AttentionActionRetryTax, Label: "Retry tax"},
		}
	}
	return att
}

func classifyPaymentFailure(inv Invoice) *Attention {
	headline := truncate(inv.LastPaymentError, 200)
	if headline == "" {
		headline = "Payment attempt failed."
	}
	since := inv.UpdatedAt
	return &Attention{
		Severity: AttentionSeverityCritical,
		Reason:   AttentionReasonPaymentFailed,
		Code:     "payment.declined",
		Message:  headline,
		DocURL:   docBaseURL + "payment-failed",
		Actions: []AttentionActionItem{
			{Code: AttentionActionRetryPayment, Label: "Retry payment"},
		},
		Detail: inv.LastPaymentError,
		Since:  &since,
	}
}

func classifyPaymentUnconfirmed(inv Invoice) *Attention {
	since := inv.UpdatedAt
	return &Attention{
		Severity: AttentionSeverityInfo,
		Reason:   AttentionReasonPaymentUnconfirmed,
		Code:     "payment.unconfirmed",
		Message:  "Payment outcome unconfirmed by the provider — the reconciler will resolve this automatically.",
		DocURL:   docBaseURL + "payment-unconfirmed",
		Actions: []AttentionActionItem{
			{Code: AttentionActionReconcilePayment, Label: "Check provider"},
		},
		Since: &since,
	}
}

func classifyOverdue(inv Invoice) *Attention {
	return &Attention{
		Severity: AttentionSeverityWarning,
		Reason:   AttentionReasonOverdue,
		Code:     "lifecycle.overdue",
		Message:  "Invoice is past its due date and remains unpaid.",
		DocURL:   docBaseURL + "overdue",
		Actions: []AttentionActionItem{
			{Code: AttentionActionChargeNow, Label: "Charge now"},
			{Code: AttentionActionSendReminder, Label: "Send reminder"},
		},
		Since: inv.DueAt,
	}
}

// classifyPaymentProcessing surfaces the in-flight charge state. Self-
// resolves on the next provider callback; no operator action is
// possible. Mirrors Stripe's `processing` PaymentIntent state.
func classifyPaymentProcessing(inv Invoice) *Attention {
	since := inv.UpdatedAt
	return &Attention{
		Severity: AttentionSeverityInfo,
		Reason:   AttentionReasonPaymentProcessing,
		Code:     "payment.processing",
		Message:  "Payment is in flight at the provider — awaiting confirmation.",
		DocURL:   docBaseURL + "payment-processing",
		Since:    &since,
	}
}

// classifyPaymentScheduled surfaces the scheduler-will-retry state.
// Auto-charge is queued; the engine will fire on its next sweep. The
// operator can short-circuit with "Charge now" if they don't want to
// wait the scheduler interval.
func classifyPaymentScheduled(inv Invoice) *Attention {
	since := inv.UpdatedAt
	return &Attention{
		Severity: AttentionSeverityInfo,
		Reason:   AttentionReasonPaymentScheduled,
		Code:     "payment.scheduled",
		Message:  "Auto-charge is scheduled — the engine will attempt the charge on its next tick.",
		DocURL:   docBaseURL + "payment-scheduled",
		Actions: []AttentionActionItem{
			{Code: AttentionActionChargeNow, Label: "Charge now"},
		},
		Since: &since,
	}
}

// classifyAwaitingPayment surfaces the steady-state finalized-but-
// unpaid invoice. No charge attempt has fired yet — either customer-
// pay collection mode (operator emails the link, customer self-pays)
// or a pre-first-charge window. Operators get two paths: trigger the
// charge themselves, or send the customer a reminder email.
func classifyAwaitingPayment(inv Invoice) *Attention {
	since := inv.UpdatedAt
	return &Attention{
		Severity: AttentionSeverityInfo,
		Reason:   AttentionReasonAwaitingPayment,
		Code:     "payment.awaiting",
		Message:  "Invoice is finalized and awaiting payment. No charge attempt has fired yet.",
		DocURL:   docBaseURL + "awaiting-payment",
		Actions: []AttentionActionItem{
			{Code: AttentionActionChargeNow, Label: "Charge now"},
			{Code: AttentionActionSendReminder, Label: "Send reminder"},
		},
		Since: &since,
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
