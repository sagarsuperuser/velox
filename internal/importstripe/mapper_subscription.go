package importstripe

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/stripe/stripe-go/v82"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// MappedSubscription is the result of translating a Stripe subscription into
// Velox shape. Phase 2 maps each Stripe Subscription 1:1 onto a
// `domain.Subscription` plus exactly one `domain.SubscriptionItem`. The
// PriceID and CustomerExternalID fields carry the Stripe-side keys the
// driver needs to look up the corresponding Velox plan + customer before
// inserting; the mapper itself is pure (no DB / no Stripe API).
type MappedSubscription struct {
	// Subscription is the row to insert. Code is set to the Stripe
	// subscription ID for round-trip lookup; CustomerID and Items[0].PlanID
	// are set by the driver after lookup. Verbatim Stripe values for
	// trial_start_at, trial_end_at, started_at, current_billing_period_*,
	// next_billing_at, canceled_at, cancel_at, cancel_at_period_end are
	// preserved here so the importer is faithful to the source state.
	Subscription domain.Subscription

	// CustomerExternalID is the Stripe `cus_...` id the driver uses to look
	// up the corresponding Velox customer. The driver fills in
	// Subscription.CustomerID after that lookup.
	CustomerExternalID string

	// PriceID is the Stripe price id of the (single) item on this sub. The
	// driver uses it to find the matching rating rule (rule_key=price_id)
	// and then the parent plan (plan.code = price.product.id) to populate
	// Subscription.Items[0].PlanID.
	PriceID string

	// Quantity captures the subscription item's quantity. Defaults to 1
	// when Stripe sends 0 (which is what some fixtures do).
	Quantity int64

	// Notes accumulates non-fatal mapping observations (lossy fields,
	// status remapping, etc.) surfaced in the CSV report.
	Notes []string
}

// Sentinel errors for unsupported Stripe subscription shapes. The driver
// converts these into ActionError rows in the CSV report so operators can
// post-process them manually without aborting the run.
var (
	// ErrMapEmptySubscriptionID — Stripe subscription with no ID. Malformed.
	ErrMapEmptySubscriptionID = errors.New("stripe subscription has empty id")

	// ErrSubscriptionUnsupportedMultiItem — Phase 2 only supports
	// subscriptions with exactly one priced item. Multi-item subs (typical
	// of Stripe's add-on / metered combo plans) need item-level mapping
	// per Velox's `subscription_items` table; deferred to a later slice
	// because it interacts with the per-item plan-change schedules.
	ErrSubscriptionUnsupportedMultiItem = errors.New("stripe subscription has multiple items; Phase 2 only imports single-item subscriptions")

	// ErrSubscriptionMissingItems — Stripe sub with no items. Malformed
	// (every billable sub has at least one item) but defensively guarded.
	ErrSubscriptionMissingItems = errors.New("stripe subscription has no items")

	// ErrSubscriptionMissingCustomer — sub has no customer reference.
	// Malformed Stripe response or detached test data.
	ErrSubscriptionMissingCustomer = errors.New("stripe subscription has no customer reference")

	// ErrSubscriptionMissingPrice — sub item has no price reference. Items
	// without a price are an unusable shape — Velox subs are priced lines.
	ErrSubscriptionMissingPrice = errors.New("stripe subscription item has no price reference")
)

