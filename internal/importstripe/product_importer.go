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

// PlanService is the narrow surface the product importer needs from
// internal/pricing. Defined locally so tests can fake it without spinning
// up the full domain stack.
type PlanService interface {
	CreatePlan(ctx context.Context, tenantID string, in pricing.CreatePlanInput) (domain.Plan, error)
	UpdatePlan(ctx context.Context, tenantID, id string, in pricing.CreatePlanInput) (domain.Plan, error)
}

// PlanLookup finds a Velox plan by its Velox `code`. Phase 1 reuses the plan
// code field as the import-stable key (set to the Stripe Product ID) — see
// the design notes on mapper_product.go for the rationale.
type PlanLookup interface {
	ListPlans(ctx context.Context, tenantID string) ([]domain.Plan, error)
}

// ProductImporter drives the per-row outcome logic for Phase 1 product
// import. Mirrors CustomerImporter's shape exactly — see customer_importer.go
// for the per-method rationale.
type ProductImporter struct {
	Source   Source
	Service  PlanService
	Lookup   PlanLookup
	Report   *Report
	TenantID string
	Livemode bool
	DryRun   bool
}

// Run iterates every Stripe product the Source yields, applies the
// outcome-decision logic, and writes one report row per product.
func (pi *ProductImporter) Run(ctx context.Context) error {
	if pi.Source == nil {
		return errors.New("importstripe: nil Source")
	}
	if pi.Report == nil {
		return errors.New("importstripe: nil Report")
	}
	if pi.TenantID == "" {
		return errors.New("importstripe: empty TenantID")
	}
	return pi.Source.IterateProducts(ctx, func(prod *stripe.Product) error {
		row := pi.processOne(ctx, prod)
		if err := pi.Report.Write(row); err != nil {
			return fmt.Errorf("write report row: %w", err)
		}
		return nil
	})
}

func (pi *ProductImporter) processOne(ctx context.Context, prod *stripe.Product) Row {
	if prod == nil {
		return Row{Resource: ResourceProduct, Action: ActionError, Detail: "nil stripe product"}
	}

	if prod.Livemode != pi.Livemode {
		return Row{
			StripeID: prod.ID,
			Resource: ResourceProduct,
			Action:   ActionError,
			Detail: fmt.Sprintf(
				"livemode mismatch: stripe=%t importer=%t (use --livemode-default=%t to override)",
				prod.Livemode, pi.Livemode, prod.Livemode),
		}
	}

	mapped, err := mapProduct(prod)
	if err != nil {
		return Row{StripeID: prod.ID, Resource: ResourceProduct, Action: ActionError, Detail: err.Error()}
	}

	existing, err := pi.findPlanByCode(ctx, mapped.Plan.Code)
	switch {
	case err == nil:
		return pi.handleExisting(mapped, existing)
	case errors.Is(err, errs.ErrNotFound):
		return pi.handleInsert(ctx, mapped)
	default:
		return Row{
			StripeID: prod.ID,
			Resource: ResourceProduct,
			Action:   ActionError,
			Detail:   fmt.Sprintf("lookup by code failed: %v", err),
		}
	}
}

// findPlanByCode is the importer's internal lookup. It avoids adding a
// dedicated GetPlanByCode method to internal/pricing because (a) Phase 1
// is the only caller that needs it, (b) plan lists are bounded (LIMIT 500
// in ListPlans), and (c) keeping the Lookup interface narrow makes test
// fakes simpler. If a future caller needs the same query, promote it to
// the pricing.Store interface then.
func (pi *ProductImporter) findPlanByCode(ctx context.Context, code string) (domain.Plan, error) {
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

func (pi *ProductImporter) handleInsert(ctx context.Context, mapped MappedProduct) Row {
	row := Row{
		StripeID: mapped.Plan.Code,
		Resource: ResourceProduct,
		Action:   ActionInsert,
		Detail:   strings.Join(mapped.Notes, "; "),
	}
	if pi.DryRun {
		return row
	}
	created, err := pi.Service.CreatePlan(ctx, pi.TenantID, pricing.CreatePlanInput{
		Code:            mapped.Plan.Code,
		Name:            mapped.Plan.Name,
		Description:     mapped.Plan.Description,
		Currency:        mapped.Plan.Currency,
		BillingInterval: mapped.Plan.BillingInterval,
		BaseAmountCents: mapped.Plan.BaseAmountCents,
		MeterIDs:        mapped.Plan.MeterIDs,
		Status:          string(mapped.Plan.Status),
		TaxCode:         mapped.Plan.TaxCode,
	})
	if err != nil {
		return Row{
			StripeID: mapped.Plan.Code,
			Resource: ResourceProduct,
			Action:   ActionError,
			Detail:   fmt.Sprintf("create plan: %v", err),
		}
	}
	row.VeloxID = created.ID
	return row
}

func (pi *ProductImporter) handleExisting(mapped MappedProduct, existing domain.Plan) Row {
	diff := diffProduct(mapped, existing)
	if diff == "" {
		return Row{
			StripeID: mapped.Plan.Code,
			Resource: ResourceProduct,
			Action:   ActionSkipEquivalent,
			VeloxID:  existing.ID,
			Detail:   strings.Join(mapped.Notes, "; "),
		}
	}
	notes := append([]string{diff}, mapped.Notes...)
	return Row{
		StripeID: mapped.Plan.Code,
		Resource: ResourceProduct,
		Action:   ActionSkipDivergent,
		VeloxID:  existing.ID,
		Detail:   strings.Join(notes, "; "),
	}
}

// diffProduct compares the plan-shape fields the product import owns
// (name, description, tax_code) against the existing Velox plan. Currency,
// billing_interval, base_amount_cents are NOT diffed here — they're owned
// by the price importer, which does its own divergence detection on the
// price→plan-update step. Stable order so the CSV diff string is
// deterministic.
func diffProduct(mapped MappedProduct, existing domain.Plan) string {
	var diffs []string
	add := func(field, want, got string) {
		if want != got {
			diffs = append(diffs, fmt.Sprintf("%s stripe=%q velox=%q", field, want, got))
		}
	}
	add("name", mapped.Plan.Name, existing.Name)
	add("description", mapped.Plan.Description, existing.Description)
	add("tax_code", mapped.Plan.TaxCode, existing.TaxCode)
	sort.Strings(diffs)
	return strings.Join(diffs, ", ")
}
