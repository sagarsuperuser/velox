package importstripe

import (
	"errors"
	"fmt"
	"strings"

	"github.com/stripe/stripe-go/v82"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// MappedProduct is the result of translating a Stripe product into Velox
// shape. Phase 1 maps each Stripe Product 1:1 onto a `domain.Plan`. Pricing
// data (currency, billing_interval, base_amount_cents) is intentionally NOT
// derived from the product's default_price during this step — the price
// importer is the source of truth for those fields and runs as a separate
// pass. Plans land with USD/monthly/0 placeholders + a note in the report
// so operators understand the multi-step nature of the import.
type MappedProduct struct {
	Plan domain.Plan
	// Notes accumulates non-fatal mapping observations surfaced in the CSV
	// report — e.g. defaulted display name, defaulted pricing fields. Empty
	// for fully-populated rows.
	Notes []string
}

// ErrMapEmptyProductID signals a Stripe product with no ID — almost always a
// malformed fixture. Production Stripe never returns one. Treated as an
// error outcome rather than panicking so the run continues.
var ErrMapEmptyProductID = errors.New("stripe product has empty id")

// mapProduct translates a *stripe.Product into a Velox plan domain shape.
// Pure: no DB / no Stripe API. Caller cross-checks livemode against the
// importer's configured value before invoking, so disagreements never reach
// this function.
//
// Lossy mappings explicitly noted in the report:
//   - Stripe `Product.Type` (good/service): Velox plans only model services,
//     so `good`-typed products are flagged. Phase 1 still imports them — the
//     downstream subscription importer will refuse to attach a sub to a good
//     plan.
//   - Stripe `Product.MarketingFeatures`, `Product.Images`, `Product.URL`,
//     `Product.UnitLabel`: not modeled in Velox v1; surfaced as a single
//     "fields not imported" note.
//   - Pricing fields (currency / interval / amount): defaulted; price
//     importer fills them in.
func mapProduct(prod *stripe.Product) (MappedProduct, error) {
	if prod == nil {
		return MappedProduct{}, errors.New("nil stripe product")
	}
	if strings.TrimSpace(prod.ID) == "" {
		return MappedProduct{}, ErrMapEmptyProductID
	}

	var notes []string

	name := strings.TrimSpace(prod.Name)
	if name == "" {
		// Velox plans require a non-empty name. Synthesize a stable
		// placeholder; operators can edit post-import via the dashboard.
		name = "(no name)"
		notes = append(notes, "stripe product has no name; defaulted plan name to '(no name)'")
	}

	plan := domain.Plan{
		// Code = Stripe product ID. The Stripe ID format (`prod_<base32>`)
		// satisfies Velox's slug pattern (alphanumeric + underscore + hyphen),
		// so no escaping needed. Reusing the ID as the code keeps the
		// importer idempotent without a dedicated external_id column on
		// plans, and gives operators a recognisable identifier in the UI.
		Code:        prod.ID,
		Name:        name,
		Description: strings.TrimSpace(prod.Description),
		// Pricing defaults — overwritten by the price importer, never by
		// downstream Stripe state. Plans require non-empty currency at
		// the schema layer; USD is the safe default. Operators can change
		// currency manually if no recurring price is ever imported.
		Currency:        "USD",
		BillingInterval: domain.BillingMonthly,
		Status:          domain.PlanActive,
		BaseAmountCents: 0,
		MeterIDs:        []string{},
	}
	notes = append(notes, "pricing fields (currency, billing_interval, base_amount_cents) defaulted; the price importer fills them in")

	if prod.Type == stripe.ProductTypeGood {
		notes = append(notes, "stripe product type=good is unusual for billing; subscriptions won't attach")
	}

	if prod.TaxCode != nil && strings.TrimSpace(prod.TaxCode.ID) != "" {
		// Velox plan.tax_code accepts Stripe tax code IDs verbatim
		// (validated by domain.ValidateStripeTaxCode at write time).
		plan.TaxCode = strings.TrimSpace(prod.TaxCode.ID)
	}

	// Surface lossy fields as a single combined note — keeps the CSV report
	// row width manageable while still flagging that auditing post-import
	// may be needed.
	var lossy []string
	if len(prod.MarketingFeatures) > 0 {
		lossy = append(lossy, fmt.Sprintf("marketing_features (%d)", len(prod.MarketingFeatures)))
	}
	if len(prod.Images) > 0 {
		lossy = append(lossy, fmt.Sprintf("images (%d)", len(prod.Images)))
	}
	if strings.TrimSpace(prod.URL) != "" {
		lossy = append(lossy, "url")
	}
	if strings.TrimSpace(prod.UnitLabel) != "" {
		lossy = append(lossy, "unit_label")
	}
	if strings.TrimSpace(prod.StatementDescriptor) != "" {
		lossy = append(lossy, "statement_descriptor")
	}
	if len(lossy) > 0 {
		notes = append(notes, "fields not imported: "+strings.Join(lossy, ", "))
	}

	return MappedProduct{Plan: plan, Notes: notes}, nil
}
