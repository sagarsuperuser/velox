package domain

import "time"

type DunningFinalAction string

const (
	DunningActionManualReview DunningFinalAction = "manual_review"
	DunningActionPause        DunningFinalAction = "pause"
	DunningActionWriteOff     DunningFinalAction = "write_off_later"
)

type DunningRunState string

const (
	DunningScheduled       DunningRunState = "scheduled"
	DunningRetryDue        DunningRunState = "retry_due"
	DunningAwaitingPayment DunningRunState = "awaiting_payment_setup"
	DunningAwaitingResult  DunningRunState = "awaiting_retry_result"
	DunningResolved        DunningRunState = "resolved"
	DunningPaused          DunningRunState = "paused"
	DunningEscalated       DunningRunState = "escalated"
	DunningExhausted       DunningRunState = "exhausted"
)

type DunningEventType string

const (
	DunningEventStarted        DunningEventType = "dunning_started"
	DunningEventRetryScheduled DunningEventType = "retry_scheduled"
	DunningEventRetryAttempted DunningEventType = "retry_attempted"
	DunningEventRetrySucceeded DunningEventType = "retry_succeeded"
	DunningEventRetryFailed    DunningEventType = "retry_failed"
	DunningEventPaused         DunningEventType = "paused"
	DunningEventResumed        DunningEventType = "resumed"
	DunningEventEscalated      DunningEventType = "escalated"
	DunningEventResolved       DunningEventType = "resolved"
)

type DunningResolution string

const (
	ResolutionPaymentSucceeded DunningResolution = "payment_succeeded"
	ResolutionNotCollectible   DunningResolution = "invoice_not_collectible"
	ResolutionOperatorResolved DunningResolution = "operator_resolved"
	ResolutionEscalated        DunningResolution = "escalated"
)

type DunningPolicy struct {
	ID               string             `json:"id"`
	TenantID         string             `json:"tenant_id,omitempty"`
	Name             string             `json:"name"`
	Enabled          bool               `json:"enabled"`
	RetrySchedule    []string           `json:"retry_schedule"`
	MaxRetryAttempts int                `json:"max_retry_attempts"`
	FinalAction      DunningFinalAction `json:"final_action"`
	GracePeriodDays  int                `json:"grace_period_days"`
	CreatedAt        time.Time          `json:"created_at"`
	UpdatedAt        time.Time          `json:"updated_at"`
}

type InvoiceDunningRun struct {
	ID            string          `json:"id"`
	TenantID      string          `json:"tenant_id,omitempty"`
	InvoiceID     string          `json:"invoice_id"`
	CustomerID    string          `json:"customer_id,omitempty"`
	PolicyID      string          `json:"policy_id"`
	State         DunningRunState `json:"state"`
	Reason        string          `json:"reason,omitempty"`
	AttemptCount  int             `json:"attempt_count"`
	LastAttemptAt *time.Time      `json:"last_attempt_at,omitempty"`
	NextActionAt  *time.Time      `json:"next_action_at,omitempty"`
	Paused        bool            `json:"paused"`
	ResolvedAt    *time.Time      `json:"resolved_at,omitempty"`
	Resolution    DunningResolution `json:"resolution,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

type InvoiceDunningEvent struct {
	ID           string           `json:"id"`
	RunID        string           `json:"run_id"`
	TenantID     string           `json:"tenant_id,omitempty"`
	InvoiceID    string           `json:"invoice_id"`
	EventType    DunningEventType `json:"event_type"`
	State        DunningRunState  `json:"state"`
	Reason       string           `json:"reason,omitempty"`
	AttemptCount int              `json:"attempt_count"`
	Metadata     map[string]any   `json:"metadata,omitempty"`
	CreatedAt    time.Time        `json:"created_at"`
}
