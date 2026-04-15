package domain

import (
	"math"
	"testing"
)

func TestComputeAmountCents_Flat(t *testing.T) {
	rule := RatingRuleVersion{Mode: PricingFlat, FlatAmountCents: 500}

	tests := []struct {
		name     string
		qty      int64
		wantAmt  int64
		wantErr  bool
	}{
		{"zero quantity", 0, 0, false},
		{"positive quantity", 100, 50000, false},
		{"negative quantity", -1, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ComputeAmountCents(rule, tt.qty)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if got != tt.wantAmt {
				t.Errorf("got %d, want %d", got, tt.wantAmt)
			}
		})
	}
}

func TestComputeAmountCents_Graduated(t *testing.T) {
	rule := RatingRuleVersion{
		Mode: PricingGraduated,
		GraduatedTiers: []RatingTier{
			{UpTo: 10, UnitAmountCents: 100},
			{UpTo: 50, UnitAmountCents: 50},
			{UpTo: 0, UnitAmountCents: 25}, // unlimited
		},
	}

	tests := []struct {
		name    string
		qty     int64
		wantAmt int64
	}{
		{"within first tier", 5, 500},
		{"exact first tier boundary", 10, 1000},
		{"into second tier", 20, 1000 + 10*50},       // 1500
		{"exact second tier boundary", 50, 1000 + 40*50}, // 3000
		{"into overflow tier", 60, 1000 + 40*50 + 10*25},  // 3250
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ComputeAmountCents(rule, tt.qty)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.wantAmt {
				t.Errorf("got %d, want %d", got, tt.wantAmt)
			}
		})
	}
}

