package importstripe

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/stripe/stripe-go/v82"

	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

// CustomerService is the narrow surface the importer needs from
// internal/customer. Defined locally so tests can fake it without spinning
// up the full domain stack.
type CustomerService interface {
	Create(ctx context.Context, tenantID string, in customer.CreateInput) (domain.Customer, error)
	UpsertBillingProfile(ctx context.Context, tenantID string, bp domain.CustomerBillingProfile) (domain.CustomerBillingProfile, error)
}

// CustomerLookup finds a Velox customer by its Stripe `cus_...` external_id.
// The customer.Store interface already exposes GetByExternalID; the
// importer accepts it as a narrow interface so unit tests don't need the
// full Postgres store.
type CustomerLookup interface {
	GetByExternalID(ctx context.Context, tenantID, externalID string) (domain.Customer, error)
	GetBillingProfile(ctx context.Context, tenantID, customerID string) (domain.CustomerBillingProfile, error)
}

// CustomerImporter drives the per-row outcome logic for Phase 0. It does
// not touch Stripe directly — that's Source's job — and does not own a
// transaction; each row is written as its own short-lived tx via the
// underlying customer.Service.
type CustomerImporter struct {
	Source   Source
	Service  CustomerService
	Lookup   CustomerLookup
	Report   *Report
	TenantID string
	// Livemode is the importer's effective mode. Used as a sanity check
	// against each Stripe customer's own livemode field — disagreements
	// produce ActionError rather than silently misclassifying rows.
	Livemode bool
	// DryRun, when true, runs the full pipeline (mapping, lookup, diff)
	// but skips DB writes. Outcomes still land in the CSV report so
	// operators can inspect what would happen.
	DryRun bool
}

// Run iterates every Stripe customer the Source yields, applies the
// outcome-decision logic, and writes one report row per customer. Returns
// the first iteration-level error encountered (network, ctx cancel,
// DB pool exhaustion). Per-row mapping/DB errors are captured in the
// report and never abort the run.
func (ci *CustomerImporter) Run(ctx context.Context) error {
	if ci.Source == nil {
		return errors.New("importstripe: nil Source")
	}
	if ci.Report == nil {
		return errors.New("importstripe: nil Report")
	}
	if ci.TenantID == "" {
		return errors.New("importstripe: empty TenantID")
	}
	return ci.Source.IterateCustomers(ctx, func(cust *stripe.Customer) error {
		row := ci.processOne(ctx, cust)
		// Report.Write is best-effort — a write failure usually means the
		// output writer (file/stdout) broke, in which case continuing the
		// loop wouldn't help anyway. Surface as iteration-level error.
		if err := ci.Report.Write(row); err != nil {
			return fmt.Errorf("write report row: %w", err)
		}
		return nil
	})
}

// processOne decides the outcome for a single Stripe customer. Pure with
// respect to the report — only the returned Row is the side-effect-bearing
// half (DB writes happen here too when not DryRun). Map errors, livemode
// mismatches, and DB failures are converted to ActionError rows and the
// pipeline continues.
func (ci *CustomerImporter) processOne(ctx context.Context, cust *stripe.Customer) Row {
	if cust == nil {
		return Row{Resource: ResourceCustomer, Action: ActionError, Detail: "nil stripe customer"}
	}

	// Livemode sanity check — if Stripe says this row is live but we're
	// running in test (or vice versa), refuse to import.
	if cust.Livemode != ci.Livemode {
		return Row{
			StripeID: cust.ID,
			Resource: ResourceCustomer,
			Action:   ActionError,
			Detail: fmt.Sprintf(
				"livemode mismatch: stripe=%t importer=%t (use --livemode-default=%t to override)",
				cust.Livemode, ci.Livemode, cust.Livemode),
		}
	}

	mapped, err := mapCustomer(cust, ci.Livemode)
	if err != nil {
		return Row{StripeID: cust.ID, Resource: ResourceCustomer, Action: ActionError, Detail: err.Error()}
	}

	// Existence check by external_id (Stripe's `cus_...`).
	existing, err := ci.Lookup.GetByExternalID(ctx, ci.TenantID, mapped.Customer.ExternalID)
	switch {
	case err == nil:
		return ci.handleExisting(ctx, mapped, existing)
	case errors.Is(err, errs.ErrNotFound):
		return ci.handleInsert(ctx, mapped)
	default:
		return Row{
			StripeID: cust.ID,
			Resource: ResourceCustomer,
			Action:   ActionError,
			Detail:   fmt.Sprintf("lookup by external_id failed: %v", err),
		}
	}
}

