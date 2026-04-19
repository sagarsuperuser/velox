package domain

import "time"

// UsageEventOrigin is the ingest path that produced a usage_events row.
// 'api' covers real-time POST /usage-events (and /batch); 'backfill' covers
// the explicit historical-ingest endpoint. Extend as new sources appear.
type UsageEventOrigin string

const (
	UsageOriginAPI      UsageEventOrigin = "api"
	UsageOriginBackfill UsageEventOrigin = "backfill"
)

type UsageEvent struct {
	ID             string           `json:"id"`
	TenantID       string           `json:"tenant_id,omitempty"`
	CustomerID     string           `json:"customer_id"`
	MeterID        string           `json:"meter_id"`
	SubscriptionID string           `json:"subscription_id,omitempty"`
	Quantity       int64            `json:"quantity"`
	Properties     map[string]any   `json:"properties,omitempty"`
	IdempotencyKey string           `json:"idempotency_key,omitempty"`
	Timestamp      time.Time        `json:"timestamp"`
	Origin         UsageEventOrigin `json:"origin,omitempty"`
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
