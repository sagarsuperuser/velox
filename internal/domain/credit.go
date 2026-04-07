package domain

import "time"

type CreditEntryType string

const (
	CreditGrant      CreditEntryType = "grant"
	CreditUsage      CreditEntryType = "usage"
	CreditExpiry     CreditEntryType = "expiry"
	CreditAdjustment CreditEntryType = "adjustment"
)

type CreditLedgerEntry struct {
	ID           string          `json:"id"`
	TenantID     string          `json:"tenant_id,omitempty"`
	CustomerID   string          `json:"customer_id"`
	EntryType    CreditEntryType `json:"entry_type"`
	AmountCents  int64           `json:"amount_cents"`
	BalanceAfter int64           `json:"balance_after"`
	Description  string          `json:"description"`
	InvoiceID    string          `json:"invoice_id,omitempty"`
	ExpiresAt    *time.Time      `json:"expires_at,omitempty"`
	Metadata     map[string]any  `json:"metadata,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
}

type CreditBalance struct {
	CustomerID   string `json:"customer_id"`
	BalanceCents int64  `json:"balance_cents"`
	TotalGranted int64  `json:"total_granted"`
	TotalUsed    int64  `json:"total_used"`
	TotalExpired int64  `json:"total_expired"`
}
