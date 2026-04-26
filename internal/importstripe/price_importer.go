package importstripe

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/stripe/stripe-go/v82"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/pricing"
)

// RatingRuleService is the narrow surface the price importer needs from
// internal/pricing for managing rating rules.
type RatingRuleService interface {
	CreateRatingRule(ctx context.Context, tenantID string, input pricing.CreateRatingRuleInput) (domain.RatingRuleVersion, error)
	GetLatestRuleByKey(ctx context.Context, tenantID, ruleKey string) (domain.RatingRuleVersion, error)
}

// PriceImporter drives the per-row outcome logic for Phase 1 price import.
// Depends on products having been imported first — looks up the Velox plan
// by code (= Stripe Product ID) and rejects rows whose plan is missing.
type PriceImporter struct {
	Source      Source
	RuleService RatingRuleService
	PlanService PlanService
	Lookup      PlanLookup
	Report      *Report
	TenantID    string
	Livemode    bool
	DryRun      bool
}

// Run iterates every Stripe price the Source yields, applies the
// outcome-decision logic, and writes one report row per price.
func (pi *PriceImporter) Run(ctx context.Context) error {
	if pi.Source == nil {
		return errors.New("importstripe: nil Source")
	}
	if pi.Report == nil {
		return errors.New("importstripe: nil Report")
	}
	if pi.TenantID == "" {
		return errors.New("importstripe: empty TenantID")
	}
	return pi.Source.IteratePrices(ctx, func(price *stripe.Price) error {
		row := pi.processOne(ctx, price)
		if err := pi.Report.Write(row); err != nil {
			return fmt.Errorf("write report row: %w", err)
		}
		return nil
	})
}

func (pi *PriceImporter) processOne(ctx context.Context, price *stripe.Price) Row {
	if price == nil {
		return Row{Resource: ResourcePrice, Action: ActionError, Detail: "nil stripe price"}
	}

	if price.Livemode != pi.Livemode {
		return Row{
			StripeID: price.ID,
			Resource: ResourcePrice,
			Action:   ActionError,
			Detail: fmt.Sprintf(
				"livemode mismatch: stripe=%t importer=%t (use --livemode-default=%t to override)",
				price.Livemode, pi.Livemode, price.Livemode),
		}
	}

	mapped, err := mapPrice(price)
	if err != nil {
		return Row{StripeID: price.ID, Resource: ResourcePrice, Action: ActionError, Detail: err.Error()}
	}

	plan, err := pi.findPlanByCode(ctx, mapped.PlanCode)
	if err != nil {
		// The most common cause: products import wasn't run first.
		// Surface a clear actionable message rather than the raw lookup
		// error.
		if errors.Is(err, errs.ErrNotFound) {
			return Row{
				StripeID: price.ID,
				Resource: ResourcePrice,
				Action:   ActionError,
				Detail:   fmt.Sprintf("plan with code %q not found; run --resource=products first", mapped.PlanCode),
			}
		}
		return Row{
			StripeID: price.ID,
			Resource: ResourcePrice,
			Action:   ActionError,
			Detail:   fmt.Sprintf("plan lookup failed: %v", err),
		}
	}

	// Idempotency: check if a rating rule with this RuleKey already exists.
	existingRule, ruleErr := pi.RuleService.GetLatestRuleByKey(ctx, pi.TenantID, mapped.RatingRule.RuleKey)
	if ruleErr == nil {
		return pi.handleExistingRule(mapped, existingRule, plan)
	}
	// GetLatestRuleByKey returns a non-typed error when nothing is found
	// (see pricing/service.go). Treat any error here as "not found" and
	// fall through to insert; if the error is something else (DB pool
	// exhausted, etc.), the insert will surface it again with proper
	// error chaining.
	return pi.handleInsert(ctx, mapped, plan)
}

