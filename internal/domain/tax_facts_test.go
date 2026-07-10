package domain

import (
	"encoding/json"
	"testing"
	"time"
)

// TestInvoiceTaxFactsWireCompat pins the load-bearing property of the TaxFacts
// embed: because the struct is embedded UNTAGGED, its JSON keys promote FLAT —
// the Invoice wire shape is byte-identical to the former 13 flat fields. If
// someone adds a json tag to the embed (nesting the keys under "TaxFacts") or
// renames a key, the API and every stored payload break silently for clients;
// this test makes that a loud failure.
func TestInvoiceTaxFactsWireCompat(t *testing.T) {
	deferredAt := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	inv := Invoice{
		ID: "inv_1",
		TaxFacts: TaxFacts{
			TaxAmountCents:   1800,
			TaxRate:          18.5,
			TaxName:          "VAT",
			TaxCountry:       "GB",
			TaxID:            "GB123",
			TaxProvider:      "stripe_tax",
			TaxCalculationID: "taxcalc_1",
			TaxReverseCharge: true,
			TaxExemptReason:  "exempt_reason",
			TaxStatus:        InvoiceTaxPending,
			TaxDeferredAt:    &deferredAt,
			TaxPendingReason: "outage",
			TaxErrorCode:     "provider_outage",
		},
		TaxTransactionID: "tx_1",
	}
	raw, err := json.Marshal(inv)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	// No nested object may appear — the embed must promote flat.
	if _, nested := m["TaxFacts"]; nested {
		t.Fatalf("TaxFacts marshaled as a NESTED object — the embed must stay untagged so keys promote flat")
	}
	for _, key := range []string{
		"tax_amount_cents", "tax_rate", "tax_name", "tax_country", "tax_id",
		"tax_provider", "tax_calculation_id", "tax_reverse_charge",
		"tax_exempt_reason", "tax_status", "tax_deferred_at",
		"tax_pending_reason", "tax_error_code",
		// invoice-only siblings must still be flat too
		"tax_transaction_id",
	} {
		if _, ok := m[key]; !ok {
			t.Errorf("wire key %q missing from marshaled Invoice", key)
		}
	}

	// Round-trip: legacy flat JSON (what clients send / stored payloads hold)
	// must land in the promoted fields.
	var back Invoice
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if back.TaxAmountCents != 1800 || back.TaxErrorCode != "provider_outage" ||
		back.TaxStatus != InvoiceTaxPending || !back.TaxReverseCharge ||
		back.TaxDeferredAt == nil || !back.TaxDeferredAt.Equal(deferredAt) {
		t.Errorf("round-trip lost tax facts: %+v", back.TaxFacts)
	}

	// omitempty parity with the pre-refactor tags: on a zero invoice the two
	// always-present keys stay, the optional ones vanish.
	rawZero, _ := json.Marshal(Invoice{ID: "inv_0"})
	var z map[string]any
	_ = json.Unmarshal(rawZero, &z)
	for _, mustHave := range []string{"tax_amount_cents", "tax_rate"} {
		if _, ok := z[mustHave]; !ok {
			t.Errorf("zero invoice must still carry %q (field has no omitempty)", mustHave)
		}
	}
	for _, mustOmit := range []string{"tax_name", "tax_provider", "tax_status", "tax_deferred_at", "tax_error_code", "tax_transaction_id"} {
		if _, ok := z[mustOmit]; ok {
			t.Errorf("zero invoice must omit %q (omitempty)", mustOmit)
		}
	}
}
