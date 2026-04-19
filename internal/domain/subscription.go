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

type Subscription struct {
	ID                        string                  `json:"id"`
	TenantID                  string                  `json:"tenant_id,omitempty"`
	Code                      string                  `json:"code"`
	DisplayName               string                  `json:"display_name"`
	CustomerID                string                  `json:"customer_id"`
	PlanID                    string                  `json:"plan_id"`
	Status                    SubscriptionStatus      `json:"status"`
	BillingTime               SubscriptionBillingTime `json:"billing_time"`
	TrialStartAt              *time.Time              `json:"trial_start_at,omitempty"`
	TrialEndAt                *time.Time              `json:"trial_end_at,omitempty"`
	StartedAt                 *time.Time              `json:"started_at,omitempty"`
	ActivatedAt               *time.Time              `json:"activated_at,omitempty"`
	CanceledAt                *time.Time              `json:"canceled_at,omitempty"`
	PreviousPlanID            string                  `json:"previous_plan_id,omitempty"`
	PlanChangedAt             *time.Time              `json:"plan_changed_at,omitempty"`
	PendingPlanID             string                  `json:"pending_plan_id,omitempty"`
	PendingPlanEffectiveAt    *time.Time              `json:"pending_plan_effective_at,omitempty"`
	CurrentBillingPeriodStart *time.Time              `json:"current_billing_period_start,omitempty"`
	CurrentBillingPeriodEnd   *time.Time              `json:"current_billing_period_end,omitempty"`
	NextBillingAt             *time.Time              `json:"next_billing_at,omitempty"`
	UsageCapUnits             *int64                  `json:"usage_cap_units,omitempty"` // Max usage units per billing period (nil = unlimited)
	OverageAction             string                  `json:"overage_action,omitempty"`  // "block" or "charge" (default: charge)
	CreatedAt                 time.Time               `json:"created_at"`
	UpdatedAt                 time.Time               `json:"updated_at"`
}