// mapSubscription translates a *stripe.Subscription into Velox shape. Pure:
// no DB / no Stripe API. The driver layer handles customer + plan lookup
// (those need DB access) before inserting the resulting Subscription.
//
// Status mapping policy (Velox lacks Stripe's payment-state granularity):
//   - active, trialing, canceled, paused → direct mapping
//   - past_due → active + note (Velox tracks dunning on the invoice, not
//     the subscription, so the sub stays active while the unpaid invoice
//     accumulates dunning attempts)
//   - unpaid, incomplete, incomplete_expired → canceled + note (these are
//     effectively terminal in Stripe — no further invoices will be billed
//     — so we mirror them as canceled with the original timestamp)
//
// Lossy mappings noted in the report:
//   - discounts (Velox coupon system is independent; operators must reapply)
//   - schedule (Velox doesn't model SubscriptionSchedule's phase pipeline)
//   - default_tax_rates (Velox tax handling is per-tenant via the tax
//     provider, not per-subscription rate IDs)
//   - pause_collection (Velox supports keep_as_draft only; mark_uncollectible
//     and void are dropped)
//   - billing_cycle_anchor (preserved verbatim into current_billing_period_*
//     where applicable; the standalone anchor field has no Velox column)
func mapSubscription(sub *stripe.Subscription) (MappedSubscription, error) {
	if sub == nil {
		return MappedSubscription{}, errors.New("nil stripe subscription")
	}
	if strings.TrimSpace(sub.ID) == "" {
		return MappedSubscription{}, ErrMapEmptySubscriptionID
	}

	if sub.Customer == nil || strings.TrimSpace(sub.Customer.ID) == "" {
		return MappedSubscription{}, ErrSubscriptionMissingCustomer
	}

	if sub.Items == nil || len(sub.Items.Data) == 0 {
		return MappedSubscription{}, ErrSubscriptionMissingItems
	}
	if len(sub.Items.Data) > 1 {
		return MappedSubscription{}, ErrSubscriptionUnsupportedMultiItem
	}

	item := sub.Items.Data[0]
	if item == nil {
		return MappedSubscription{}, ErrSubscriptionMissingItems
	}
	if item.Price == nil || strings.TrimSpace(item.Price.ID) == "" {
		return MappedSubscription{}, ErrSubscriptionMissingPrice
	}

	var notes []string

	// Status mapping. Velox status enum is narrower than Stripe's; document
	// each remap in the CSV note so operators understand the divergence.
	veloxStatus, statusNote := mapSubscriptionStatus(sub.Status)
	if statusNote != "" {
		notes = append(notes, statusNote)
	}

	// Build the subscription row. Code = stripe sub id for idempotent
	// round-trip lookup (UNIQUE(tenant_id, livemode, code) is the natural
	// dedup key — no migration needed).
	veloxSub := domain.Subscription{
		Code: sub.ID,
		// DisplayName falls back to the sub ID; Velox requires non-empty,
		// Stripe has no equivalent. Operators can edit post-import.
		DisplayName: subscriptionDisplayName(sub),
		Status:      veloxStatus,
		// Anniversary is the safe default — Stripe billing cycles always
		// anchor on the subscription's own start, not a calendar boundary.
		BillingTime: domain.BillingTimeAnniversary,
	}

	// Verbatim time fields from Stripe.
	if sub.TrialStart > 0 {
		t := time.Unix(sub.TrialStart, 0).UTC()
		veloxSub.TrialStartAt = &t
	}
	if sub.TrialEnd > 0 {
		t := time.Unix(sub.TrialEnd, 0).UTC()
		veloxSub.TrialEndAt = &t
	}
	if sub.StartDate > 0 {
		t := time.Unix(sub.StartDate, 0).UTC()
		veloxSub.StartedAt = &t
	} else if sub.Created > 0 {
		// Some older subs have no StartDate; fall back to Created.
		t := time.Unix(sub.Created, 0).UTC()
		veloxSub.StartedAt = &t
	}
	// CanceledAt: only stamp if Stripe has it (status canceled/unpaid/etc.).
	if sub.CanceledAt > 0 {
		t := time.Unix(sub.CanceledAt, 0).UTC()
		veloxSub.CanceledAt = &t
	}

	// ActivatedAt: derived. Stripe doesn't have a direct field; use StartDate
	// as a reasonable proxy when status is active or post-trial. This keeps
	// the audit trail populated for downstream Velox features (e.g., MRR
	// dashboards filtering on activated_at).
	if veloxStatus == domain.SubscriptionActive && veloxSub.StartedAt != nil {
		t := *veloxSub.StartedAt
		veloxSub.ActivatedAt = &t
	}

	// Future cancel intent. Stripe carries this on the row even after the
	// schedule fires, so we only honor it on non-terminal statuses.
	if sub.CancelAt > 0 && veloxStatus != domain.SubscriptionCanceled {
		t := time.Unix(sub.CancelAt, 0).UTC()
		veloxSub.CancelAt = &t
	}
	if sub.CancelAtPeriodEnd && veloxStatus != domain.SubscriptionCanceled {
		veloxSub.CancelAtPeriodEnd = true
	}

	// Current billing period — sourced from the item (Stripe v82 moved this
	// from sub-level to item-level). Falls back to BillingCycleAnchor when
	// item-level period is unset (rare; some incomplete subs).
	if item.CurrentPeriodStart > 0 {
		t := time.Unix(item.CurrentPeriodStart, 0).UTC()
		veloxSub.CurrentBillingPeriodStart = &t
	} else if sub.BillingCycleAnchor > 0 {
		t := time.Unix(sub.BillingCycleAnchor, 0).UTC()
		veloxSub.CurrentBillingPeriodStart = &t
	}
	if item.CurrentPeriodEnd > 0 {
		t := time.Unix(item.CurrentPeriodEnd, 0).UTC()
		veloxSub.CurrentBillingPeriodEnd = &t
		// next_billing_at = period_end is the convention for in-cycle
		// active subs; the engine uses this for the GetDueBilling query.
		nb := t
		veloxSub.NextBillingAt = &nb
	}

	// Lossy-field notes — surface to operators for post-import audit.
	if len(sub.Discounts) > 0 {
		notes = append(notes, fmt.Sprintf("stripe subscription has %d discount(s) — Velox coupons must be reapplied manually after import", len(sub.Discounts)))
	}
	if sub.Schedule != nil && strings.TrimSpace(sub.Schedule.ID) != "" {
		notes = append(notes, fmt.Sprintf("stripe subscription is attached to schedule %s — Velox doesn't model phase pipelines; future schedule transitions won't fire", sub.Schedule.ID))
	}
	if len(sub.DefaultTaxRates) > 0 {
		notes = append(notes, fmt.Sprintf("stripe subscription default_tax_rates (%d) ignored — Velox tax handling is per-tenant via the configured tax provider", len(sub.DefaultTaxRates)))
	}
	if sub.PauseCollection != nil {
		notes = append(notes, fmt.Sprintf("stripe pause_collection (behavior=%s) ignored — Velox supports keep_as_draft only and operators can reapply via API", sub.PauseCollection.Behavior))
	}
	if sub.BillingCycleAnchor > 0 {
		notes = append(notes, "stripe billing_cycle_anchor preserved as current_billing_period_start when item-level period is unset")
	}

	quantity := item.Quantity
	if quantity <= 0 {
		quantity = 1
	}

	return MappedSubscription{
		Subscription:       veloxSub,
		CustomerExternalID: sub.Customer.ID,
		PriceID:            item.Price.ID,
		Quantity:           quantity,
		Notes:              notes,
	}, nil
}

