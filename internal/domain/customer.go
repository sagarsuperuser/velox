package domain

import (
	"time"
)

// CustomerTaxStatus drives the tax decision for a customer. Mirrors the
// CHECK constraint on customer_billing_profiles.tax_status.
type CustomerTaxStatus string

const (
	// TaxStatusStandard: regular taxable customer. Tax applied per provider rules.
	TaxStatusStandard CustomerTaxStatus = "standard"
	// TaxStatusExempt: customer is not taxable (reseller, non-profit,
	// government entity with a certificate on file). Tax is zero;
	// exempt_reason is recorded on the invoice for audit.
	TaxStatusExempt CustomerTaxStatus = "exempt"
	// TaxStatusReverseCharge: B2B buyer self-accounts for tax in their
	// jurisdiction (EU VAT reverse charge, Indian GST RCM, etc.). Tax is
	// zero on the seller's invoice; the PDF renders the legally required
	// legend directing the buyer to self-assess.
	TaxStatusReverseCharge CustomerTaxStatus = "reverse_charge"
)

type CustomerStatus string

const (
	CustomerStatusActive   CustomerStatus = "active"
	CustomerStatusArchived CustomerStatus = "archived"
)

type Customer struct {
	ID          string         `json:"id"`
	TenantID    string         `json:"tenant_id,omitempty"`
	ExternalID  string         `json:"external_id"`
	DisplayName string         `json:"display_name"`
	Email       string         `json:"email,omitempty"`
	Status      CustomerStatus `json:"status"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type BillingProfileStatus string

const (
	BillingProfileMissing    BillingProfileStatus = "missing"
	BillingProfileIncomplete BillingProfileStatus = "incomplete"
	BillingProfileReady      BillingProfileStatus = "ready"
)

type CustomerBillingProfile struct {
	CustomerID   string `json:"customer_id"`
	TenantID     string `json:"tenant_id,omitempty"`
	LegalName    string `json:"legal_name,omitempty"`
	Email        string `json:"email,omitempty"`
	Phone        string `json:"phone,omitempty"`
	AddressLine1 string `json:"address_line1,omitempty"`
	AddressLine2 string `json:"address_line2,omitempty"`
	City         string `json:"city,omitempty"`
	State        string `json:"state,omitempty"`
	PostalCode   string `json:"postal_code,omitempty"`
	Country      string `json:"country,omitempty"`
	Currency     string `json:"currency,omitempty"`
	// TaxStatus drives tax-decision routing independent of the configured
	// provider. 'standard' is the default; 'exempt' and 'reverse_charge' both
	// zero the invoice tax but render different legends on the PDF (exempt
	// shows the certificate reason; reverse charge directs the buyer to
	// self-assess). Mirrors the CHECK constraint on
	// customer_billing_profiles.tax_status.
	TaxStatus       CustomerTaxStatus    `json:"tax_status"`
	TaxExemptReason string               `json:"tax_exempt_reason,omitempty"`
	TaxID           string               `json:"tax_id"`
	TaxIDType       string               `json:"tax_id_type"`
	ProfileStatus   BillingProfileStatus `json:"profile_status"`
	CreatedAt       time.Time            `json:"created_at"`
	UpdatedAt       time.Time            `json:"updated_at"`
}

type PaymentSetupStatus string

const (
	PaymentSetupMissing PaymentSetupStatus = "missing"
	PaymentSetupPending PaymentSetupStatus = "pending"
	PaymentSetupReady   PaymentSetupStatus = "ready"
	PaymentSetupError   PaymentSetupStatus = "error"
)

type CustomerPaymentSetup struct {
	CustomerID                  string             `json:"customer_id"`
	TenantID                    string             `json:"tenant_id,omitempty"`
	SetupStatus                 PaymentSetupStatus `json:"setup_status"`
	DefaultPaymentMethodPresent bool               `json:"default_payment_method_present"`
	PaymentMethodType           string             `json:"payment_method_type,omitempty"`
	StripeCustomerID            string             `json:"stripe_customer_id,omitempty"`
	StripePaymentMethodID       string             `json:"stripe_payment_method_id,omitempty"`
	CardBrand                   string             `json:"card_brand,omitempty"`
	CardLast4                   string             `json:"card_last4,omitempty"`
	CardExpMonth                int                `json:"card_exp_month,omitempty"`
	CardExpYear                 int                `json:"card_exp_year,omitempty"`
	LastVerifiedAt              *time.Time         `json:"last_verified_at,omitempty"`
	CreatedAt                   time.Time          `json:"created_at"`
	UpdatedAt                   time.Time          `json:"updated_at"`
}
