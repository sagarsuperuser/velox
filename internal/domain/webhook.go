package domain

import "time"

type StripeWebhookEvent struct {
	ID                 string         `json:"id"`
	TenantID           string         `json:"tenant_id"`
	Livemode           bool           `json:"livemode"`
	StripeEventID      string         `json:"stripe_event_id"`
	EventType          string         `json:"event_type"`
	ObjectType         string         `json:"object_type"`
	InvoiceID          string         `json:"invoice_id,omitempty"`
	CustomerExternalID string         `json:"customer_external_id,omitempty"`
	PaymentIntentID    string         `json:"payment_intent_id,omitempty"`
	PaymentStatus      string         `json:"payment_status,omitempty"`
	AmountCents        *int64         `json:"amount_cents,omitempty"`
	Currency           string         `json:"currency,omitempty"`
	FailureMessage     string         `json:"failure_message,omitempty"`
	Payload            map[string]any `json:"payload"`
	ReceivedAt         time.Time      `json:"received_at"`
	OccurredAt         time.Time      `json:"occurred_at"`
}
