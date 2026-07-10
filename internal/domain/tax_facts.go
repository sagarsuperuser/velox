package domain

import "time"

// TaxFacts is the 13-field bundle a single tax calculation produces — the
// facts every invoice writer must carry verbatim from the engine's
// ApplyTaxToLineItems onto the invoice row. It is embedded (untagged, so the
// JSON keys promote FLAT — the wire shape is unchanged) in domain.Invoice,
// billing.TaxApplication, domain.InvoiceTaxRetryUpdate, and
// subscription.ProrationTaxResult, collapsing what used to be four hand-synced
// mirror types and seven field-by-field copy sites into single struct
// assignments the compiler enforces.
//
// Why this type exists (2026-07-10 design review, redesign #1): the mirrors
// produced FIVE documented field-drop bugs of the same shape — provider
// provenance never committed (tax charged but never reported upstream); the
// reverse-charge/exemption legend silently vanishing from proration invoices;
// TaxErrorCode dropped by the threshold writer; and the deferral facts
// (TaxDeferredAt/TaxPendingReason/TaxErrorCode) missing from the proration
// mirror entirely — which left deferred proration invoices with
// tax_error_code=” that the tax-retry reconciler's retryable-code filter
// never matched, so they were never auto-retried. Adding a tax field is now a
// one-site change: add it here and every embedder carries it.
//
// Deliberately NOT in the bundle (invoice-only lifecycle, not products of a
// calculation): TaxTransactionID (written at Commit/finalize, not at calc),
// TaxRetryCount and TaxNextRetryAt (retry-scheduler bookkeeping).
type TaxFacts struct {
	TaxAmountCents int64   `json:"tax_amount_cents"`
	TaxRate        float64 `json:"tax_rate"` // Percent rate (4-decimal precision via NUMERIC(7,4)). 7.25 = 7.25%. ADR-042/043.
	TaxName        string  `json:"tax_name,omitempty"`
	TaxCountry     string  `json:"tax_country,omitempty"`
	TaxID          string  `json:"tax_id,omitempty"`
	// Durable audit snapshot of the tax decision. Written once at invoice
	// build time and never mutated so a finalized invoice remains
	// reconstructable even after the upstream provider's tax_calculation
	// expires (Stripe Tax: 24 h). TaxCalculationID is the provider ref used
	// by Commit to create a tax_transaction at finalize time.
	TaxProvider      string `json:"tax_provider,omitempty"`
	TaxCalculationID string `json:"tax_calculation_id,omitempty"`
	TaxReverseCharge bool   `json:"tax_reverse_charge,omitempty"`
	TaxExemptReason  string `json:"tax_exempt_reason,omitempty"`
	// TaxStatus gates finalize: only invoices with TaxStatus=ok are
	// finalizable. Pending/failed invoices carry no committed tax yet and
	// are either awaiting retry or awaiting operator intervention.
	TaxStatus        InvoiceTaxStatus `json:"tax_status,omitempty"`
	TaxDeferredAt    *time.Time       `json:"tax_deferred_at,omitempty"`
	TaxPendingReason string           `json:"tax_pending_reason,omitempty"`
	// TaxErrorCode is the typed classification of TaxPendingReason —
	// one of customer_data_invalid / jurisdiction_unsupported /
	// provider_outage / provider_auth / unknown. Lets the operator
	// UX branch on cause (fix-customer-data vs wait-for-provider), lets
	// webhook consumers route alerts, and is what the tax-retry
	// reconciler's retryable-code filter matches on. Empty for invoices
	// deferred before this column existed (migration 0067).
	TaxErrorCode string `json:"tax_error_code,omitempty"`
}
