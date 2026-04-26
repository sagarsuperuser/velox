package importstripe

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stripe/stripe-go/v82"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// loadProductFixture decodes a Stripe product fixture into *stripe.Product.
func loadProductFixture(t *testing.T, name string) *stripe.Product {
	t.Helper()
	path := filepath.Join("testdata", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	var prod stripe.Product
	if err := json.Unmarshal(data, &prod); err != nil {
		t.Fatalf("decode fixture %s: %v", path, err)
	}
	return &prod
}

func TestMapProduct_Full(t *testing.T) {
	prod := loadProductFixture(t, "product_full.json")
	got, err := mapProduct(prod)
	if err != nil {
		t.Fatalf("mapProduct: %v", err)
	}
	if got.Plan.Code != "prod_NfJG2N4m6X" {
		t.Errorf("Plan.Code = %q, want prod_NfJG2N4m6X", got.Plan.Code)
	}
	if got.Plan.Name != "Premium" {
		t.Errorf("Plan.Name = %q, want Premium", got.Plan.Name)
	}
	if got.Plan.Description != "Premium plan for SaaS customers" {
		t.Errorf("Plan.Description = %q, want long form", got.Plan.Description)
	}
	if got.Plan.Currency != "USD" {
		t.Errorf("Plan.Currency = %q, want USD (default)", got.Plan.Currency)
	}
	if got.Plan.BillingInterval != domain.BillingMonthly {
		t.Errorf("Plan.BillingInterval = %q, want monthly (default)", got.Plan.BillingInterval)
	}
	if got.Plan.BaseAmountCents != 0 {
		t.Errorf("Plan.BaseAmountCents = %d, want 0 (default)", got.Plan.BaseAmountCents)
	}
	if got.Plan.Status != domain.PlanActive {
		t.Errorf("Plan.Status = %q, want active", got.Plan.Status)
	}
	// Lossy fields surfaced in notes.
	if !containsNote(got.Notes, "marketing_features") {
		t.Errorf("expected note about marketing_features; got %v", got.Notes)
	}
	if !containsNote(got.Notes, "url") {
		t.Errorf("expected note about url; got %v", got.Notes)
	}
	if !containsNote(got.Notes, "unit_label") {
		t.Errorf("expected note about unit_label; got %v", got.Notes)
	}
	if !containsNote(got.Notes, "pricing fields") {
		t.Errorf("expected note about pricing field defaults; got %v", got.Notes)
	}
}

func TestMapProduct_Minimal(t *testing.T) {
	prod := loadProductFixture(t, "product_minimal.json")
	got, err := mapProduct(prod)
	if err != nil {
		t.Fatalf("mapProduct: %v", err)
	}
	if got.Plan.Code != "prod_minimal01" {
		t.Errorf("Plan.Code = %q, want prod_minimal01", got.Plan.Code)
	}
	if got.Plan.Name != "Basic" {
		t.Errorf("Plan.Name = %q, want Basic", got.Plan.Name)
	}
	if got.Plan.Description != "" {
		t.Errorf("Plan.Description = %q, want empty", got.Plan.Description)
	}
	// Minimal product has no marketing_features / images / url so those
	// notes shouldn't appear.
	if containsNote(got.Notes, "marketing_features") {
		t.Errorf("unexpected marketing_features note for minimal product; got %v", got.Notes)
	}
}

func TestMapProduct_BlankNameDefaults(t *testing.T) {
	prod := loadProductFixture(t, "product_blank.json")
	got, err := mapProduct(prod)
	if err != nil {
		t.Fatalf("mapProduct: %v", err)
	}
	if got.Plan.Name != "(no name)" {
		t.Errorf("Plan.Name = %q, want '(no name)' default", got.Plan.Name)
	}
	if !containsNote(got.Notes, "no name") {
		t.Errorf("expected note about defaulted name; got %v", got.Notes)
	}
	// type=good is unusual.
	if !containsNote(got.Notes, "type=good") {
		t.Errorf("expected note about type=good; got %v", got.Notes)
	}
}

func TestMapProduct_EmptyIDIsError(t *testing.T) {
	_, err := mapProduct(&stripe.Product{ID: ""})
	if err != ErrMapEmptyProductID {
		t.Errorf("err = %v, want ErrMapEmptyProductID", err)
	}
}

func TestMapProduct_NilIsError(t *testing.T) {
	_, err := mapProduct(nil)
	if err == nil {
		t.Fatal("expected error for nil product")
	}
}
