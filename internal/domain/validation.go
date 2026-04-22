package domain

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/sagarsuperuser/velox/internal/errs"
)

// MaxLen validates a field doesn't exceed max length. Returns an errs.Invalid
// so callers can surface the message on the named field.
func MaxLen(field, value string, max int) error {
	if len(value) > max {
		return errs.Invalid(field, "must be at most "+strconv.Itoa(max)+" characters")
	}
	return nil
}

// ValidateCurrency checks for common ISO 4217 currency codes.
var validCurrencies = map[string]bool{
	"USD": true, "EUR": true, "GBP": true, "CAD": true, "AUD": true,
	"JPY": true, "CHF": true, "CNY": true, "INR": true, "BRL": true,
	"MXN": true, "SGD": true, "HKD": true, "NZD": true, "SEK": true,
	"NOK": true, "DKK": true, "ZAR": true, "KRW": true, "TWD": true,
	"PLN": true, "CZK": true, "HUF": true, "ILS": true, "AED": true,
	"SAR": true, "THB": true, "MYR": true, "IDR": true, "PHP": true,
	"VND": true, "CLP": true, "COP": true, "PEN": true, "ARS": true,
}

// ValidateCurrency returns an errs.Invalid/Required tied to the "currency"
// field. When the caller's form uses a different field name (e.g.
// "default_currency"), wrap the returned message with the right field by
// calling errs.Invalid yourself instead.
func ValidateCurrency(currency string) error {
	upper := strings.ToUpper(strings.TrimSpace(currency))
	if upper == "" {
		return errs.Required("currency")
	}
	if !validCurrencies[upper] {
		return errs.Invalid("currency", "invalid currency code: "+upper)
	}
	return nil
}

// stripeTaxCodeFormat matches a Stripe product tax code: "txcd_" followed by
// 8 digits (e.g. txcd_10103001 for SaaS business use). The full catalog is
// maintained by Stripe; we only enforce the format to catch typos.
var stripeTaxCodeFormat = regexp.MustCompile(`^txcd_[0-9]{8}$`)

// ValidateStripeTaxCode checks a Stripe product tax code format. Empty is
// allowed — callers supply a default when the field is optional.
func ValidateStripeTaxCode(field, code string) error {
	if code == "" {
		return nil
	}
	if !stripeTaxCodeFormat.MatchString(code) {
		return errs.Invalid(field, "must be a Stripe product tax code like 'txcd_10103001'")
	}
	return nil
}
