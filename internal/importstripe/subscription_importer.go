package importstripe

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/stripe/stripe-go/v82"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/subscription"
)

// SubscriptionStore is the narrow surface the subscription importer needs
// for persisting subscriptions. The importer uses Store.Create directly —
// not subscription.Service.Create — because the service applies state-
// machine logic (defaulted billing periods, derived statuses from
// TrialDays/StartNow flags) that would clobber the verbatim Stripe
// values we want to preserve. Store.Create accepts the row as-is so the
// imported sub matches what Stripe says it is.
//
// ScheduleCancellation patches cancel_at + cancel_at_period_end after
// Create — these aren't part of Store.Create's INSERT but are persisted
// via this side path. Idempotent.
//
// FindByCode is a narrow lookup helper the importer needs for idempotency.
// Phase 2 expects Store implementations to expose List(filter{Code:...})
// or equivalent; the helper interface keeps the dependency narrow.
type SubscriptionStore interface {
	Create(ctx context.Context, tenantID string, sub domain.Subscription) (domain.Subscription, error)
	ScheduleCancellation(ctx context.Context, tenantID, id string, cancelAt *time.Time, cancelAtPeriodEnd bool) (domain.Subscription, error)
	List(ctx context.Context, filter subscription.ListFilter) ([]domain.Subscription, int, error)
}

// SubscriptionCustomerLookup finds a Velox customer by Stripe `cus_...` id.
// Reused from Phase 0's CustomerLookup (the importer holds a reference to
// the customer.Store directly).
type SubscriptionCustomerLookup interface {
	GetByExternalID(ctx context.Context, tenantID, externalID string) (domain.Customer, error)
}

// SubscriptionRuleLookup is the narrow surface for finding a rating rule by
// its Stripe price ID (= rule_key). Used to ensure prices have been
// imported first; without a matching rule, the price→plan link can't be
// resolved.
type SubscriptionRuleLookup interface {
	GetLatestRuleByKey(ctx context.Context, tenantID, ruleKey string) (domain.RatingRuleVersion, error)
}

// SubscriptionImporter drives the per-row outcome logic for Phase 2.
// Depends on customers AND prices having been imported first — looks up
// the Velox customer (by Stripe `cus_...` id) and the Velox plan (via the
// price's product link) and rejects rows where either is missing.
type SubscriptionImporter struct {
	Source         Source
	Store          SubscriptionStore
	CustomerLookup SubscriptionCustomerLookup
	RuleLookup     SubscriptionRuleLookup
	PlanLookup     PlanLookup
	Report         *Report
	TenantID       string
	Livemode       bool
	DryRun         bool
}

// Run iterates every Stripe subscription the Source yields, applies the
// outcome-decision logic, and writes one report row per subscription.
func (si *SubscriptionImporter) Run(ctx context.Context) error {
	if si.Source == nil {
		return errors.New("importstripe: nil Source")
	}
	if si.Report == nil {
		return errors.New("importstripe: nil Report")
	}
	if si.TenantID == "" {
		return errors.New("importstripe: empty TenantID")
	}
	return si.Source.IterateSubscriptions(ctx, func(sub *stripe.Subscription) error {
		row := si.processOne(ctx, sub)
		if err := si.Report.Write(row); err != nil {
			return fmt.Errorf("write report row: %w", err)
		}
		return nil
	})
}

