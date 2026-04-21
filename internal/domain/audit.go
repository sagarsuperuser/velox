package domain

import "time"

type AuditEntry struct {
	ID            string         `json:"id"`
	TenantID      string         `json:"tenant_id,omitempty"`
	ActorType     string         `json:"actor_type"` // api_key, user, system
	ActorID       string         `json:"actor_id"`
	Action        string         `json:"action"`
	ResourceType  string         `json:"resource_type"`
	ResourceID    string         `json:"resource_id"`
	ResourceLabel string         `json:"resource_label,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
	IPAddress     string         `json:"ip_address,omitempty"`
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
	// TaxHomeCountry (ISO-3166 alpha-2) is the tenant's jurisdiction for tax.
	// When set, invoices to customers in other countries are zero-rated
	// (exports). Empty string disables cross-border zero-rating.
	TaxHomeCountry string `json:"tax_home_country,omitempty"`
	CompanyName    string `json:"company_name,omitempty"`
	CompanyAddress string `json:"company_address,omitempty"`
	CompanyEmail   string `json:"company_email,omitempty"`
	CompanyPhone   string `json:"company_phone,omitempty"`
	LogoURL        string `json:"logo_url,omitempty"`
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
	SecretKeyLast4     string     `json:"secret_key_last4"`
	PublishableKey     string     `json:"publishable_key"`
	WebhookSecretLast4 string     `json:"webhook_secret_last4,omitempty"`
	HasWebhookSecret   bool       `json:"has_webhook_secret"`
	VerifiedAt         *time.Time `json:"verified_at,omitempty"`
	LastVerifiedError  string     `json:"last_verified_error,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}
