package tax

import "testing"

func TestNormalizeTaxID(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", ""},
		{"  ", ""},
		{"27aaepm1234c1z5", "27AAEPM1234C1Z5"},
		{"  27AAEPM1234C1Z5  ", "27AAEPM1234C1Z5"},
		{"de123456789", "DE123456789"},
	}
	for _, tc := range tests {
		got := NormalizeTaxID(tc.in)
		if got != tc.want {
			t.Errorf("NormalizeTaxID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestValidateTaxID_GSTIN(t *testing.T) {
	valid := []string{
		"27AAEPM1234C1Z5",
		"09AAACH7409R1ZZ",
	}
	for _, v := range valid {
		if err := ValidateTaxID("gstin", v); err != nil {
			t.Errorf("ValidateTaxID(gstin, %q) unexpected error: %v", v, err)
		}
	}
	// Alternate kind aliases must also work.
	if err := ValidateTaxID("in_gst", "27AAEPM1234C1Z5"); err != nil {
		t.Errorf("in_gst alias rejected: %v", err)
	}
	if err := ValidateTaxID("IN_GSTIN", "27AAEPM1234C1Z5"); err != nil {
		t.Errorf("IN_GSTIN alias rejected: %v", err)
	}

	invalid := []string{
		"ABC",              // too short
		"27AAEPM1234C1Z",   // 14 chars
		"27AAEPM1234C1Z55", // 16 chars
		"27AAEPM1234C1X5",  // no Z in reserved slot
		"AAEPM12345C1Z5",   // state code not numeric
	}
	for _, v := range invalid {
		if err := ValidateTaxID("gstin", v); err == nil {
			t.Errorf("ValidateTaxID(gstin, %q) expected error, got nil", v)
		}
	}

	// Lowercase input should be normalized and accepted (validator is case-insensitive).
	if err := ValidateTaxID("gstin", "27aaepm1234c1z5"); err != nil {
		t.Errorf("lowercase GSTIN should normalize and pass: %v", err)
	}
}

func TestValidateTaxID_VAT(t *testing.T) {
	valid := []string{"DE123456789", "FRAB123456789", "GB123456789"}
	for _, v := range valid {
		if err := ValidateTaxID("vat", v); err != nil {
			t.Errorf("ValidateTaxID(vat, %q) unexpected error: %v", v, err)
		}
	}
	if err := ValidateTaxID("eu_vat", "DE123456789"); err != nil {
		t.Errorf("eu_vat alias rejected: %v", err)
	}

	invalid := []string{"D1", "123456789", "DEABCDEFGHIJKLM"} // too short prefix, no prefix, too long
	for _, v := range invalid {
		if err := ValidateTaxID("vat", v); err == nil {
			t.Errorf("ValidateTaxID(vat, %q) expected error, got nil", v)
		}
	}
}

func TestValidateTaxID_ABN(t *testing.T) {
	if err := ValidateTaxID("abn", "51824753556"); err != nil {
		t.Errorf("valid ABN rejected: %v", err)
	}
	if err := ValidateTaxID("au_abn", "51824753556"); err != nil {
		t.Errorf("au_abn alias rejected: %v", err)
	}
	if err := ValidateTaxID("abn", "51824753"); err == nil {
		t.Error("short ABN should be rejected")
	}
	if err := ValidateTaxID("abn", "5182475355A"); err == nil {
		t.Error("ABN with letters should be rejected")
	}
}

func TestValidateTaxID_EmptyValuePasses(t *testing.T) {
	if err := ValidateTaxID("gstin", ""); err != nil {
		t.Errorf("empty value should pass: %v", err)
	}
	if err := ValidateTaxID("", ""); err != nil {
		t.Errorf("empty kind+value should pass: %v", err)
	}
}

func TestValidateTaxID_UnknownKindPassesThrough(t *testing.T) {
	// Jurisdictions without explicit support must not be rejected.
	if err := ValidateTaxID("br_cnpj", "12.345.678/0001-90"); err != nil {
		t.Errorf("unknown kind should pass through: %v", err)
	}
	if err := ValidateTaxID("sg_uen", "anything"); err != nil {
		t.Errorf("unknown kind should pass through: %v", err)
	}
}