func (si *SubscriptionImporter) processOne(ctx context.Context, sub *stripe.Subscription) Row {
	if sub == nil {
		return Row{Resource: ResourceSubscription, Action: ActionError, Detail: "nil stripe subscription"}
	}

	if sub.Livemode != si.Livemode {
		return Row{
			StripeID: sub.ID,
			Resource: ResourceSubscription,
			Action:   ActionError,
			Detail: fmt.Sprintf(
				"livemode mismatch: stripe=%t importer=%t (use --livemode-default=%t to override)",
				sub.Livemode, si.Livemode, sub.Livemode),
		}
	}

	mapped, err := mapSubscription(sub)
	if err != nil {
		return Row{StripeID: sub.ID, Resource: ResourceSubscription, Action: ActionError, Detail: err.Error()}
	}

	// Customer lookup. The Stripe sub references a `cus_...` ID which must
	// already be imported as a Velox customer.
	customer, err := si.CustomerLookup.GetByExternalID(ctx, si.TenantID, mapped.CustomerExternalID)
	if err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			return Row{
				StripeID: sub.ID,
				Resource: ResourceSubscription,
				Action:   ActionError,
				Detail:   fmt.Sprintf("customer with external_id %q not found; run --resource=customers first", mapped.CustomerExternalID),
			}
		}
		return Row{
			StripeID: sub.ID,
			Resource: ResourceSubscription,
			Action:   ActionError,
			Detail:   fmt.Sprintf("customer lookup failed: %v", err),
		}
	}
	mapped.Subscription.CustomerID = customer.ID

	// Rule lookup (existence check — confirms `--resource=prices` was run).
	if _, err := si.RuleLookup.GetLatestRuleByKey(ctx, si.TenantID, mapped.PriceID); err != nil {
		return Row{
			StripeID: sub.ID,
			Resource: ResourceSubscription,
			Action:   ActionError,
			Detail:   fmt.Sprintf("rating rule for price %q not found; run --resource=prices first", mapped.PriceID),
		}
	}

	// Plan lookup. The plan code = stripe price's product id (set by Phase 1
	// product import). We need the Velox plan ID to populate Items[0].PlanID.
	planCode, err := si.resolvePlanCodeFromSub(sub)
	if err != nil {
		return Row{
			StripeID: sub.ID,
			Resource: ResourceSubscription,
			Action:   ActionError,
			Detail:   err.Error(),
		}
	}
	plan, err := si.findPlanByCode(ctx, planCode)
	if err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			return Row{
				StripeID: sub.ID,
				Resource: ResourceSubscription,
				Action:   ActionError,
				Detail:   fmt.Sprintf("plan with code %q not found; run --resource=products first", planCode),
			}
		}
		return Row{
			StripeID: sub.ID,
			Resource: ResourceSubscription,
			Action:   ActionError,
			Detail:   fmt.Sprintf("plan lookup failed: %v", err),
		}
	}

	// Compose the single subscription item.
	mapped.Subscription.Items = []domain.SubscriptionItem{
		{
			PlanID:   plan.ID,
			Quantity: mapped.Quantity,
		},
	}

	// Idempotency: check if a sub with this code (= Stripe sub id) already
	// exists. UNIQUE(tenant_id, livemode, code) makes this the natural
	// dedup key — same idea as plan.code = stripe product id and
	// rating_rule.rule_key = stripe price id. No new migration needed.
	existing, found, err := si.findSubByCode(ctx, sub.ID)
	if err != nil {
		return Row{
			StripeID: sub.ID,
			Resource: ResourceSubscription,
			Action:   ActionError,
			Detail:   fmt.Sprintf("subscription lookup by code failed: %v", err),
		}
	}
	if found {
		return si.handleExisting(mapped, existing)
	}
	return si.handleInsert(ctx, mapped)
}

// resolvePlanCodeFromSub extracts the Stripe product id from the
// subscription's single item. Done here (driver-level) rather than in the
// pure mapper so the mapper stays free of Stripe-API navigation logic.
func (si *SubscriptionImporter) resolvePlanCodeFromSub(sub *stripe.Subscription) (string, error) {
	if sub.Items == nil || len(sub.Items.Data) == 0 || sub.Items.Data[0] == nil {
		return "", errors.New("stripe subscription item has no product reference")
	}
	item := sub.Items.Data[0]
	if item.Price == nil {
		return "", errors.New("stripe subscription item has no price")
	}
	if item.Price.Product != nil && strings.TrimSpace(item.Price.Product.ID) != "" {
		return item.Price.Product.ID, nil
	}
	// Some legacy fixtures use the deprecated `plan` block which carries
	// a separate Product field. Support both shapes for safety.
	if item.Plan != nil && item.Plan.Product != nil && strings.TrimSpace(item.Plan.Product.ID) != "" {
		return item.Plan.Product.ID, nil
	}
	return "", errors.New("stripe subscription item has no product reference (price.product / plan.product both empty)")
}

// findPlanByCode mirrors the price importer's helper — bounded ListPlans
// scan, no new pricing.Store method needed for a Phase-2-only caller.
func (si *SubscriptionImporter) findPlanByCode(ctx context.Context, code string) (domain.Plan, error) {
	plans, err := si.PlanLookup.ListPlans(ctx, si.TenantID)
	if err != nil {
		return domain.Plan{}, err
	}
	for _, p := range plans {
		if p.Code == code {
			return p, nil
		}
	}
	return domain.Plan{}, errs.ErrNotFound
}

// findSubByCode scans the tenant's subscriptions for one whose Code matches
// the Stripe sub ID. Like findPlanByCode it accepts the bounded ListAll
// cost rather than pushing a dedicated GetByCode method into the
// subscription store — Phase 2 is the only caller. Returns (sub, true,
// nil) on a hit, (zero, false, nil) on miss, and (zero, false, err) on
// transport error.
func (si *SubscriptionImporter) findSubByCode(ctx context.Context, code string) (domain.Subscription, bool, error) {
	// Page through subscriptions with a generous limit to avoid pagination
	// in the common small-tenant case. Phase 2 is intended for fresh
	// imports; large existing fleets are out of scope for this slice.
	const pageLimit = 100
	offset := 0
	for {
		subs, total, err := si.Store.List(ctx, subscription.ListFilter{
			TenantID: si.TenantID,
			Limit:    pageLimit,
			Offset:   offset,
		})
		if err != nil {
			return domain.Subscription{}, false, err
		}
		for _, s := range subs {
			if s.Code == code {
				return s, true, nil
			}
		}
		offset += len(subs)
		if offset >= total || len(subs) == 0 {
			return domain.Subscription{}, false, nil
		}
	}
}

