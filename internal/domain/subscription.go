package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

type SubscriptionStatus string

const (
	SubscriptionDraft    SubscriptionStatus = "draft"
	SubscriptionTrialing SubscriptionStatus = "trialing"
	SubscriptionActive   SubscriptionStatus = "active"
	SubscriptionCanceled SubscriptionStatus = "canceled"
	SubscriptionArchived SubscriptionStatus = "archived"
)

type SubscriptionBillingTime string

const (
	BillingTimeCalendar    SubscriptionBillingTime = "calendar"
	BillingTimeAnniversary SubscriptionBillingTime = "anniversary"
)

// BeginningOfDayIn snaps `t` to 00:00:00 on its calendar date in `loc`,
// returned as a UTC instant for storage. Day-grade billing requires
// this to align UI-displayed dates with proration math.
func BeginningOfDayIn(t time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	local := t.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc).UTC()
}

// BeginningOfMonthIn snaps `t` to the first-of-month-at-00:00 in `loc`,
// returned as UTC. Calendar-billing anchor helper. Shared between
// subscription.Service (initial activation / trial-end / reset) and
// billing.Engine (cycle close re-anchoring) so both compute the same
// boundary from the same inputs. loc=nil falls back to UTC.
func BeginningOfMonthIn(t time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	local := t.In(loc)
	return time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc).UTC()
}

// NextBillingPeriodEnd computes the next period's end boundary at
// cycle close, honoring billing_time + interval. The semantics:
//
//   - Yearly: always anniversary (periodEnd + 1 year). No industry analog
//     for "calendar yearly stub to Jan 1"; Velox doesn't ship it either.
//   - Calendar monthly: snap to first-of-next-month in tenant TZ. This
//     auto-re-anchors subs whose day-of-month drifted from a prior
//     plan-interval change (e.g. yearly→monthly preserves the yearly
//     anniversary day at the swap; the next cycle close pulls the
//     anchor back to the calendar boundary the operator configured).
//   - Anniversary monthly: preserve day-of-month (periodEnd + 1 month).
//
// Returns the new periodEnd. The new periodStart = the old periodEnd
// at every cycle close (continuous coverage). Caller passes
// `next_billing_at = new periodEnd` so the scheduler picks the sub up
// at the right boundary.
//
// Replaces the legacy interval-only `advanceBillingPeriod(from, interval)`
// used in cycle close, which silently preserved a drifted anchor across
// plan-interval changes (industry-standard per Stripe-flexible / Lago /
// Chargebee but conflicts with the operator's calendar-billing intent).
func NextBillingPeriodEnd(periodEnd time.Time, billingTime SubscriptionBillingTime, interval BillingInterval, loc *time.Location) time.Time {
	if interval == BillingYearly {
		return periodEnd.AddDate(1, 0, 0)
	}
	if billingTime == BillingTimeCalendar {
		// Add the month in LOCAL TZ so the resulting "1st of next
		// month" is the next calendar boundary in the tenant's zone,
		// not in UTC. UTC AddDate can shift the local day across the
		// month boundary when the local-vs-UTC offset crosses
		// midnight — e.g. periodEnd = Jun 19 18:30 UTC is Jun 20 IST,
		// AddDate in UTC gives Jul 19 18:30 UTC = Jul 20 IST, then
		// BeginningOfMonth in IST snaps to Jul 1 (correct). But for
		// periodEnd = Apr 30 18:30 UTC = May 1 IST, AddDate in UTC
		// gives May 30 18:30 UTC = May 31 IST, then BeginningOfMonth
		// in IST snaps to May 1 (WRONG — caller expected Jun 1).
		// Adding the month in local TZ avoids the off-by-one.
		if loc == nil {
			loc = time.UTC
		}
		local := periodEnd.In(loc)
		next := local.AddDate(0, 1, 0)
		return time.Date(next.Year(), next.Month(), 1, 0, 0, 0, 0, loc).UTC()
	}
	return periodEnd.AddDate(0, 1, 0)
}

// PauseCollectionBehavior controls what the engine does with the invoice it
// would normally finalize during a paused-collection cycle. v1 supports
// only KeepAsDraft; the other Stripe modes (mark_uncollectible, void) need
// an "uncollectible" invoice status that doesn't exist in Velox yet.
type PauseCollectionBehavior string

const (
	PauseCollectionKeepAsDraft PauseCollectionBehavior = "keep_as_draft"
)

// PauseCollection is the per-subscription collection-pause state. The
// pointer-on-Subscription form mirrors Stripe's nullable subscription.
// pause_collection — nil = running normally, non-nil = paused.
type PauseCollection struct {
	Behavior  PauseCollectionBehavior `json:"behavior"`
	ResumesAt *time.Time              `json:"resumes_at,omitempty"`
}

// SubscriptionItemThreshold is one configured per-item usage cap on a
// subscription. UsageGTE is decimal because meter quantities can be
// fractional (cached-token ratios, GPU-hours) and we want round-trip
// precision in the wire contract; Postgres column is NUMERIC(38,12).
type SubscriptionItemThreshold struct {
	SubscriptionItemID string          `json:"subscription_item_id"`
	UsageGTE           decimal.Decimal `json:"usage_gte"`
}