func (ci *CustomerImporter) handleInsert(ctx context.Context, mapped MappedCustomer) Row {
	row := Row{
		StripeID: mapped.Customer.ExternalID,
		Resource: ResourceCustomer,
		Action:   ActionInsert,
		Detail:   strings.Join(mapped.Notes, "; "),
	}
	if ci.DryRun {
		return row
	}
	created, err := ci.Service.Create(ctx, ci.TenantID, customer.CreateInput{
		ExternalID:  mapped.Customer.ExternalID,
		DisplayName: mapped.Customer.DisplayName,
		Email:       mapped.Customer.Email,
	})
	if err != nil {
		return Row{
			StripeID: mapped.Customer.ExternalID,
			Resource: ResourceCustomer,
			Action:   ActionError,
			Detail:   fmt.Sprintf("create customer: %v", err),
		}
	}
	row.VeloxID = created.ID
	bp := mapped.BillingProfile
	bp.CustomerID = created.ID
	if _, err := ci.Service.UpsertBillingProfile(ctx, ci.TenantID, bp); err != nil {
		// Customer landed; profile failed. Surface the partial state — the
		// next run will see the customer and try to reconcile the profile.
		return Row{
			StripeID: mapped.Customer.ExternalID,
			Resource: ResourceCustomer,
			Action:   ActionError,
			VeloxID:  created.ID,
			Detail: fmt.Sprintf("customer created but billing profile upsert failed: %v",
				err),
		}
	}
	return row
}

func (ci *CustomerImporter) handleExisting(ctx context.Context, mapped MappedCustomer, existing domain.Customer) Row {
	// Compare the mapped fields against the persisted ones. Phase 0 reports
	// divergence but does not overwrite — that's the safe default for an
	// importer that may run on a partially-migrated tenant.
	veloxBP, _ := ci.Lookup.GetBillingProfile(ctx, ci.TenantID, existing.ID)
	diff := diffCustomer(mapped, existing, veloxBP)
	if diff == "" {
		return Row{
			StripeID: mapped.Customer.ExternalID,
			Resource: ResourceCustomer,
			Action:   ActionSkipEquivalent,
			VeloxID:  existing.ID,
			Detail:   strings.Join(mapped.Notes, "; "),
		}
	}
	notes := append([]string{diff}, mapped.Notes...)
	return Row{
		StripeID: mapped.Customer.ExternalID,
		Resource: ResourceCustomer,
		Action:   ActionSkipDivergent,
		VeloxID:  existing.ID,
		Detail:   strings.Join(notes, "; "),
	}
}

// diffCustomer returns a stable, human-readable diff string of the fields
// that differ between the mapped Stripe customer and the existing Velox row.
// Empty string means "fields are equivalent". Field-level normalization is
// the same as mapCustomer's (trim, uppercase country/currency).
func diffCustomer(mapped MappedCustomer, existing domain.Customer, existingBP domain.CustomerBillingProfile) string {
	var diffs []string

	add := func(field, want, got string) {
		if want != got {
			diffs = append(diffs, fmt.Sprintf("%s stripe=%q velox=%q", field, want, got))
		}
	}

	add("display_name", mapped.Customer.DisplayName, existing.DisplayName)
	add("email", mapped.Customer.Email, existing.Email)

	add("legal_name", mapped.BillingProfile.LegalName, existingBP.LegalName)
	add("phone", mapped.BillingProfile.Phone, existingBP.Phone)
	add("address_line1", mapped.BillingProfile.AddressLine1, existingBP.AddressLine1)
	add("address_line2", mapped.BillingProfile.AddressLine2, existingBP.AddressLine2)
	add("city", mapped.BillingProfile.City, existingBP.City)
	add("state", mapped.BillingProfile.State, existingBP.State)
	add("postal_code", mapped.BillingProfile.PostalCode, existingBP.PostalCode)
	add("country", mapped.BillingProfile.Country, existingBP.Country)
	add("currency", mapped.BillingProfile.Currency, existingBP.Currency)
	add("tax_status", string(mapped.BillingProfile.TaxStatus), string(existingBP.TaxStatus))
	add("tax_id", mapped.BillingProfile.TaxID, existingBP.TaxID)
	add("tax_id_type", mapped.BillingProfile.TaxIDType, existingBP.TaxIDType)

	// Stable order so the CSV diff string is deterministic — matters for
	// regression tests that pin the exact output.
	sort.Strings(diffs)
	return strings.Join(diffs, ", ")
}
