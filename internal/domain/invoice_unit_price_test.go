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

// DisplayUnitAmountDecimal shows the stamped NOMINAL configured rate on flat
// usage lines (clean, override-safe rate-card figure) and falls back to the
// effective amount÷quantity rate everywhere else (ADR-054 re-examination).
func TestDisplayUnitAmountDecimal(t *testing.T) {
	dec := func(s string) *decimal.Decimal { d := decimal.RequireFromString(s); return &d }
	cases := []struct {
		name    string
		amount  int64
		qty     int64
		qtyDec  string
		nominal *decimal.Decimal
		want    string
	}{
		{
			// The screenshot case: 6,000 tokens billed 2¢ at a configured
			// $3.00/1M = 0.0003¢/token. Effective is the inflated, repeating
			// 0.000333…; nominal is the clean rate-card figure the fix shows.
			name:   "flat usage shows nominal, not the inflated effective",
			amount: 2, qty: 6000, qtyDec: "6000", nominal: dec("0.0003"), want: "0.0003",
		},
		{
			name:   "nil nominal falls back to effective (today's behavior)",
			amount: 2, qty: 6000, qtyDec: "6000", nominal: nil, want: "0.000333333333",
		},
		{
			// Graduated/package lines stamp no nominal (no single rate); the
			// blended effective rate is the honest figure and must be kept.
			name:   "graduated line (nil nominal) keeps blended effective",
			amount: 300, qty: 200, qtyDec: "200", nominal: nil, want: "1.5",
		},
		{
			// A clean flat line where effective already equals nominal — the
			// display is unchanged, proving the fix is inert when there's no
			// rounding drift.
			name:   "clean flat line: nominal equals effective",
			amount: 1800, qty: 6000000, qtyDec: "6000000", nominal: dec("0.0003"), want: "0.0003",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			qd := decimal.Zero
			if tc.qtyDec != "" {
				qd = decimal.RequireFromString(tc.qtyDec)
			}
			li := InvoiceLineItem{
				AmountCents: tc.amount, Quantity: tc.qty, QuantityDecimal: qd,
				NominalUnitAmountDecimal: tc.nominal,
			}
			if got := li.DisplayUnitAmountDecimal().String(); got != tc.want {
				t.Fatalf("DisplayUnitAmountDecimal: got %q want %q", got, tc.want)
			}
		})
	}
}

// The wire's unit_amount_decimal must reflect the nominal rate when stamped, so
// the dashboard/hosted renderers show the clean rate without any FE change.
func TestInvoiceLineItem_MarshalJSON_PrefersNominal(t *testing.T) {
	nom := decimal.RequireFromString("0.0003")
	li := InvoiceLineItem{
		AmountCents: 2, Quantity: 6000,
		QuantityDecimal:          decimal.RequireFromString("6000"),
		UnitAmountCents:          0,
		NominalUnitAmountDecimal: &nom,
	}
	b, err := json.Marshal(li)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"unit_amount_decimal":"0.0003"`) {
		t.Fatalf("expected nominal unit_amount_decimal 0.0003, got: %s", s)
	}
	// Internal-only field must not leak onto the wire.
	if strings.Contains(s, "nominal_unit_amount_decimal") {
		t.Fatalf("nominal field must be json:\"-\", got: %s", s)
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