func (pi *PriceImporter) findPlanByCode(ctx context.Context, code string) (domain.Plan, error) {
	plans, err := pi.Lookup.ListPlans(ctx, pi.TenantID)
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

func (pi *PriceImporter) handleInsert(ctx context.Context, mapped MappedPrice, plan domain.Plan) Row {
	row := Row{
		StripeID: mapped.RatingRule.RuleKey,
		Resource: ResourcePrice,
		Action:   ActionInsert,
		Detail:   strings.Join(mapped.Notes, "; "),
	}
	if pi.DryRun {
		row.VeloxID = plan.ID // surface the linked plan for operator visibility
		return row
	}
	rule, err := pi.RuleService.CreateRatingRule(ctx, pi.TenantID, pricing.CreateRatingRuleInput{
		RuleKey:         mapped.RatingRule.RuleKey,
		Name:            mapped.RatingRule.Name,
		Mode:            domain.PricingFlat,
		Currency:        mapped.RatingRule.Currency,
		FlatAmountCents: mapped.RatingRule.FlatAmountCents,
	})
	if err != nil {
		return Row{
			StripeID: mapped.RatingRule.RuleKey,
			Resource: ResourcePrice,
			Action:   ActionError,
			Detail:   fmt.Sprintf("create rating rule: %v", err),
		}
	}
	row.VeloxID = rule.ID

	// Also patch the plan's pricing fields. Currency + billing_interval
	// are immutable via UpdatePlan; if the plan was imported with USD/
	// monthly placeholders and the price is EUR/yearly, we surface a note
	// rather than failing — operators can recreate the plan manually if
	// the divergence matters.
	if plan.Currency != mapped.PlanUpdate.Currency {
		row.Detail = appendNote(row.Detail, fmt.Sprintf(
			"plan currency stripe=%q velox=%q (Velox does not allow mutating plan currency post-creation; recreate the plan if needed)",
			mapped.PlanUpdate.Currency, plan.Currency))
	}
	if plan.BillingInterval != mapped.PlanUpdate.BillingInterval {
		row.Detail = appendNote(row.Detail, fmt.Sprintf(
			"plan billing_interval stripe=%q velox=%q (immutable post-creation; recreate the plan if needed)",
			mapped.PlanUpdate.BillingInterval, plan.BillingInterval))
	}

	// base_amount_cents IS mutable via UpdatePlan, so we can patch it.
	if plan.BaseAmountCents != mapped.PlanUpdate.BaseAmountCents {
		_, err := pi.PlanService.UpdatePlan(ctx, pi.TenantID, plan.ID, pricing.CreatePlanInput{
			Name:            plan.Name,
			Description:     plan.Description,
			BaseAmountCents: mapped.PlanUpdate.BaseAmountCents,
			MeterIDs:        plan.MeterIDs,
			Status:          string(plan.Status),
			TaxCode:         plan.TaxCode,
		})
		if err != nil {
			row.Detail = appendNote(row.Detail, fmt.Sprintf("rating rule created but plan base price update failed: %v", err))
		} else {
			row.Detail = appendNote(row.Detail, fmt.Sprintf("plan base_amount_cents updated to %d", mapped.PlanUpdate.BaseAmountCents))
		}
	}
	return row
}

func (pi *PriceImporter) handleExistingRule(mapped MappedPrice, existing domain.RatingRuleVersion, plan domain.Plan) Row {
	diff := diffPrice(mapped, existing)
	if diff == "" {
		return Row{
			StripeID: mapped.RatingRule.RuleKey,
			Resource: ResourcePrice,
			Action:   ActionSkipEquivalent,
			VeloxID:  existing.ID,
			Detail:   strings.Join(mapped.Notes, "; "),
		}
	}
	notes := append([]string{diff}, mapped.Notes...)
	return Row{
		StripeID: mapped.RatingRule.RuleKey,
		Resource: ResourcePrice,
		Action:   ActionSkipDivergent,
		VeloxID:  existing.ID,
		Detail:   strings.Join(notes, "; "),
	}
}

// diffPrice compares the rating-rule fields the price import owns against
// the existing Velox row. Mode is fixed at flat for Phase 1 so we don't
// diff it — only currency, name, and flat_amount_cents.
func diffPrice(mapped MappedPrice, existing domain.RatingRuleVersion) string {
	var diffs []string
	add := func(field, want, got string) {
		if want != got {
			diffs = append(diffs, fmt.Sprintf("%s stripe=%q velox=%q", field, want, got))
		}
	}
	add("name", mapped.RatingRule.Name, existing.Name)
	add("currency", mapped.RatingRule.Currency, existing.Currency)
	if mapped.RatingRule.FlatAmountCents != existing.FlatAmountCents {
		diffs = append(diffs, fmt.Sprintf("flat_amount_cents stripe=%d velox=%d",
			mapped.RatingRule.FlatAmountCents, existing.FlatAmountCents))
	}
	sort.Strings(diffs)
	return strings.Join(diffs, ", ")
}

// appendNote joins a new note onto an existing detail string with the same
// "; " separator the report uses for its own notes. Avoids leading/trailing
// separators when either side is empty.
func appendNote(existing, note string) string {
	if existing == "" {
		return note
	}
	return existing + "; " + note
}