func (si *SubscriptionImporter) handleInsert(ctx context.Context, mapped MappedSubscription) Row {
	row := Row{
		StripeID: mapped.Subscription.Code,
		Resource: ResourceSubscription,
		Action:   ActionInsert,
		Detail:   strings.Join(mapped.Notes, "; "),
	}
	if si.DryRun {
		return row
	}
	created, err := si.Store.Create(ctx, si.TenantID, mapped.Subscription)
	if err != nil {
		return Row{
			StripeID: mapped.Subscription.Code,
			Resource: ResourceSubscription,
			Action:   ActionError,
			Detail:   fmt.Sprintf("create subscription: %v", err),
		}
	}
	row.VeloxID = created.ID

	// cancel_at / cancel_at_period_end aren't part of Store.Create's INSERT;
	// patch them on via ScheduleCancellation if either is set. Idempotent
	// — the call is safe to make even when both are zero values, but skip
	// the round-trip when there's nothing to set.
	if (mapped.Subscription.CancelAt != nil && !mapped.Subscription.CancelAt.IsZero()) || mapped.Subscription.CancelAtPeriodEnd {
		if _, err := si.Store.ScheduleCancellation(ctx, si.TenantID, created.ID,
			mapped.Subscription.CancelAt, mapped.Subscription.CancelAtPeriodEnd); err != nil {
			row.Detail = appendNote(row.Detail,
				fmt.Sprintf("subscription created but cancel-schedule patch failed: %v", err))
		}
	}
	return row
}

func (si *SubscriptionImporter) handleExisting(mapped MappedSubscription, existing domain.Subscription) Row {
	diff := diffSubscription(mapped, existing)
	if diff == "" {
		return Row{
			StripeID: mapped.Subscription.Code,
			Resource: ResourceSubscription,
			Action:   ActionSkipEquivalent,
			VeloxID:  existing.ID,
			Detail:   strings.Join(mapped.Notes, "; "),
		}
	}
	notes := append([]string{diff}, mapped.Notes...)
	return Row{
		StripeID: mapped.Subscription.Code,
		Resource: ResourceSubscription,
		Action:   ActionSkipDivergent,
		VeloxID:  existing.ID,
		Detail:   strings.Join(notes, "; "),
	}
}

// diffSubscription compares the subscription-level fields the import owns
// (status, customer_id, display_name, billing time, trial window, cancel
// schedule, current period) against the persisted Velox row. Stable order
// for deterministic CSV output.
//
// Items aren't diffed at the unit-test level: Phase 2 imports a single
// item and the importer doesn't mutate items on existing subs. If a Stripe
// sub gets a different plan post-import, that's a divergence the operator
// must resolve manually (creating a Velox subscription with the new plan
// and deactivating the old one is a different lifecycle than what the
// importer should apply).
func diffSubscription(mapped MappedSubscription, existing domain.Subscription) string {
	var diffs []string
	add := func(field, want, got string) {
		if want != got {
			diffs = append(diffs, fmt.Sprintf("%s stripe=%q velox=%q", field, want, got))
		}
	}
	add("status", string(mapped.Subscription.Status), string(existing.Status))
	add("customer_id", mapped.Subscription.CustomerID, existing.CustomerID)
	add("display_name", mapped.Subscription.DisplayName, existing.DisplayName)
	addTime := func(field string, want, got *time.Time) {
		l, r := timeStr(want), timeStr(got)
		if l != r {
			diffs = append(diffs, fmt.Sprintf("%s stripe=%q velox=%q", field, l, r))
		}
	}
	addTime("trial_start_at", mapped.Subscription.TrialStartAt, existing.TrialStartAt)
	addTime("trial_end_at", mapped.Subscription.TrialEndAt, existing.TrialEndAt)
	addTime("canceled_at", mapped.Subscription.CanceledAt, existing.CanceledAt)
	addTime("cancel_at", mapped.Subscription.CancelAt, existing.CancelAt)
	if mapped.Subscription.CancelAtPeriodEnd != existing.CancelAtPeriodEnd {
		diffs = append(diffs, fmt.Sprintf("cancel_at_period_end stripe=%t velox=%t",
			mapped.Subscription.CancelAtPeriodEnd, existing.CancelAtPeriodEnd))
	}
	addTime("current_billing_period_start", mapped.Subscription.CurrentBillingPeriodStart, existing.CurrentBillingPeriodStart)
	addTime("current_billing_period_end", mapped.Subscription.CurrentBillingPeriodEnd, existing.CurrentBillingPeriodEnd)
	sort.Strings(diffs)
	return strings.Join(diffs, ", ")
}

// timeStr formats a *time.Time for diff display. Empty for nil so
// "stripe=\"\" velox=\"2025-01-01...\"" reads naturally. RFC3339 is the
// project's default human-readable timestamp format.
func timeStr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
