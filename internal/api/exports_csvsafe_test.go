package api

import "testing"

// TestCSVSafe covers the low-severity [security] audit finding: free-text
// columns in CSV exports were written unescaped, so a customer-controlled value
// beginning with a formula character executes when an operator opens the file in
// Excel / Sheets / LibreOffice. csvSafe prefixes those with a single quote.
func TestCSVSafe(t *testing.T) {
	cases := map[string]string{
		"":                  "",
		"Acme Inc":          "Acme Inc",      // plain text untouched
		"cus_123":           "cus_123",       // id untouched
		"user@example.com":  "user@example.com",
		"=HYPERLINK(\"x\")": "'=HYPERLINK(\"x\")", // formula neutralized
		"+1234567890":       "'+1234567890",
		"-2+3":              "'-2+3",
		"@SUM(A1:A9)":       "'@SUM(A1:A9)",
		"\tTabbed":          "'\tTabbed",
		"\rCarriage":        "'\rCarriage",
	}
	for in, want := range cases {
		if got := csvSafe(in); got != want {
			t.Errorf("csvSafe(%q) = %q, want %q", in, got, want)
		}
	}
}
