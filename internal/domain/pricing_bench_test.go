package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func BenchmarkComputeAmountCents_Flat(b *testing.B) {
	rule := RatingRuleVersion{Mode: PricingFlat, FlatAmountCents: 500}
	q := decimal.NewFromInt(1000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ComputeAmountCents(rule, q)
	}
}

func BenchmarkComputeAmountCents_Graduated_5Tiers(b *testing.B) {
	rule := RatingRuleVersion{
		Mode: PricingGraduated,
		GraduatedTiers: []RatingTier{
			{UpTo: 100, UnitAmountCents: 50},
			{UpTo: 500, UnitAmountCents: 30},
			{UpTo: 1000, UnitAmountCents: 20},
			{UpTo: 5000, UnitAmountCents: 10},
			{UpTo: 0, UnitAmountCents: 5},
		},
	}
	q := decimal.NewFromInt(7500)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ComputeAmountCents(rule, q)
	}
}

func BenchmarkComputeAmountCents_Package(b *testing.B) {
	rule := RatingRuleVersion{
		Mode:                   PricingPackage,
		PackageSize:            1000,
		PackageAmountCents:     2000,
		OverageUnitAmountCents: 30,
	}
	q := decimal.NewFromInt(15750)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ComputeAmountCents(rule, q)
	}
}
