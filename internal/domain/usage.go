package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// UsageEventOrigin is the ingest path that produced a usage_events row.
// 'api' covers real-time POST /usage-events (and /batch); 'backfill' covers
// the explicit historical-ingest endpoint. Extend as new sources appear.
type UsageEventOrigin string

const (
	UsageOriginAPI      UsageEventOrigin = "api"
	UsageOriginBackfill UsageEventOrigin = "backfill"
)

// UsageEvent.Quantity is a NUMERIC(38, 12) decimal so AI usage primitives
// (GPU-hours, cached-token ratios, partial KV-cache reads) round-trip
// exactly. Maps to Stripe's quantity_decimal. JSON wire format is a string
// ("0.50") on the response side; the unmarshaller accepts both number (5)
// and string ("5.5") on the request side.
type UsageEvent struct {
	ID             string           `json:"id"`
	TenantID       string           `json:"tenant_id,omitempty"`
	CustomerID     string           `json:"customer_id"`
	MeterID        string           `json:"meter_id"`
	SubscriptionID string           `json:"subscription_id,omitempty"`
	Quantity       decimal.Decimal  `json:"quantity"`
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
