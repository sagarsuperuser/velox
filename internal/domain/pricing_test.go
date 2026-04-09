package domain

import "testing"

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
