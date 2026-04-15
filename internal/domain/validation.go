package domain

import (
	"fmt"
	"strings"
)

// MaxLen validates a field doesn't exceed max length.
func MaxLen(field, value string, max int) error {
	if len(value) > max {
		return fmt.Errorf("%s must be at most %d characters", field, max)
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

func ValidateCurrency(currency string) error {
	upper := strings.ToUpper(strings.TrimSpace(currency))
	if upper == "" {
		return fmt.Errorf("currency is required")
	}
	if !validCurrencies[upper] {
		return fmt.Errorf("invalid currency code: %s", upper)
	}
	return nil
}
