package importstripe

import (
	"errors"
	"fmt"
	"strings"

	"github.com/stripe/stripe-go/v82"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// MappedCustomer is the result of translating a Stripe customer into Velox
// shapes. The two halves correspond to Velox's normalized model:
// `domain.Customer` (identity + email) and `domain.CustomerBillingProfile`
// (legal/address/tax). Phase 0 writes both atomically per row.
type MappedCustomer struct {
	Customer       domain.Customer
	BillingProfile domain.CustomerBillingProfile
	// Notes accumulates non-fatal mapping observations (e.g. "Stripe customer
	// has 3 tax IDs, only the first was imported"). Surfaced in the CSV report
	// even on `insert` outcomes so operators can audit lossy translations.
	Notes []string
}

// ErrMapEmptyID signals a Stripe customer with no ID — almost always a
// malformed fixture. Production Stripe never returns one. Treated as an
// error outcome rather than panicking so the import run continues.
var ErrMapEmptyID = errors.New("stripe customer has empty id")

// mapCustomer translates a *stripe.Customer into Velox shapes. Pure function:
// no DB access, no Stripe API calls. The livemode argument is the importer's
// configured/derived value, which the caller cross-checks against
// cust.Livemode before calling — disagreements never reach this function.
func mapCustomer(cust *stripe.Customer, livemode bool) (MappedCustomer, error) {
	if cust == nil {
		return MappedCustomer{}, errors.New("nil stripe customer")
	}
	if strings.TrimSpace(cust.ID) == "" {
		return MappedCustomer{}, ErrMapEmptyID
	}

	var notes []string

	displayName := strings.TrimSpace(cust.Name)
	if displayName == "" {
		displayName = strings.TrimSpace(cust.Description)
	}
	displayNameSynthetic := false
	if displayName == "" {
		// Velox requires non-empty display_name. Stripe customers without a
		// name or description are common (B2C signup flows that only collect
		// email). Use a stable placeholder; operators can edit post-import.
		displayName = "(no name)"
		displayNameSynthetic = true
		notes = append(notes, "stripe customer has no name; defaulted display_name to '(no name)'")
	}

	c := domain.Customer{
		ExternalID:  cust.ID,
		DisplayName: displayName,
		Email:       strings.TrimSpace(cust.Email),
		Status:      domain.CustomerStatusActive,
	}

	bp := domain.CustomerBillingProfile{
		LegalName: strings.TrimSpace(cust.Name),
		Email:     strings.TrimSpace(cust.Email),
		Phone:     strings.TrimSpace(cust.Phone),
	}
	// Only mirror displayName into LegalName when it represents real data.
	// The synthetic "(no name)" placeholder must NOT bleed into the billing
	// profile — otherwise a truly-blank customer gets ProfileStatus =
	// incomplete instead of missing, masking the operator-action signal.
	if bp.LegalName == "" && !displayNameSynthetic {
		bp.LegalName = displayName
	}

	if cust.Address != nil {
		bp.AddressLine1 = strings.TrimSpace(cust.Address.Line1)
		bp.AddressLine2 = strings.TrimSpace(cust.Address.Line2)
		bp.City = strings.TrimSpace(cust.Address.City)
		bp.State = strings.TrimSpace(cust.Address.State)
		bp.PostalCode = strings.TrimSpace(cust.Address.PostalCode)
		bp.Country = strings.ToUpper(strings.TrimSpace(cust.Address.Country))
	}

	if cur := strings.TrimSpace(string(cust.Currency)); cur != "" {
		bp.Currency = strings.ToUpper(cur)
	}

	bp.TaxStatus = mapTaxStatus(cust.TaxExempt)

	if cust.TaxIDs != nil && len(cust.TaxIDs.Data) > 0 {
		first := cust.TaxIDs.Data[0]
		bp.TaxID = strings.TrimSpace(first.Value)
		bp.TaxIDType = string(first.Type)
		if len(cust.TaxIDs.Data) > 1 {
			notes = append(notes, fmt.Sprintf("stripe customer has %d tax IDs; only the first was imported", len(cust.TaxIDs.Data)))
		}
	}

	bp.ProfileStatus = computeProfileStatus(bp)

	// Sanity check: caller should have already filtered, but defense-in-depth.
	// We don't fail mapping on Stripe's deleted flag — IterateCustomers does that.
	_ = livemode

	return MappedCustomer{Customer: c, BillingProfile: bp, Notes: notes}, nil
}

// mapTaxStatus translates Stripe's CustomerTaxExempt enum to Velox's
// CustomerTaxStatus. Stripe has 3 values (none/exempt/reverse); Velox has
// 3 (standard/exempt/reverse_charge). Unknown values default to standard
// to keep the import flowing — surfaced as a divergence in the CSV report
// only if the divergence-detector flags a downstream mismatch.
func mapTaxStatus(s stripe.CustomerTaxExempt) domain.CustomerTaxStatus {
	switch s {
	case stripe.CustomerTaxExemptExempt:
		return domain.TaxStatusExempt
	case stripe.CustomerTaxExemptReverse:
		return domain.TaxStatusReverseCharge
	default:
		return domain.TaxStatusStandard
	}
}

// computeProfileStatus mirrors the rule used by Velox's existing customer
// service — populated address + tax => ready, partial => incomplete,
// nothing => missing. Importer applies it at write time so imported rows
// look identical to natively-created ones.
func computeProfileStatus(bp domain.CustomerBillingProfile) domain.BillingProfileStatus {
	hasAddress := bp.AddressLine1 != "" && bp.City != "" && bp.Country != ""
	hasTaxOrName := bp.TaxID != "" || bp.LegalName != ""
	switch {
	case hasAddress && hasTaxOrName:
		return domain.BillingProfileReady
	case hasAddress || hasTaxOrName:
		return domain.BillingProfileIncomplete
	default:
		return domain.BillingProfileMissing
	}
}
