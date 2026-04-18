package domain

import (
	"context"
	"time"
)

type WebhookEndpoint struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenant_id,omitempty"`
	URL         string    `json:"url"`
	Description string    `json:"description,omitempty"`
	Secret      string    `json:"-"`
	Events      []string  `json:"events"`
	Active      bool      `json:"active"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type WebhookEvent struct {
	ID        string         `json:"id"`
	TenantID  string         `json:"tenant_id,omitempty"`
	EventType string         `json:"event_type"`
	Payload   map[string]any `json:"payload"`
	CreatedAt time.Time      `json:"created_at"`
}

type DeliveryStatus string

const (
	DeliveryPending   DeliveryStatus = "pending"
	DeliverySucceeded DeliveryStatus = "succeeded"
	DeliveryFailed    DeliveryStatus = "failed"
)

type WebhookDelivery struct {
	ID                string         `json:"id"`
	TenantID          string         `json:"tenant_id,omitempty"`
	WebhookEndpointID string         `json:"webhook_endpoint_id"`
	WebhookEventID    string         `json:"webhook_event_id"`
	Status            DeliveryStatus `json:"status"`
	HTTPStatusCode    int            `json:"http_status_code,omitempty"`
	ResponseBody      string         `json:"response_body,omitempty"`
	ErrorMessage      string         `json:"error_message,omitempty"`
	AttemptCount      int            `json:"attempt_count"`
	NextRetryAt       *time.Time     `json:"next_retry_at,omitempty"`
	CreatedAt         time.Time      `json:"created_at"`
	CompletedAt       *time.Time     `json:"completed_at,omitempty"`
}

// EventDispatcher fires outbound webhook events.
type EventDispatcher interface {
	Dispatch(ctx context.Context, tenantID, eventType string, payload map[string]any) error
}

// Well-known event types emitted by Velox.
const (
	EventInvoiceCreated        = "invoice.created"
	EventInvoiceFinalized      = "invoice.finalized"
	EventInvoicePaid           = "invoice.paid"
	EventInvoiceVoided         = "invoice.voided"
	EventPaymentSucceeded      = "payment.succeeded"
	EventPaymentFailed         = "payment.failed"
	EventSubscriptionCreated   = "subscription.created"
	EventSubscriptionActivated = "subscription.activated"
	EventSubscriptionCanceled  = "subscription.canceled"
	EventCustomerCreated       = "customer.created"
	EventDunningStarted        = "dunning.started"
	EventDunningEscalated      = "dunning.escalated"
	EventDunningResolved       = "dunning.resolved"
	EventSubscriptionPaused    = "subscription.paused"
	EventSubscriptionResumed   = "subscription.resumed"
	EventCreditGranted         = "credit.granted"
	EventCreditNoteIssued      = "credit_note.issued"
)
