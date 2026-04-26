package importstripe

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stripe/stripe-go/v82"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// loadFixture decodes a Stripe customer fixture JSON file into a
// *stripe.Customer. Fixtures live under testdata/ and mirror Stripe's
// public API response samples.
func loadFixture(t *testing.T, name string) *stripe.Customer {
	t.Helper()
	path := filepath.Join("testdata", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	var cust stripe.Customer
	if err := json.Unmarshal(data, &cust); err != nil {
		t.Fatalf("decode fixture %s: %v", path, err)
	}
	return &cust
}

func TestMapCustomer_Full(t *testing.T) {
	cust := loadFixture(t, "customer_full.json")
	got, err := mapCustomer(cust, true)
	if err != nil {
		t.Fatalf("mapCustomer: %v", err)
	}
	if got.Customer.ExternalID != "cus_NfJG2N4m6X" {
		t.Errorf("ExternalID = %q, want cus_NfJG2N4m6X", got.Customer.ExternalID)
	}
	if got.Customer.DisplayName != "Acme Co" {
		t.Errorf("DisplayName = %q, want Acme Co", got.Customer.DisplayName)
	}
	if got.Customer.Email != "alice@example.com" {
		t.Errorf("Email = %q, want alice@example.com", got.Customer.Email)
	}
	if got.Customer.Status != domain.CustomerStatusActive {
		t.Errorf("Status = %q, want active", got.Customer.Status)
	}
	if got.BillingProfile.AddressLine1 != "1 Market St" {
		t.Errorf("AddressLine1 = %q, want 1 Market St", got.BillingProfile.AddressLine1)
	}
	if got.BillingProfile.Country != "US" {
		t.Errorf("Country = %q, want US (uppercased)", got.BillingProfile.Country)
	}
	if got.BillingProfile.Currency != "USD" {
		t.Errorf("Currency = %q, want USD (uppercased)", got.BillingProfile.Currency)
	}
	if got.BillingProfile.TaxStatus != domain.TaxStatusStandard {
		t.Errorf("TaxStatus = %q, want standard", got.BillingProfile.TaxStatus)
	}
	if got.BillingProfile.TaxID != "DE123456789" {
		t.Errorf("TaxID = %q, want DE123456789", got.BillingProfile.TaxID)
	}
	if got.BillingProfile.TaxIDType != "eu_vat" {
		t.Errorf("TaxIDType = %q, want eu_vat", got.BillingProfile.TaxIDType)
	}
	if got.BillingProfile.ProfileStatus != domain.BillingProfileReady {
		t.Errorf("ProfileStatus = %q, want ready", got.BillingProfile.ProfileStatus)
	}
}

func TestMapCustomer_NoNameFallsBackToDescription(t *testing.T) {
	cust := loadFixture(t, "customer_minimal.json")
	got, err := mapCustomer(cust, false)
	if err != nil {
		t.Fatalf("mapCustomer: %v", err)
	}
	if got.Customer.DisplayName != "Internal — sandbox tester" {
		t.Errorf("DisplayName = %q, want Description fallback", got.Customer.DisplayName)
	}
	if got.BillingProfile.ProfileStatus != domain.BillingProfileIncomplete {
		t.Errorf("ProfileStatus = %q, want incomplete (legal_name only)", got.BillingProfile.ProfileStatus)
	}
}

func TestMapCustomer_NoNameNoDescriptionDefaults(t *testing.T) {
	cust := loadFixture(t, "customer_blank.json")
	got, err := mapCustomer(cust, false)
	if err != nil {
		t.Fatalf("mapCustomer: %v", err)
	}
	if got.Customer.DisplayName != "(no name)" {
		t.Errorf("DisplayName = %q, want '(no name)' default", got.Customer.DisplayName)
	}
	if got.BillingProfile.ProfileStatus != domain.BillingProfileMissing {
		t.Errorf("ProfileStatus = %q, want missing (truly blank)", got.BillingProfile.ProfileStatus)
	}
	if !containsNote(got.Notes, "no name") {
		t.Errorf("expected note about defaulted name; got %v", got.Notes)
	}
}

func TestMapCustomer_TaxExemptVariants(t *testing.T) {
	cases := []struct {
		stripeVal stripe.CustomerTaxExempt
		want      domain.CustomerTaxStatus
	}{
		{stripe.CustomerTaxExemptNone, domain.TaxStatusStandard},
		{stripe.CustomerTaxExemptExempt, domain.TaxStatusExempt},
		{stripe.CustomerTaxExemptReverse, domain.TaxStatusReverseCharge},
	}
	for _, tc := range cases {
		t.Run(string(tc.stripeVal), func(t *testing.T) {
			got := mapTaxStatus(tc.stripeVal)
			if got != tc.want {
				t.Errorf("mapTaxStatus(%q) = %q, want %q", tc.stripeVal, got, tc.want)
			}
		})
	}
}

func TestMapCustomer_MultipleTaxIDsRecordsNote(t *testing.T) {
	cust := loadFixture(t, "customer_multi_taxid.json")
	got, err := mapCustomer(cust, true)
	if err != nil {
		t.Fatalf("mapCustomer: %v", err)
	}
	// Phase 0 only takes the first.
	if got.BillingProfile.TaxID != "DE111" {
		t.Errorf("TaxID = %q, want DE111 (first entry)", got.BillingProfile.TaxID)
	}
	if !containsNote(got.Notes, "tax IDs") {
		t.Errorf("expected note about multiple tax IDs; got %v", got.Notes)
	}
}

func TestMapCustomer_EmptyIDIsError(t *testing.T) {
	cust := &stripe.Customer{ID: ""}
	_, err := mapCustomer(cust, true)
	if err != ErrMapEmptyID {
		t.Errorf("err = %v, want ErrMapEmptyID", err)
	}
}

func TestMapCustomer_NilCustomer(t *testing.T) {
	_, err := mapCustomer(nil, true)
	if err == nil {
		t.Fatal("expected error for nil customer")
	}
}

func containsNote(notes []string, substr string) bool {
	for _, n := range notes {
		if strings.Contains(n, substr) {
			return true
		}
	}
	return false
}
