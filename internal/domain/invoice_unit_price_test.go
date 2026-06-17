package domain

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// The effective per-unit price is derived from amount ÷ quantity and must not
// collapse a sub-cent rate to 0 the way the whole-cent UnitAmountCents does.
func TestEffectiveUnitAmountDecimal(t *testing.T) {
	cases := []struct {
		name   string
		amount int64
		qty    int64
		qtyDec string // QuantityDecimal; "" = zero (fall back to qty)
		want   string // expected decimal-cents string
	}{
		{"sub-cent flat (screenshot case)", 300, 1000, "1000", "0.3"},
		{"whole-cent rate", 5000, 10, "10", "500"},
		{"fractional quantity reconciles", 300, 1, "1.5", "200"},
		{"falls back to int quantity when decimal zero", 500, 1000, "", "0.5"},
		{"zero quantity guards to zero", 300, 0, "", "0"},
		{"negative credit line", -5000, 1, "1", "-5000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			qd := decimal.Zero
			if tc.qtyDec != "" {
				qd = decimal.RequireFromString(tc.qtyDec)
			}
			li := InvoiceLineItem{AmountCents: tc.amount, Quantity: tc.qty, QuantityDecimal: qd}
			if got := li.EffectiveUnitAmountDecimal().String(); got != tc.want {
				t.Fatalf("EffectiveUnitAmountDecimal: got %q want %q", got, tc.want)
			}
		})
	}
}

// The wire form must carry unit_amount_decimal (full precision) AND retain the
// legacy whole-cent unit_amount_cents.
func TestInvoiceLineItem_MarshalJSON_CarriesUnitAmountDecimal(t *testing.T) {
	li := InvoiceLineItem{
		AmountCents:     300,
		Quantity:        1000,
		QuantityDecimal: decimal.RequireFromString("1000"),
		UnitAmountCents: 0, // whole-cent rate floors to 0 — the bug we're fixing
	}
	b, err := json.Marshal(li)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"unit_amount_decimal":"0.3"`) {
		t.Fatalf("expected unit_amount_decimal 0.3 in JSON, got: %s", s)
	}
	if !strings.Contains(s, `"unit_amount_cents":0`) {
		t.Fatalf("expected unit_amount_cents:0 retained, got: %s", s)
	}
}
