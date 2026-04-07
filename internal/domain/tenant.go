package domain

import "time"

type TenantStatus string

const (
	TenantStatusActive    TenantStatus = "active"
	TenantStatusSuspended TenantStatus = "suspended"
	TenantStatusDeleted   TenantStatus = "deleted"
)

type Tenant struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Status    TenantStatus `json:"status"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
}

type BillingProviderType string

const (
	BillingProviderStripe BillingProviderType = "stripe"
)

type BillingProviderConnectionStatus string

const (
	BillingProviderConnectionPending   BillingProviderConnectionStatus = "pending"
	BillingProviderConnectionConnected BillingProviderConnectionStatus = "connected"
	BillingProviderConnectionError     BillingProviderConnectionStatus = "sync_error"
	BillingProviderConnectionDisabled  BillingProviderConnectionStatus = "disabled"
)

type BillingProviderConnection struct {
	ID            string                          `json:"id"`
	TenantID      string                          `json:"tenant_id"`
	ProviderType  BillingProviderType             `json:"provider_type"`
	Environment   string                          `json:"environment"`
	DisplayName   string                          `json:"display_name"`
	Status        BillingProviderConnectionStatus `json:"status"`
	SecretRef     string                          `json:"-"`
	LastSyncedAt  *time.Time                      `json:"last_synced_at,omitempty"`
	LastSyncError string                          `json:"last_sync_error,omitempty"`
	ConnectedAt   *time.Time                      `json:"connected_at,omitempty"`
	CreatedAt     time.Time                       `json:"created_at"`
	UpdatedAt     time.Time                       `json:"updated_at"`
}
