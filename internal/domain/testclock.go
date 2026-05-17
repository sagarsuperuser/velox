package domain

import "time"

// TestClockStatus tracks what a test clock is doing. Mirrors Stripe's API.
//
//   - ready: idle, safe to advance or delete.
//   - advancing: a catchup run is in flight; further advances are rejected
//     until it completes.
//   - internal_failure: the last advance errored partway through; the clock
//     is frozen at an intermediate state and the tenant must inspect
//     (advances and subscription attachment stay blocked).
type TestClockStatus string

const (
	TestClockStatusReady          TestClockStatus = "ready"
	TestClockStatusAdvancing      TestClockStatus = "advancing"
	TestClockStatusInternalFailed TestClockStatus = "internal_failure"
)

// TestClock is a frozen-time simulator scoped to a single tenant in test mode.
// Subscriptions attached to a clock read the clock's FrozenTime instead of
// wall-clock when the billing engine evaluates cycle boundaries, trial ends,
// and dunning retries — this lets integration tests walk through a multi-month
// lifecycle in seconds.
//
// Livemode is always false and is enforced by a CHECK constraint in the DB.
type TestClock struct {
	ID         string          `json:"id"`
	TenantID   string          `json:"tenant_id,omitempty"`
	Name       string          `json:"name"`
	FrozenTime time.Time       `json:"frozen_time"`
	Status     TestClockStatus `json:"status"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
	// DeletedAt marks a soft-deleted clock (ADR-016). API responses
	// omit deleted clocks entirely — read paths filter
	// deleted_at IS NULL — but the field is exposed on the JSON so
	// background tooling and migrations that bypass the live filter
	// can identify deleted rows without a second query.
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
	// LastFailureReason is the error captured when an advance
	// transitioned the clock to status='internal_failure'. Cleared
	// on the next successful advance. ADR-018.
	LastFailureReason string `json:"last_failure_reason,omitempty"`
}
