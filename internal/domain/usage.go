package domain

import "time"

type UsageEvent struct {
	ID             string         `json:"id"`
	TenantID       string         `json:"tenant_id,omitempty"`
	CustomerID     string         `json:"customer_id"`
	MeterID        string         `json:"meter_id"`
	SubscriptionID string         `json:"subscription_id,omitempty"`
	Quantity       int64          `json:"quantity"`
	Properties     map[string]any `json:"properties,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	Timestamp      time.Time      `json:"timestamp"`
}

type BilledEntrySource string

const (
	BilledSourceAPI    BilledEntrySource = "api"
	BilledSourceReplay BilledEntrySource = "replay_adjustment"
)

type BilledEntry struct {
	ID             string            `json:"id"`
	TenantID       string            `json:"tenant_id,omitempty"`
	CustomerID     string            `json:"customer_id"`
	MeterID        string            `json:"meter_id"`
	AmountCents    int64             `json:"amount_cents"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
	Source         BilledEntrySource `json:"source,omitempty"`
	Timestamp      time.Time         `json:"timestamp"`
}
