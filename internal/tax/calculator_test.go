package tax

import (
	"context"
	"testing"
)

func TestManualCalculator_BasicTax(t *testing.T) {
	calc := NewManualCalculator(1850, "Sales Tax") // 18.50%
	result, err := calc.CalculateTax(context.Background(), "usd", CustomerAddress{}, []LineItemInput{
		{AmountCents: 10000, Description: "Widget", Quantity: 1},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 10000 * 1850 / 10000 = 1850
	if result.TotalTaxAmountCents != 1850 {
		t.Errorf("got total tax %d, want 1850", result.TotalTaxAmountCents)
	}
	if result.TaxRateBP != 1850 {
		t.Errorf("got rate %d bp, want 1850", result.TaxRateBP)
	}
	if result.TaxName != "Sales Tax" {
		t.Errorf("got name %q, want Sales Tax", result.TaxName)
	}
	if len(result.LineItemTaxes) != 1 {
		t.Fatalf("got %d line taxes, want 1", len(result.LineItemTaxes))
	}
	if result.LineItemTaxes[0].TaxAmountCents != 1850 {
		t.Errorf("line item tax: got %d, want 1850", result.LineItemTaxes[0].TaxAmountCents)
	}
}

func TestManualCalculator_ZeroRate(t *testing.T) {
	calc := NewManualCalculator(0, "")
	result, err := calc.CalculateTax(context.Background(), "usd", CustomerAddress{}, []LineItemInput{
		{AmountCents: 5000, Description: "Gadget", Quantity: 1},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalTaxAmountCents != 0 {
		t.Errorf("zero rate should produce 0 tax, got %d", result.TotalTaxAmountCents)
	}
	if len(result.LineItemTaxes) != 0 {
		t.Errorf("zero rate should produce no line taxes, got %d", len(result.LineItemTaxes))
	}
}

func TestManualCalculator_EmptyLineItems(t *testing.T) {
	calc := NewManualCalculator(1000, "VAT")
	result, err := calc.CalculateTax(context.Background(), "eur", CustomerAddress{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalTaxAmountCents != 0 {
		t.Errorf("empty items should produce 0 tax, got %d", result.TotalTaxAmountCents)
	}
}

func TestManualCalculator_MultipleLineItems_SumsCorrectly(t *testing.T) {
	calc := NewManualCalculator(1000, "Tax") // 10%
	items := []LineItemInput{
		{AmountCents: 4900, Description: "Base Fee", Quantity: 1},
		{AmountCents: 12500, Description: "API Calls", Quantity: 1500},
		{AmountCents: 625000, Description: "Storage", Quantity: 250},
	}

	result, err := calc.CalculateTax(context.Background(), "usd", CustomerAddress{}, items)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Subtotal = 4900 + 12500 + 625000 = 642400
	// Total tax = 642400 * 1000 / 10000 = 64240
	if result.TotalTaxAmountCents != 64240 {
		t.Errorf("got total tax %d, want 64240", result.TotalTaxAmountCents)
	}

	// Verify line item taxes sum to total
	var lineSum int64
	for _, lt := range result.LineItemTaxes {
		lineSum += lt.TaxAmountCents
	}
	if lineSum != result.TotalTaxAmountCents {
		t.Errorf("line item sum %d != total %d", lineSum, result.TotalTaxAmountCents)
	}

	if len(result.LineItemTaxes) != 3 {
		t.Fatalf("got %d line taxes, want 3", len(result.LineItemTaxes))
	}
}

func TestManualCalculator_RoundingEdgeCase(t *testing.T) {
	// 333 cents * 1000bp / 10000 = 33.3 -> 33 (no rounding: 3000 < 5000)
	calc := NewManualCalculator(1000, "Tax")
	result, err := calc.CalculateTax(context.Background(), "usd", CustomerAddress{}, []LineItemInput{
		{AmountCents: 333, Description: "item", Quantity: 1},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalTaxAmountCents != 33 {
		t.Errorf("got %d, want 33 (no round up)", result.TotalTaxAmountCents)
	}
}

func TestManualCalculator_RoundingUp(t *testing.T) {
	// 155 cents * 1000bp / 10000 = 15.5 -> remainder 5000 >= 5000 -> round up to 16
	calc := NewManualCalculator(1000, "Tax")
	result, err := calc.CalculateTax(context.Background(), "usd", CustomerAddress{}, []LineItemInput{
		{AmountCents: 155, Description: "item", Quantity: 1},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalTaxAmountCents != 16 {
		t.Errorf("got %d, want 16 (round up)", result.TotalTaxAmountCents)
	}
}

func TestManualCalculator_RemainderAdjustment(t *testing.T) {
	// Two items where individual rounding doesn't sum to total:
	// Item A: 101 * 1000 / 10000 = 10, remainder 1000 < 5000 -> 10
	// Item B: 102 * 1000 / 10000 = 10, remainder 2000 < 5000 -> 10
	// Line sum = 20
	// Total: (101+102) = 203 * 1000 / 10000 = 20, remainder 3000 < 5000 -> 20
	// No adjustment needed here.
	//
	// Better test: items that actually diverge.
	// Item A: 105 * 1000 / 10000 = 10, remainder 5000 >= 5000 -> 11
	// Item B: 105 * 1000 / 10000 = 10, remainder 5000 >= 5000 -> 11
	// Line sum = 22
	// Total: 210 * 1000 / 10000 = 21, remainder 0 -> 21
	// Adjustment: last item gets 21 - 22 = -1, so last = 10
	calc := NewManualCalculator(1000, "Tax")
	result, err := calc.CalculateTax(context.Background(), "usd", CustomerAddress{}, []LineItemInput{
		{AmountCents: 105, Description: "A", Quantity: 1},
		{AmountCents: 105, Description: "B", Quantity: 1},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.TotalTaxAmountCents != 21 {
		t.Errorf("got total %d, want 21", result.TotalTaxAmountCents)
	}

	var lineSum int64
	for _, lt := range result.LineItemTaxes {
		lineSum += lt.TaxAmountCents
	}
	if lineSum != 21 {
		t.Errorf("line sum %d != total 21", lineSum)
	}
}

func TestManualCalculator_NegativeRate(t *testing.T) {
	calc := NewManualCalculator(-100, "Invalid")
	result, err := calc.CalculateTax(context.Background(), "usd", CustomerAddress{}, []LineItemInput{
		{AmountCents: 1000, Description: "item", Quantity: 1},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalTaxAmountCents != 0 {
		t.Errorf("negative rate should produce 0 tax, got %d", result.TotalTaxAmountCents)
	}
}

func TestManualCalculator_AllLineItemsHaveCorrectIndex(t *testing.T) {
	calc := NewManualCalculator(500, "GST") // 5%
	items := []LineItemInput{
		{AmountCents: 1000, Description: "A", Quantity: 1},
		{AmountCents: 2000, Description: "B", Quantity: 2},
		{AmountCents: 3000, Description: "C", Quantity: 3},
	}
	result, err := calc.CalculateTax(context.Background(), "aud", CustomerAddress{}, items)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i, lt := range result.LineItemTaxes {
		if lt.Index != i {
			t.Errorf("line %d: got index %d", i, lt.Index)
		}
		if lt.TaxRateBP != 500 {
			t.Errorf("line %d: got rate %d, want 500", i, lt.TaxRateBP)
		}
		if lt.TaxName != "GST" {
			t.Errorf("line %d: got name %q, want GST", i, lt.TaxName)
		}
	}
}

func TestParseLineRef(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"line_0", 0},
		{"line_5", 5},
		{"line_123", 123},
		{"", -1},
		{"item_0", -1},
		{"line_", -1},
		{"line_abc", -1},
	}
	for _, tc := range tests {
		got := parseLineRef(tc.input)
		if got != tc.want {
			t.Errorf("parseLineRef(%q) = %d, want %d", tc.input, got, tc.want)
		}
	}
}
