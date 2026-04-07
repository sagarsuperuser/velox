package domain

import "time"

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
	CustomerID    string               `json:"customer_id"`
	TenantID      string               `json:"tenant_id,omitempty"`
	LegalName     string               `json:"legal_name,omitempty"`
	Email         string               `json:"email,omitempty"`
	Phone         string               `json:"phone,omitempty"`
	AddressLine1  string               `json:"address_line1,omitempty"`
	AddressLine2  string               `json:"address_line2,omitempty"`
	City          string               `json:"city,omitempty"`
	State         string               `json:"state,omitempty"`
	PostalCode    string               `json:"postal_code,omitempty"`
	Country       string               `json:"country,omitempty"`
	Currency      string               `json:"currency,omitempty"`
	TaxIdentifier string               `json:"tax_identifier,omitempty"`
	ProfileStatus BillingProfileStatus `json:"profile_status"`
	CreatedAt     time.Time            `json:"created_at"`
	UpdatedAt     time.Time            `json:"updated_at"`
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
	LastVerifiedAt              *time.Time         `json:"last_verified_at,omitempty"`
	CreatedAt                   time.Time          `json:"created_at"`
	UpdatedAt                   time.Time          `json:"updated_at"`
}