// BillingThresholds is the Stripe-parity hard-cap configuration. Two
// independent threshold types:
//
//   - AmountGTE: the running cycle subtotal (cents) at which the engine
//     fires an early finalize. Currency is the subscription's own
//     currency; multi-currency subs are rejected at PATCH time.
//
//   - ItemThresholds: per-subscription-item quantity caps. When any
//     item's running cycle quantity crosses its UsageGTE, the engine
//     fires the same early finalize.
//
// Either alone or both. First to fire wins.
//
// ResetBillingCycle controls whether the cycle resets after fire. When
// true (default), the new cycle starts at fire time and the next bill
// is the natural cycle invoice for the new cycle. When false, the
// original cycle continues and a second invoice fires at the natural
// cycle end with whatever residual usage accumulated.
type BillingThresholds struct {
	AmountGTE         int64                       `json:"amount_gte,omitempty"`
	ResetBillingCycle bool                        `json:"reset_billing_cycle"`
	ItemThresholds    []SubscriptionItemThreshold `json:"item_thresholds"`
}

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

// SubscriptionItemChange is one row from the subscription_item_changes
// audit table (migration 0029). Captures every plan/quantity mutation
// on a subscription_item with both before- and after-state. Drives the
// segment-aware base-fee billing at cycle close: each row demarcates
// a [pre-change, post-change] boundary the engine uses to bill each
// segment at its own plan + quantity rate. Matches the Lago /
// Chargebee / Orb shape for mid-period proration.
type SubscriptionItemChange struct {
	ID                 string    `json:"id"`
	TenantID           string    `json:"tenant_id"`
	SubscriptionID     string    `json:"subscription_id"`
	SubscriptionItemID string    `json:"subscription_item_id,omitempty"`
	ChangeType         string    `json:"change_type"` // add | remove | plan | quantity
	FromPlanID         string    `json:"from_plan_id,omitempty"`
	ToPlanID           string    `json:"to_plan_id,omitempty"`
	FromQuantity       int64     `json:"from_quantity,omitempty"`
	ToQuantity         int64     `json:"to_quantity,omitempty"`
	ChangedAt          time.Time `json:"changed_at"`
	CreatedAt          time.Time `json:"created_at"`
}

type Subscription struct {
	ID           string                  `json:"id"`
	TenantID     string                  `json:"tenant_id,omitempty"`
	Code         string                  `json:"code"`
	DisplayName  string                  `json:"display_name"`
	CustomerID   string                  `json:"customer_id"`
	Status       SubscriptionStatus      `json:"status"`
	BillingTime  SubscriptionBillingTime `json:"billing_time"`
	TrialStartAt *time.Time              `json:"trial_start_at,omitempty"`
	TrialEndAt   *time.Time              `json:"trial_end_at,omitempty"`
	StartedAt    *time.Time              `json:"started_at,omitempty"`
	ActivatedAt  *time.Time              `json:"activated_at,omitempty"`
	CanceledAt   *time.Time              `json:"canceled_at,omitempty"`
	// CancelAt is a future timestamp at which the billing cycle should
	// transition the subscription to canceled. Distinct from CanceledAt
	// (past-tense, set only when the cancel has fired). Nil means no
	// timestamp-based schedule.
	CancelAt *time.Time `json:"cancel_at,omitempty"`
	// CancelAtPeriodEnd is the soft-cancel flag. When true and the cycle
	// scan observes effectiveNow >= CurrentBillingPeriodEnd, the engine
	// transitions the sub to canceled and skips the next invoice. Setting
	// false before the boundary fires undoes the schedule.
	CancelAtPeriodEnd bool `json:"cancel_at_period_end"`
	// PauseCollection holds the Stripe-parity collection-pause state. When
	// non-nil, the cycle still advances but the engine generates the
	// invoice as draft and skips finalize/charge/dunning. Distinct from
	// Status=paused, which is the hard freeze (sub excluded from
	// GetDueBilling entirely). Nil means collection is running normally.
	PauseCollection *PauseCollection `json:"pause_collection,omitempty"`
	// BillingThresholds holds the Stripe-parity hard-cap config. When
	// non-nil, the threshold scan tick computes the in-cycle running
	// totals and fires an early finalize once any configured cap is
	// crossed. Nil means no threshold; the cycle scan is the only
	// invoice-emitting path.
	BillingThresholds         *BillingThresholds `json:"billing_thresholds,omitempty"`
	CurrentBillingPeriodStart *time.Time         `json:"current_billing_period_start,omitempty"`
	CurrentBillingPeriodEnd   *time.Time         `json:"current_billing_period_end,omitempty"`
	NextBillingAt             *time.Time         `json:"next_billing_at,omitempty"`
	UsageCapUnits             *int64             `json:"usage_cap_units,omitempty"` // Max usage units per billing period (nil = unlimited)
	OverageAction             string             `json:"overage_action,omitempty"`  // "block" or "charge" (default: charge)
	TestClockID               string             `json:"test_clock_id,omitempty"`   // Test mode only — attached simulator clock
	CreatedAt                 time.Time          `json:"created_at"`
	UpdatedAt                 time.Time          `json:"updated_at"`

	// Items is populated by store reads that hydrate the subscription with
	// its current priced lines. Writes through Store.Create require a
	// non-empty Items slice; runtime lookups (billing engine, coupon apply)
	// iterate this. A subscription without items is not a valid state.
	Items []SubscriptionItem `json:"items,omitempty"`
}
