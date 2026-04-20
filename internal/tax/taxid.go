package tax

import (
	"fmt"
	"regexp"
	"strings"
)

// TaxIDKind is the canonical identifier for a tax-ID scheme. Kept as strings
// rather than constants so unknown schemes pass through without rejection —
// validation runs only when we know the format.
const (
	TaxIDKindGSTIN = "gstin" // India Goods & Services Tax Identification Number
	TaxIDKindVAT   = "vat"   // European Union VAT identification number (format-only)
	TaxIDKindABN   = "abn"   // Australian Business Number (format-only)
)

// gstinPattern matches the Indian GSTIN format:
//
//	2 digits (state code) + 10 chars PAN (AAAAA9999A) + 1 digit entity code
//	+ 'Z' (reserved) + 1 alphanumeric check character
//
// Example: 27AAEPM1234C1Z5. Full checksum validation requires additional work
// and is not performed here — format check catches the vast majority of typos
// and intentional invalid inputs, which is the audit requirement for
// phase-lite. Upgrade to checksum validation when we have a paying Indian
// customer under a live GST registration.
var gstinPattern = regexp.MustCompile(`^[0-9]{2}[A-Z]{5}[0-9]{4}[A-Z]{1}[1-9A-Z]{1}Z[0-9A-Z]{1}$`)

// euVATPattern accepts the common EU VAT shape: 2-letter country prefix
// followed by up to 12 alphanumeric characters. Country-specific length and
// check-digit rules vary; network-based validation via VIES is a phase-2
// concern and not performed here.
var euVATPattern = regexp.MustCompile(`^[A-Z]{2}[A-Z0-9]{2,12}$`)

// australianABNPattern: 11 digits.
var australianABNPattern = regexp.MustCompile(`^[0-9]{11}$`)

// ValidateTaxID checks that a tax ID conforms to the format for its declared
// kind. Returns nil for unknown kinds (pass-through) so we don't reject tax
// IDs from jurisdictions we haven't added explicit support for. An empty
// value is always valid — callers should check presence separately if the
// field is required.
//
// Kind matching is case-insensitive on the key; the value itself is
// normalized (uppercased, whitespace trimmed) before format validation.
func ValidateTaxID(kind, value string) error {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case TaxIDKindGSTIN, "in_gst", "in_gstin":
		if !gstinPattern.MatchString(value) {
			return fmt.Errorf("invalid GSTIN format: expected 15-char code like 27AAEPM1234C1Z5")
		}
	case TaxIDKindVAT, "eu_vat":
		if !euVATPattern.MatchString(value) {
			return fmt.Errorf("invalid EU VAT format: expected 2-letter country prefix + alphanumerics")
		}
	case TaxIDKindABN, "au_abn":
		if !australianABNPattern.MatchString(value) {
			return fmt.Errorf("invalid ABN format: expected 11 digits")
		}
	}
	return nil
}

// NormalizeTaxID returns the canonical storage form: whitespace-trimmed and
// uppercased for schemes that are case-insensitive by spec. Safe to call
// before validation; returns the input unchanged when it's empty.
func NormalizeTaxID(value string) string {
	v := strings.TrimSpace(value)
	return strings.ToUpper(v)
}
