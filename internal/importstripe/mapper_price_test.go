package importstripe

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stripe/stripe-go/v82"

	"github.com/sagarsuperuser/velox/internal/domain"
)

// loadPriceFixture decodes a Stripe price fixture into *stripe.Price.
func loadPriceFixture(t *testing.T, name string) *stripe.Price {
	t.Helper()
	path := filepath.Join("testdata", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	var price stripe.Price
	if err := json.Unmarshal(data, &price); err != nil {
		t.Fatalf("decode fixture %s: %v", path, err)
	}
	return &price
}

func TestMapPrice_FlatMonthlyUSD(t *testing.T) {
	price := loadPriceFixture(t, "price_flat.json")
	got, err := mapPrice(price)
	if err != nil {
		t.Fatalf("mapPrice: %v", err)
	}
	if got.RatingRule.RuleKey != "price_flat001" {
		t.Errorf("RuleKey = %q, want price_flat001", got.RatingRule.RuleKey)
	}
	if got.RatingRule.Name != "Premium Monthly" {
		t.Errorf("Name = %q, want Premium Monthly (from nickname)", got.RatingRule.Name)
	}
	if got.RatingRule.Mode != domain.PricingFlat {
		t.Errorf("Mode = %q, want flat", got.RatingRule.Mode)
	}
	if got.RatingRule.Currency != "USD" {
		t.Errorf("Currency = %q, want USD (uppercased)", got.RatingRule.Currency)
	}
	if got.RatingRule.FlatAmountCents != 4999 {
		t.Errorf("FlatAmountCents = %d, want 4999", got.RatingRule.FlatAmountCents)
	}
	if got.PlanCode != "prod_NfJG2N4m6X" {
		t.Errorf("PlanCode = %q, want prod_NfJG2N4m6X", got.PlanCode)
	}
	if got.PlanUpdate.Currency != "USD" {
		t.Errorf("PlanUpdate.Currency = %q, want USD", got.PlanUpdate.Currency)
	}
	if got.PlanUpdate.BillingInterval != domain.BillingMonthly {
		t.Errorf("PlanUpdate.BillingInterval = %q, want monthly", got.PlanUpdate.BillingInterval)
	}
	if got.PlanUpdate.BaseAmountCents != 4999 {
		t.Errorf("PlanUpdate.BaseAmountCents = %d, want 4999", got.PlanUpdate.BaseAmountCents)
	}
}

func TestMapPrice_TieredIsError(t *testing.T) {
	price := loadPriceFixture(t, "price_tiered.json")
	_, err := mapPrice(price)
	if !errors.Is(err, ErrPriceUnsupportedTiered) {
		t.Errorf("err = %v, want ErrPriceUnsupportedTiered", err)
	}
}

func TestMapPrice_OneTimeIsError(t *testing.T) {
	price := loadPriceFixture(t, "price_one_time.json")
	_, err := mapPrice(price)
	if !errors.Is(err, ErrPriceUnsupportedOneTime) {
		t.Errorf("err = %v, want ErrPriceUnsupportedOneTime", err)
	}
}

func TestMapPrice_MeteredIsError(t *testing.T) {
	price := loadPriceFixture(t, "price_metered.json")
	_, err := mapPrice(price)
	if !errors.Is(err, ErrPriceUnsupportedMetered) {
		t.Errorf("err = %v, want ErrPriceUnsupportedMetered", err)
	}
}

func TestMapPrice_EURYearly(t *testing.T) {
	price := loadPriceFixture(t, "price_eur_yearly.json")
	got, err := mapPrice(price)
	if err != nil {
		t.Fatalf("mapPrice: %v", err)
	}
	if got.RatingRule.Currency != "EUR" {
		t.Errorf("Currency = %q, want EUR", got.RatingRule.Currency)
	}
	if got.PlanUpdate.BillingInterval != domain.BillingYearly {
		t.Errorf("BillingInterval = %q, want yearly", got.PlanUpdate.BillingInterval)
	}
	if got.RatingRule.FlatAmountCents != 49900 {
		t.Errorf("FlatAmountCents = %d, want 49900", got.RatingRule.FlatAmountCents)
	}
}

func TestMapPrice_UnsupportedWeeklyInterval(t *testing.T) {
	price := loadPriceFixture(t, "price_weekly.json")
	_, err := mapPrice(price)
	if !errors.Is(err, ErrPriceUnsupportedInterval) {
		t.Errorf("err = %v, want ErrPriceUnsupportedInterval", err)
	}
}

func TestMapPrice_EmptyIDIsError(t *testing.T) {
	_, err := mapPrice(&stripe.Price{ID: ""})
	if err != ErrMapEmptyPriceID {
		t.Errorf("err = %v, want ErrMapEmptyPriceID", err)
	}
}

func TestMapPrice_NilIsError(t *testing.T) {
	_, err := mapPrice(nil)
	if err == nil {
		t.Fatal("expected error for nil price")
	}
}

func TestMapPrice_MissingProduct(t *testing.T) {
	// Construct a price without product link.
	price := &stripe.Price{
		ID:            "price_orphan",
		BillingScheme: stripe.PriceBillingSchemePerUnit,
		Type:          stripe.PriceTypeRecurring,
		Currency:      "usd",
		UnitAmount:    100,
		Recurring:     &stripe.PriceRecurring{Interval: stripe.PriceRecurringIntervalMonth, IntervalCount: 1, UsageType: stripe.PriceRecurringUsageTypeLicensed},
		Active:        true,
		Livemode:      true,
	}
	_, err := mapPrice(price)
	if !errors.Is(err, ErrPriceMissingProduct) {
		t.Errorf("err = %v, want ErrPriceMissingProduct", err)
	}
}

func TestMapPrice_DecimalOnlyAmountIsError(t *testing.T) {
	// Stripe permits unit_amount=0 with unit_amount_decimal set when the
	// value is sub-cent. Velox doesn't model that.
	price := &stripe.Price{
		ID:                "price_decimal",
		BillingScheme:     stripe.PriceBillingSchemePerUnit,
		Type:              stripe.PriceTypeRecurring,
		Currency:          "usd",
		UnitAmount:        0,
		UnitAmountDecimal: 0.5,
		Product:           &stripe.Product{ID: "prod_x"},
		Recurring:         &stripe.PriceRecurring{Interval: stripe.PriceRecurringIntervalMonth, IntervalCount: 1, UsageType: stripe.PriceRecurringUsageTypeLicensed},
		Active:            true,
		Livemode:          true,
	}
	_, err := mapPrice(price)
	if !errors.Is(err, ErrPriceMissingUnitAmount) {
		t.Errorf("err = %v, want ErrPriceMissingUnitAmount", err)
	}
}
