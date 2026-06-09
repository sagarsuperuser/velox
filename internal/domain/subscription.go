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

// LoadLocationOrUTC resolves an IANA timezone name to a *time.Location,
// falling back to UTC on empty/invalid input — the shared tenant-TZ resolution
// used at render boundaries (PDF) and anywhere a tenant_settings.Timezone needs
// turning into a Location for ADR-050 calendar math.
func LoadLocationOrUTC(name string) *time.Location {
	if name == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

// InclusiveDisplayEnd converts a half-open period_end (the EXCLUSIVE boundary,
// = the next period's start) into the last calendar day FULLY covered by the
// period, in the tenant timezone `loc` — the industry-standard inclusive
// display end ("Jun 1 – Jun 30", not the exclusive "Jun 1 – Jul 1"). Storage
// stays half-open; this is render-only.
//
// It snaps the exclusive end to civil midnight in `loc`, then steps back ONE
// CALENDAR day. The calendar step (not a 24h instant subtraction) is essential:
// 24h-before an instant at a DST boundary, or a non-midnight end, lands on the
// wrong civil date — the same off-by-one class as ADR-050. loc=nil → UTC.
// Returns a loc-located civil-midnight time; format it with loc-reading layouts
// (e.g. "Jan 2, 2006") — never via UTC fields.
func InclusiveDisplayEnd(periodEnd time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	local := periodEnd.In(loc)
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	return midnight.AddDate(0, 0, -1)
}

// FormatInclusivePeriod renders the human period string
// "<start> – <inclusiveEnd>" using inclusive display dates in loc — the single
// backend-authored value every render surface (PDF, hosted, dashboard, list)
// shows verbatim, so the period cannot drift across the Go and TS runtimes (and
// the subtle ADR-050 civil-day math lives in exactly one place). Both ends are
// rendered date-only in the tenant timezone. Returns "" when start == end (a
// one-off / no-period invoice — Stripe/Chargebee/Lago all omit the period
// there) so callers show no period row. Clamps the inclusive end to >= the
// start's civil day so a sub-day stub never renders an inverted range.
func FormatInclusivePeriod(start, end time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	if start.Equal(end) {
		return ""
	}
	startLocal := start.In(loc)
	startCivil := time.Date(startLocal.Year(), startLocal.Month(), startLocal.Day(), 0, 0, 0, 0, loc)
	inc := InclusiveDisplayEnd(end, loc)
	if inc.Before(startCivil) {
		inc = startCivil
	}
	const layout = "Jan 2, 2006"
	return startCivil.Format(layout) + " – " + inc.Format(layout)
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
		// Anniversary yearly: advance in tenant TZ so the anchor day is
		// preserved in the operator's calendar, not the value's ambient
		// Location (ADR-050). Pre-fix this did a bare UTC AddDate, which
		// drifted the boundary by a day for any offset-TZ tenant whose
		// anchor instant maps to a different UTC calendar date.
		return addIntervalIn(periodEnd, interval, loc)
	}
	if billingTime == BillingTimeCalendar {
		// Snap to the 1st of periodEnd's month in tenant TZ, THEN add a
		// month — so a day-29/30/31 anchor never overflows a short month.
		// Pre-fix this added the month first (Jan 31 + 1mo = Mar 3 in Go's
		// overflow), then snapped to the 1st of the *result's* month →
		// Mar 1, silently skipping February (ADR-050 Root C). Adding the
		// month in `loc` also keeps the boundary in the tenant's zone.
		return addIntervalIn(BeginningOfMonthIn(periodEnd, loc), BillingMonthly, loc)
	}
	// Anniversary monthly: preserve day-of-month, advanced in tenant TZ.
	return addIntervalIn(periodEnd, interval, loc)
}

// addIntervalIn advances `t` by one billing interval (+1 month, or +1 year for
// yearly), performing the calendar add in `loc` so the result is anchored to
// the tenant's timezone rather than `t`'s ambient Location or the host
// time.Local (ADR-050). Returns a UTC instant. loc=nil falls back to UTC.
//
// This is the single fix for the timezone date-math defect class: every
// month/year advance MUST go through here (or NextBillingPeriodEnd, which
// does), so the result no longer varies with whether the input time.Time was
// freshly built in UTC or DB-scanned as time.Local. Day-grade adds
// (AddDate(0,0,N) for trial/due dates) are TZ-invariant and need not route here.
func addIntervalIn(t time.Time, interval BillingInterval, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	local := t.In(loc)
	if interval == BillingYearly {
		return local.AddDate(1, 0, 0).UTC()
	}
	return local.AddDate(0, 1, 0).UTC()
}

// AddBillingInterval returns t advanced by exactly one billing interval —
// the anniversary advance: +1 year for yearly, otherwise +1 month. It is the
// SINGLE source of truth for "one full cycle from an anchor".
//
// This is the denominator proration MUST divide by — the FULL cycle length,
// NOT the current period length. On a stub/partial period (mid-cycle signup,
// e.g. a 14-day period of a 30-day monthly cycle) the current period is
// shorter than a cycle, so dividing by it over-charges upgrades and
// over-credits downgrades. The engine's day-1 stub base fee is prorated against
// this same full cycle (segDays/fullCycleDays), and the subscription handler's
// immediate plan-change proration now derives its denominator from here too —
// so the two paths can never disagree (the defect class that produced the
// stub-period proration mischarge).
//
// Deliberately calendar-agnostic. For the calendar-snapping cycle-CLOSE
// advance (which re-anchors to the 1st of the next month in tenant TZ), use
// NextBillingPeriodEnd instead.
//
// `loc` is the tenant's billing timezone: the advance is computed in `loc`
// (ADR-050) so the full-cycle length is independent of whether `t` was built
// in UTC or DB-scanned as time.Local. loc=nil falls back to UTC. Pre-fix this
// took no loc and did a bare AddDate on `t`'s ambient Location, making the
// proration denominator host-TZ-dependent (30 vs 31 for an offset-TZ tenant).
func AddBillingInterval(t time.Time, interval BillingInterval, loc *time.Location) time.Time {
	return addIntervalIn(t, interval, loc)
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
