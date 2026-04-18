package domain

import "testing"

func BenchmarkComputeAmountCents_Flat(b *testing.B) {
	rule := RatingRuleVersion{Mode: PricingFlat, FlatAmountCents: 500}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ComputeAmountCents(rule, 1000)
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
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ComputeAmountCents(rule, 7500)
	}
}

func BenchmarkComputeAmountCents_Package(b *testing.B) {
	rule := RatingRuleVersion{
		Mode:                   PricingPackage,
		PackageSize:            1000,
		PackageAmountCents:     2000,
		OverageUnitAmountCents: 30,
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ComputeAmountCents(rule, 15750)
	}
}