// mapSubscriptionStatus translates Stripe's status enum onto Velox's. Returns
// the Velox status and an optional note string describing any non-trivial
// mapping (so the CSV report makes the divergence visible to operators).
func mapSubscriptionStatus(s stripe.SubscriptionStatus) (domain.SubscriptionStatus, string) {
	switch s {
	case stripe.SubscriptionStatusActive:
		return domain.SubscriptionActive, ""
	case stripe.SubscriptionStatusTrialing:
		return domain.SubscriptionTrialing, ""
	case stripe.SubscriptionStatusCanceled:
		return domain.SubscriptionCanceled, ""
	case stripe.SubscriptionStatusPaused:
		// Stripe `paused` = trial-without-payment-method. Velox `paused` is
		// the hard-pause that excludes from billing. Same shape, same
		// semantics — direct map.
		return domain.SubscriptionPaused, ""
	case stripe.SubscriptionStatusPastDue:
		// Velox doesn't model past_due on the subscription — dunning is
		// tracked on the invoice. Keep the sub active so the engine still
		// processes the row; the unpaid invoice carries the failure state.
		return domain.SubscriptionActive, "stripe status=past_due mapped to velox=active (dunning is tracked on the unpaid invoice, not the subscription)"
	case stripe.SubscriptionStatusUnpaid:
		// Terminal in Stripe — no further invoices will succeed. Closest
		// Velox equivalent is canceled.
		return domain.SubscriptionCanceled, "stripe status=unpaid mapped to velox=canceled (Stripe stops attempting invoices for unpaid subs)"
	case stripe.SubscriptionStatusIncomplete:
		// Initial payment failed; sub never activated. Velox has no
		// equivalent transient state — map to canceled to avoid creating
		// a row the engine would never bill correctly.
		return domain.SubscriptionCanceled, "stripe status=incomplete mapped to velox=canceled (initial payment failed; the subscription never activated)"
	case stripe.SubscriptionStatusIncompleteExpired:
		// Terminal failed-init. Same target as incomplete with a different
		// note for traceability.
		return domain.SubscriptionCanceled, "stripe status=incomplete_expired mapped to velox=canceled (initial payment never succeeded within Stripe's 23-hour window)"
	default:
		// Future Stripe statuses (or malformed). Map to canceled defensively
		// rather than panicking; surface unknown for the operator to audit.
		return domain.SubscriptionCanceled, fmt.Sprintf("unknown stripe status %q mapped to velox=canceled", s)
	}
}

// subscriptionDisplayName picks the most informative human-readable label
// available on the Stripe subscription. Falls back to the sub ID — Velox
// requires non-empty display_name.
func subscriptionDisplayName(sub *stripe.Subscription) string {
	if d := strings.TrimSpace(sub.Description); d != "" {
		return d
	}
	// Stripe doesn't put a display name on subscriptions natively; the ID
	// is the operator-recognisable handle.
	return sub.ID
}
