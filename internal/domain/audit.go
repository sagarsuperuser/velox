package domain

import "time"

type AuditEntry struct {
	ID            string         `json:"id"`
	TenantID      string         `json:"tenant_id,omitempty"`
	ActorType     string         `json:"actor_type"` // api_key, user, system
	ActorID       string         `json:"actor_id"`
	ActorName     string         `json:"actor_name,omitempty"` // resolved from api_keys.name when ActorType=api_key
	Action        string         `json:"action"`
	ResourceType  string         `json:"resource_type"`
	ResourceID    string         `json:"resource_id"`
	ResourceLabel string         `json:"resource_label,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
	IPAddress     string         `json:"ip_address,omitempty"`
	RequestID     string         `json:"request_id,omitempty"` // chi request UUID — joins back to server logs
	CreatedAt     time.Time      `json:"created_at"`
}

// Well-known audit actions.
const (
	AuditActionCreate   = "create"
	AuditActionUpdate   = "update"
	AuditActionDelete   = "delete"
	AuditActionActivate = "activate"
	AuditActionCancel   = "cancel"
	AuditActionPause    = "pause"
	AuditActionResume   = "resume"
	AuditActionFinalize = "finalize"
	AuditActionVoid     = "void"
	AuditActionRevoke   = "revoke"
	AuditActionGrant    = "grant"
	AuditActionRefund   = "refund"
)

type TenantSettings struct {
	TenantID        string `json:"tenant_id"`
	DefaultCurrency string `json:"default_currency"`
	Timezone        string `json:"timezone"`
	InvoicePrefix   string `json:"invoice_prefix"`
	InvoiceNextSeq  int    `json:"invoice_next_seq"`
	NetPaymentTerms int    `json:"net_payment_terms"`
	TaxRateBP       int64  `json:"tax_rate_bp"` // Basis points (1850 = 18.50%)
	TaxName         string `json:"tax_name,omitempty"`
	TaxInclusive    bool   `json:"tax_inclusive"`
	// TaxProvider selects the backend used to compute tax. 'none' skips tax
	// entirely; 'manual' uses the flat tenant-level rate below. Future
	// providers (e.g. 'stripe_tax') will be added once their integrations are
	// end-to-end verified.
	TaxProvider string `json:"tax_provider"`
	// TaxOnFailure controls what the engine does when the configured provider
	// fails transiently. 'block' (default) defers the invoice to
	// tax_status=pending and lets a retry worker try again — the safe choice
	// for jurisdictions where the manual flat rate would be wrong.
	// 'fallback_manual' preserves the legacy behaviour: silently apply the
	// tenant-configured manual rate and proceed. Opt-in only; existing
	// tenants are migrated to fallback_manual to avoid a silent policy flip.
	TaxOnFailure string `json:"tax_on_failure,omitempty"`
	// DefaultProductTaxCode is the Stripe product tax code applied when a
	// plan doesn't carry its own. Defaults to txcd_10103001 (SaaS, business
	// use) on first save; tenants override when their product mix diverges.
	// Only consulted by the stripe_tax provider; manual/none ignore it.
	DefaultProductTaxCode string `json:"default_product_tax_code,omitempty"`
	CompanyName string `json:"company_name,omitempty"`
	// Structured registered-business address. Printed in the invoice "From"
	// block and used to seed tax_home_country on first save. Flat fields
	// mirror the billing_profile.Address shape on the customer side for
	// codebase consistency and shared frontend components.
	CompanyAddressLine1 string `json:"company_address_line1,omitempty"`
	CompanyAddressLine2 string `json:"company_address_line2,omitempty"`
	CompanyCity         string `json:"company_city,omitempty"`
	CompanyState        string `json:"company_state,omitempty"`
	CompanyPostalCode   string `json:"company_postal_code,omitempty"`
	// CompanyCountry (ISO-3166 alpha-2) is the registered-business country.
	// Distinct from TaxHomeCountry: business can be registered in country A
	// but tax-resident in country B (rare, but common enough in multinational
	// setups that Stripe/QuickBooks model them separately).
	CompanyCountry string `json:"company_country,omitempty"`
	CompanyEmail   string `json:"company_email,omitempty"`
	CompanyPhone   string `json:"company_phone,omitempty"`
	LogoURL        string `json:"logo_url,omitempty"`
	// TaxID is the SELLER's tax identifier (VAT, EIN, GSTIN, ABN, ...),
	// printed in the invoice "From" block. Separate from customer.TaxID /
	// billing_profile.TaxID which hold the BUYER's identifier.
	TaxID string `json:"tax_id,omitempty"`
	// SupportURL appears in the invoice footer so customers have a
	// self-serve channel for billing questions.
	SupportURL string `json:"support_url,omitempty"`
	// InvoiceFooter is the tenant-level default footer text applied to
	// newly issued invoices. Per-invoice override via invoices.footer is
	// already supported and takes precedence.
	InvoiceFooter string `json:"invoice_footer,omitempty"`
	// AuditFailClosed makes audit log write failures hard-fail the request
	// with 503 audit_error instead of logging and returning the handler's
	// response. SOC-2-bound tenants opt in so a recorded action is a
	// precondition for a 2xx response.
	AuditFailClosed bool      `json:"audit_fail_closed"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// StripeProviderCredentials holds a tenant's connection to their own Stripe
// account for a single mode (live or test). Velox is a billing engine, not a
// merchant of record — tenants bring their own Stripe accounts and Velox
// orchestrates billing through the supplied keys. Secret keys and webhook
// signing secrets are encrypted at rest; the plaintext is only present in
// transit (Connect call → Stripe verify → DB row).
type StripeProviderCredentials struct {
	ID                 string     `json:"id"`
	TenantID           string     `json:"tenant_id"`
	Livemode           bool       `json:"livemode"`
	StripeAccountID    string     `json:"stripe_account_id,omitempty"`
	StripeAccountName  string     `json:"stripe_account_name,omitempty"`
	SecretKeyPrefix    string     `json:"secret_key_prefix,omitempty"`
	SecretKeyLast4     string     `json:"secret_key_last4"`
	PublishableKey     string     `json:"publishable_key"`
	WebhookSecretLast4 string     `json:"webhook_secret_last4,omitempty"`
	HasWebhookSecret   bool       `json:"has_webhook_secret"`
	VerifiedAt         *time.Time `json:"verified_at,omitempty"`
	LastVerifiedError  string     `json:"last_verified_error,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}