func TestComputeAmountCents_Package(t *testing.T) {
	rule := RatingRuleVersion{
		Mode:                   PricingPackage,
		PackageSize:            100,
		PackageAmountCents:     2000,
		OverageUnitAmountCents: 30,
	}

	tests := []struct {
		name    string
		qty     int64
		wantAmt int64
	}{
		{"zero", 0, 0},
		{"one full package", 100, 2000},
		{"with overage", 150, 2000 + 50*30}, // 3500
		{"two packages", 200, 4000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ComputeAmountCents(rule, tt.qty)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.wantAmt {
				t.Errorf("got %d, want %d", got, tt.wantAmt)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Edge case: flat pricing — overflow, negative unit price, large quantity
// ---------------------------------------------------------------------------

func TestComputeAmountCents_Flat_NegativeUnitPrice(t *testing.T) {
	rule := RatingRuleVersion{Mode: PricingFlat, FlatAmountCents: -1}
	_, err := ComputeAmountCents(rule, 10)
	if err != ErrInvalidPricingConfig {
		t.Fatalf("expected ErrInvalidPricingConfig for negative flat price, got %v", err)
	}
}

func TestComputeAmountCents_Flat_LargeQuantityOverflowCheck(t *testing.T) {
	// Verify large quantity * unit price doesn't silently overflow.
	// With FlatAmountCents=1000 ($10) and quantity near int64 max / 1000,
	// the multiplication should still produce a valid result.
	rule := RatingRuleVersion{Mode: PricingFlat, FlatAmountCents: 1000}
	qty := int64(math.MaxInt64 / 1000) // largest safe quantity
	got, err := ComputeAmountCents(rule, qty)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := qty * 1000
	if got != expected {
		t.Errorf("got %d, want %d", got, expected)
	}
}

func TestComputeAmountCents_Flat_ZeroUnitPrice(t *testing.T) {
	// Free tier: 0 cents per unit
	rule := RatingRuleVersion{Mode: PricingFlat, FlatAmountCents: 0}
	got, err := ComputeAmountCents(rule, 999999)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 0 {
		t.Errorf("expected 0 for free tier, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// Edge case: graduated pricing — boundary, insufficient tiers, bad config
// ---------------------------------------------------------------------------

func TestComputeAmountCents_Graduated_ExactTierBoundary(t *testing.T) {
	rule := RatingRuleVersion{
		Mode: PricingGraduated,
		GraduatedTiers: []RatingTier{
			{UpTo: 100, UnitAmountCents: 50},
			{UpTo: 0, UnitAmountCents: 25}, // catch-all
		},
	}
	// Exactly at the first tier boundary
	got, err := ComputeAmountCents(rule, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 100*50 {
		t.Errorf("got %d, want %d", got, 100*50)
	}
}

func TestComputeAmountCents_Graduated_OnlyUnlimitedTier(t *testing.T) {
	// Single tier with UpTo=0 (catch-all) handles any quantity
	rule := RatingRuleVersion{
		Mode: PricingGraduated,
		GraduatedTiers: []RatingTier{
			{UpTo: 0, UnitAmountCents: 10},
		},
	}
	got, err := ComputeAmountCents(rule, 5000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 5000*10 {
		t.Errorf("got %d, want %d", got, 5000*10)
	}
}

func TestComputeAmountCents_Graduated_InsufficientTiers(t *testing.T) {
	// Tiers only cover up to 100, but quantity is 200 and no catch-all
	rule := RatingRuleVersion{
		Mode: PricingGraduated,
		GraduatedTiers: []RatingTier{
			{UpTo: 100, UnitAmountCents: 50},
		},
	}
	_, err := ComputeAmountCents(rule, 200)
	if err != ErrInvalidPricingConfig {
		t.Fatalf("expected ErrInvalidPricingConfig for insufficient tiers, got %v", err)
	}
}

func TestComputeAmountCents_Graduated_EmptyTiers(t *testing.T) {
	rule := RatingRuleVersion{
		Mode:           PricingGraduated,
		GraduatedTiers: nil,
	}
	_, err := ComputeAmountCents(rule, 10)
	if err != ErrInvalidPricingConfig {
		t.Fatalf("expected ErrInvalidPricingConfig for empty tiers, got %v", err)
	}
}

func TestComputeAmountCents_Graduated_NegativeUnitPrice(t *testing.T) {
	rule := RatingRuleVersion{
		Mode: PricingGraduated,
		GraduatedTiers: []RatingTier{
			{UpTo: 100, UnitAmountCents: -5},
		},
	}
	_, err := ComputeAmountCents(rule, 10)
	if err != ErrInvalidPricingConfig {
		t.Fatalf("expected ErrInvalidPricingConfig for negative unit price in tier, got %v", err)
	}
}

func TestComputeAmountCents_Graduated_NegativeUpTo(t *testing.T) {
	rule := RatingRuleVersion{
		Mode: PricingGraduated,
		GraduatedTiers: []RatingTier{
			{UpTo: -10, UnitAmountCents: 50},
		},
	}
	_, err := ComputeAmountCents(rule, 5)
	if err != ErrInvalidPricingConfig {
		t.Fatalf("expected ErrInvalidPricingConfig for negative UpTo, got %v", err)
	}
}

func TestComputeAmountCents_Graduated_ZeroQuantity(t *testing.T) {
	rule := RatingRuleVersion{
		Mode: PricingGraduated,
		GraduatedTiers: []RatingTier{
			{UpTo: 100, UnitAmountCents: 50},
			{UpTo: 0, UnitAmountCents: 25},
		},
	}
	got, err := ComputeAmountCents(rule, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 0 {
		t.Errorf("expected 0 for zero quantity, got %d", got)
	}
}

func TestComputeAmountCents_Graduated_ThreeTiersExactBoundaries(t *testing.T) {
	// Tier 1: 0-10 @ 100, Tier 2: 11-50 @ 50, Tier 3: 51+ @ 25
	rule := RatingRuleVersion{
		Mode: PricingGraduated,
		GraduatedTiers: []RatingTier{
			{UpTo: 10, UnitAmountCents: 100},
			{UpTo: 50, UnitAmountCents: 50},
			{UpTo: 0, UnitAmountCents: 25},
		},
	}
	tests := []struct {
		name    string
		qty     int64
		wantAmt int64
	}{
		{"1 unit", 1, 100},
		{"10 units (end of tier 1)", 10, 1000},
		{"11 units (first in tier 2)", 11, 1000 + 50},
		{"50 units (end of tier 2)", 50, 1000 + 40*50},
		{"51 units (first in tier 3)", 51, 1000 + 40*50 + 25},
		{"1000 units (deep in tier 3)", 1000, 1000 + 40*50 + 950*25},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ComputeAmountCents(rule, tt.qty)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.wantAmt {
				t.Errorf("got %d, want %d", got, tt.wantAmt)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Edge case: package pricing
// ---------------------------------------------------------------------------

func TestComputeAmountCents_Package_ExactMultiple(t *testing.T) {
	rule := RatingRuleVersion{
		Mode:                   PricingPackage,
		PackageSize:            50,
		PackageAmountCents:     1000,
		OverageUnitAmountCents: 30,
	}
	// 150 = 3 * 50, no overage
	got, err := ComputeAmountCents(rule, 150)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 3*1000 {
		t.Errorf("got %d, want %d", got, 3*1000)
	}
}

func TestComputeAmountCents_Package_SingleUnitLargePackage(t *testing.T) {
	rule := RatingRuleVersion{
		Mode:                   PricingPackage,
		PackageSize:            1000,
		PackageAmountCents:     5000,
		OverageUnitAmountCents: 10,
	}
	// qty=1: 0 full packages, 1 overage unit
	got, err := ComputeAmountCents(rule, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 10 {
		t.Errorf("got %d, want %d (1 overage unit)", got, 10)
	}
}

func TestComputeAmountCents_Package_ZeroPackageSize(t *testing.T) {
	rule := RatingRuleVersion{
		Mode:                   PricingPackage,
		PackageSize:            0,
		PackageAmountCents:     1000,
		OverageUnitAmountCents: 10,
	}
	_, err := ComputeAmountCents(rule, 10)
	if err != ErrInvalidPricingConfig {
		t.Fatalf("expected ErrInvalidPricingConfig for zero package size, got %v", err)
	}
}

func TestComputeAmountCents_Package_NegativePackageSize(t *testing.T) {
	rule := RatingRuleVersion{
		Mode:                   PricingPackage,
		PackageSize:            -5,
		PackageAmountCents:     1000,
		OverageUnitAmountCents: 10,
	}
	_, err := ComputeAmountCents(rule, 10)
	if err != ErrInvalidPricingConfig {
		t.Fatalf("expected ErrInvalidPricingConfig for negative package size, got %v", err)
	}
}

func TestComputeAmountCents_Package_NegativePackagePrice(t *testing.T) {
	rule := RatingRuleVersion{
		Mode:                   PricingPackage,
		PackageSize:            100,
		PackageAmountCents:     -500,
		OverageUnitAmountCents: 10,
	}
	_, err := ComputeAmountCents(rule, 10)
	if err != ErrInvalidPricingConfig {
		t.Fatalf("expected ErrInvalidPricingConfig for negative package price, got %v", err)
	}
}

func TestComputeAmountCents_Package_NegativeOveragePrice(t *testing.T) {
	rule := RatingRuleVersion{
		Mode:                   PricingPackage,
		PackageSize:            100,
		PackageAmountCents:     1000,
		OverageUnitAmountCents: -5,
	}
	_, err := ComputeAmountCents(rule, 10)
	if err != ErrInvalidPricingConfig {
		t.Fatalf("expected ErrInvalidPricingConfig for negative overage price, got %v", err)
	}
}

func TestComputeAmountCents_Package_ZeroOveragePrice(t *testing.T) {
	// Free overage: anything beyond full packages costs nothing
	rule := RatingRuleVersion{
		Mode:                   PricingPackage,
		PackageSize:            100,
		PackageAmountCents:     2000,
		OverageUnitAmountCents: 0,
	}
	got, err := ComputeAmountCents(rule, 150)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 1 full package = 2000, 50 overage units at 0 = 0
	if got != 2000 {
		t.Errorf("got %d, want %d", got, 2000)
	}
}

// ---------------------------------------------------------------------------
// Edge case: unknown pricing mode
// ---------------------------------------------------------------------------

func TestComputeAmountCents_UnknownMode(t *testing.T) {
	rule := RatingRuleVersion{Mode: "volume"}
	_, err := ComputeAmountCents(rule, 10)
	if err != ErrInvalidPricingConfig {
		t.Fatalf("expected ErrInvalidPricingConfig for unknown mode, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Edge case: negative quantity for all modes
// ---------------------------------------------------------------------------

func TestComputeAmountCents_NegativeQuantity_AllModes(t *testing.T) {
	modes := []struct {
		name string
		rule RatingRuleVersion
	}{
		{"flat", RatingRuleVersion{Mode: PricingFlat, FlatAmountCents: 100}},
		{"graduated", RatingRuleVersion{
			Mode:           PricingGraduated,
			GraduatedTiers: []RatingTier{{UpTo: 0, UnitAmountCents: 10}},
		}},
		{"package", RatingRuleVersion{
			Mode: PricingPackage, PackageSize: 10, PackageAmountCents: 100, OverageUnitAmountCents: 5,
		}},
	}
	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			_, err := ComputeAmountCents(m.rule, -1)
			if err != ErrInvalidPricingConfig {
				t.Fatalf("expected ErrInvalidPricingConfig for negative quantity in %s mode, got %v", m.name, err)
			}
		})
	}
}
