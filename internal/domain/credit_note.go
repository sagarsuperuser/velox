package domain

import "time"

type CreditNoteStatus string

const (
	CreditNoteDraft  CreditNoteStatus = "draft"
	CreditNoteIssued CreditNoteStatus = "issued"
	CreditNoteVoided CreditNoteStatus = "voided"
)

type RefundStatus string

const (
	RefundNone      RefundStatus = "none"
	RefundPending   RefundStatus = "pending"
	RefundSucceeded RefundStatus = "succeeded"
	RefundFailed    RefundStatus = "failed"
)

type CreditNote struct {
	ID                string           `json:"id"`
	TenantID          string           `json:"tenant_id,omitempty"`
	InvoiceID         string           `json:"invoice_id"`
	CustomerID        string           `json:"customer_id"`
	CreditNoteNumber  string           `json:"credit_note_number"`
	Status            CreditNoteStatus `json:"status"`
	Reason            string           `json:"reason"`
	SubtotalCents     int64            `json:"subtotal_cents"`
	TaxAmountCents    int64            `json:"tax_amount_cents"`
	TotalCents        int64            `json:"total_cents"`
	RefundAmountCents int64            `json:"refund_amount_cents"`
	CreditAmountCents int64            `json:"credit_amount_cents"`
	Currency          string           `json:"currency"`
	IssuedAt          *time.Time       `json:"issued_at,omitempty"`
	VoidedAt          *time.Time       `json:"voided_at,omitempty"`
	RefundStatus      RefundStatus     `json:"refund_status"`
	StripeRefundID    string           `json:"stripe_refund_id,omitempty"`
	Metadata          map[string]any   `json:"metadata,omitempty"`
	CreatedAt         time.Time        `json:"created_at"`
	UpdatedAt         time.Time        `json:"updated_at"`
}

type CreditNoteLineItem struct {
	ID                string    `json:"id"`
	CreditNoteID      string    `json:"credit_note_id"`
	TenantID          string    `json:"tenant_id,omitempty"`
	InvoiceLineItemID string    `json:"invoice_line_item_id,omitempty"`
	Description       string    `json:"description"`
	Quantity          int64     `json:"quantity"`
	UnitAmountCents   int64     `json:"unit_amount_cents"`
	AmountCents       int64     `json:"amount_cents"`
	CreatedAt         time.Time `json:"created_at"`
}

type CustomerPriceOverride struct {
	ID                     string       `json:"id"`
	TenantID               string       `json:"tenant_id,omitempty"`
	CustomerID             string       `json:"customer_id"`
	RatingRuleVersionID    string       `json:"rating_rule_version_id"`
	Mode                   PricingMode  `json:"mode"`
	FlatAmountCents        int64        `json:"flat_amount_cents"`
	GraduatedTiers         []RatingTier `json:"graduated_tiers"`
	PackageSize            int64        `json:"package_size"`
	PackageAmountCents     int64        `json:"package_amount_cents"`
	OverageUnitAmountCents int64        `json:"overage_unit_amount_cents"`
	Reason                 string       `json:"reason,omitempty"`
	Active                 bool         `json:"active"`
	CreatedAt              time.Time    `json:"created_at"`
	UpdatedAt              time.Time    `json:"updated_at"`
}

// ToRatingRule converts a price override to a RatingRuleVersion for computation.
func (o CustomerPriceOverride) ToRatingRule() RatingRuleVersion {
	return RatingRuleVersion{
		ID:                     o.RatingRuleVersionID,
		Mode:                   o.Mode,
		FlatAmountCents:        o.FlatAmountCents,
		GraduatedTiers:         o.GraduatedTiers,
		PackageSize:            o.PackageSize,
		PackageAmountCents:     o.PackageAmountCents,
		OverageUnitAmountCents: o.OverageUnitAmountCents,
	}
}
