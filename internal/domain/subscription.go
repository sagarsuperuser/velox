package domain

import "time"

type SubscriptionStatus string

const (
	SubscriptionDraft    SubscriptionStatus = "draft"
	SubscriptionActive   SubscriptionStatus = "active"
	SubscriptionPaused   SubscriptionStatus = "paused"
	SubscriptionCanceled SubscriptionStatus = "canceled"
	SubscriptionArchived SubscriptionStatus = "archived"
)

type SubscriptionBillingTime string

const (
	BillingTimeCalendar    SubscriptionBillingTime = "calendar"
	BillingTimeAnniversary SubscriptionBillingTime = "anniversary"
)

// SubscriptionItem is a single priced line on a subscription. A subscription
// holds one or more items; each item pairs a plan with a quantity and carries
// its own pending-plan-change state so upgrades and downgrades can schedule
// independently per line.
type SubscriptionItem struct {
	ID                     string     `json:"id"`
	TenantID               string     `json:"tenant_id,omitempty"`
	SubscriptionID         string     `json:"subscription_id"`
	PlanID                 string     `json:"plan_id"`
	Quantity               int64      `json:"quantity"`
	Metadata               []byte     `json:"metadata,omitempty"` // raw JSONB
	PendingPlanID          string     `json:"pending_plan_id,omitempty"`
	PendingPlanEffectiveAt *time.Time `json:"pending_plan_effective_at,omitempty"`
	// PlanChangedAt stamps the last immediate plan swap on this item. Feeds the
	// per-item proration dedup key (invoices.source_plan_changed_at plus
	// source_subscription_item_id) so retries of the same change converge on
	// the existing invoice. Nil until the first immediate plan change.
	PlanChangedAt *time.Time `json:"plan_changed_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type Subscription struct {
	ID                        string                  `json:"id"`
	TenantID                  string                  `json:"tenant_id,omitempty"`
	Code                      string                  `json:"code"`
	DisplayName               string                  `json:"display_name"`
	CustomerID                string                  `json:"customer_id"`
	Status                    SubscriptionStatus      `json:"status"`
	BillingTime               SubscriptionBillingTime `json:"billing_time"`
	TrialStartAt              *time.Time              `json:"trial_start_at,omitempty"`
	TrialEndAt                *time.Time              `json:"trial_end_at,omitempty"`
	StartedAt                 *time.Time              `json:"started_at,omitempty"`
	ActivatedAt               *time.Time              `json:"activated_at,omitempty"`
	CanceledAt                *time.Time              `json:"canceled_at,omitempty"`
	CurrentBillingPeriodStart *time.Time              `json:"current_billing_period_start,omitempty"`
	CurrentBillingPeriodEnd   *time.Time              `json:"current_billing_period_end,omitempty"`
	NextBillingAt             *time.Time              `json:"next_billing_at,omitempty"`
	UsageCapUnits             *int64                  `json:"usage_cap_units,omitempty"` // Max usage units per billing period (nil = unlimited)
	OverageAction             string                  `json:"overage_action,omitempty"`  // "block" or "charge" (default: charge)
	TestClockID               string                  `json:"test_clock_id,omitempty"`   // Test mode only — attached simulator clock
	CreatedAt                 time.Time               `json:"created_at"`
	UpdatedAt                 time.Time               `json:"updated_at"`

	// Items is populated by store reads that hydrate the subscription with
	// its current priced lines. Writes through Store.Create require a
	// non-empty Items slice; runtime lookups (billing engine, coupon apply)
	// iterate this. A subscription without items is not a valid state.
	Items []SubscriptionItem `json:"items,omitempty"`
}
