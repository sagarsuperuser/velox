package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// BillingAlertStatus is the lifecycle of an alert.
//   - Active: armed, awaiting threshold crossing.
//   - Triggered: a one_time alert has fired and will not fire again. Stays
//     in this state until archived.
//   - TriggeredForPeriod: a per_period alert has fired in the current
//     billing cycle; the evaluator transitions back to Active when the
//     customer's cycle rolls over.
//   - Archived: soft-disabled by the operator; the evaluator skips
//     archived rows entirely.
type BillingAlertStatus string

const (
	BillingAlertStatusActive             BillingAlertStatus = "active"
	BillingAlertStatusTriggered          BillingAlertStatus = "triggered"
	BillingAlertStatusTriggeredForPeriod BillingAlertStatus = "triggered_for_period"
	BillingAlertStatusArchived           BillingAlertStatus = "archived"
)

// BillingAlertRecurrence controls how often an alert can fire.
//   - OneTime: at most once per alert lifetime.
//   - PerPeriod: at most once per billing cycle; rearms on cycle rollover.
type BillingAlertRecurrence string

const (
	BillingAlertRecurrenceOneTime   BillingAlertRecurrence = "one_time"
	BillingAlertRecurrencePerPeriod BillingAlertRecurrence = "per_period"
)

// BillingAlertThreshold carries the firing threshold. Exactly one of
// AmountCentsGTE or QuantityGTE is non-nil — the table CHECK constraint
// enforces this and the service layer validates it on Create. Both are
// surfaced on the wire (one as null) so clients can read both without
// branching on cardinality — same always-object idiom dimension_match
// uses.
type BillingAlertThreshold struct {
	AmountCentsGTE *int64           `json:"amount_gte"`
	QuantityGTE    *decimal.Decimal `json:"usage_gte"`
}

// BillingAlertFilter narrows the events an alert evaluates against.
//   - MeterID empty → cross-meter alert (sum of customer's cycle spend).
//   - MeterID set → only that meter's resolved spend.
//   - Dimensions only meaningful with MeterID set; rejected with 422
//     otherwise. Always-object idiom: empty filter marshals as {} not
//     null so dashboard rendering can iterate without nil guards.
type BillingAlertFilter struct {
	MeterID    string         `json:"meter_id,omitempty"`
	Dimensions map[string]any `json:"dimensions"`
}

// BillingAlert is the persisted alert configuration. Status is a
// state machine driven by the evaluator; LastTriggeredAt and
// LastPeriodStart are nil until the alert fires for the first time.
type BillingAlert struct {
	ID                string                 `json:"id"`
	TenantID          string                 `json:"tenant_id,omitempty"`
	CustomerID        string                 `json:"customer_id"`
	Title             string                 `json:"title"`
	Filter            BillingAlertFilter     `json:"filter"`
	Threshold         BillingAlertThreshold  `json:"threshold"`
	Recurrence        BillingAlertRecurrence `json:"recurrence"`
	Status            BillingAlertStatus     `json:"status"`
	LastTriggeredAt   *time.Time             `json:"last_triggered_at"`
	LastPeriodStart   *time.Time             `json:"last_period_start"`
	CreatedAt         time.Time              `json:"created_at"`
	UpdatedAt         time.Time              `json:"updated_at"`
}

// BillingAlertTrigger is one historical fire event for an alert. The
// evaluator inserts one row per fire inside the same tx that mutates the
// alert's status and enqueues the outbound webhook — see
// docs/design-billing-alerts.md "Atomicity contract".
//
// UNIQUE (alert_id, period_from) is the per-period idempotency key: two
// replicas racing the evaluator can both attempt to insert; one wins,
// the other gets a unique-violation that rolls its tx back and prevents
// double-emission.
type BillingAlertTrigger struct {
	ID                  string          `json:"id"`
	TenantID            string          `json:"tenant_id,omitempty"`
	AlertID             string          `json:"alert_id"`
	PeriodFrom          time.Time       `json:"period_from"`
	PeriodTo            time.Time       `json:"period_to"`
	ObservedAmountCents int64           `json:"observed_amount_cents"`
	ObservedQuantity    decimal.Decimal `json:"observed_quantity"`
	Currency            string          `json:"currency"`
	TriggeredAt         time.Time       `json:"triggered_at"`
}

// IsTerminal returns true when the alert's status excludes it from
// future evaluation. one_time→triggered and any→archived are terminal;
// per_period→triggered_for_period is NOT terminal (the evaluator may
// rearm on cycle rollover).
func (s BillingAlertStatus) IsTerminal() bool {
	return s == BillingAlertStatusTriggered || s == BillingAlertStatusArchived
}
