package domain

import "time"

type AuditEntry struct {
	ID           string         `json:"id"`
	TenantID     string         `json:"tenant_id,omitempty"`
	ActorType    string         `json:"actor_type"` // api_key, user, system
	ActorID      string         `json:"actor_id"`
	Action       string         `json:"action"`
	ResourceType string         `json:"resource_type"`
	ResourceID    string         `json:"resource_id"`
	ResourceLabel string         `json:"resource_label,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
	IPAddress    string         `json:"ip_address,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
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
)

type TenantSettings struct {
	TenantID        string `json:"tenant_id"`
	DefaultCurrency string `json:"default_currency"`
	Timezone        string `json:"timezone"`
	InvoicePrefix   string `json:"invoice_prefix"`
	InvoiceNextSeq  int    `json:"invoice_next_seq"`
	NetPaymentTerms int     `json:"net_payment_terms"`
	TaxRate         float64 `json:"tax_rate"`       // Deprecated: use TaxRateBP
	TaxRateBP       int     `json:"tax_rate_bp"`    // Basis points (1850 = 18.50%)
	TaxName         string  `json:"tax_name,omitempty"`
	CompanyName     string  `json:"company_name,omitempty"`
	CompanyAddress  string `json:"company_address,omitempty"`
	CompanyEmail    string `json:"company_email,omitempty"`
	CompanyPhone    string `json:"company_phone,omitempty"`
	LogoURL         string `json:"logo_url,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}
