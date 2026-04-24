package email

import (
	"html/template"
	"strings"
	"testing"
)

func TestSender_NotConfigured(t *testing.T) {
	s := &Sender{} // No SMTP config
	if s.IsConfigured() {
		t.Error("should not be configured without host")
	}

	// Should not error — just logs. publicToken intentionally empty here
	// because we're only exercising the "SMTP unconfigured → log + return"
	// short-circuit; the template render is covered separately.
	err := s.SendInvoice("t1", "test@example.com", "Test Customer", "VLX-000001", 19900, "USD", []byte("%PDF"), "")
	if err != nil {
		t.Errorf("unconfigured sender should not error: %v", err)
	}
}

func TestSender_Configured(t *testing.T) {
	s := &Sender{host: "smtp.example.com", port: "587", from: "billing@velox.dev"}
	if !s.IsConfigured() {
		t.Error("should be configured with host")
	}
}

func TestFormatAmount(t *testing.T) {
	tests := []struct {
		cents    int64
		currency string
		want     string
	}{
		{19900, "usd", "USD 199.00"},
		{500, "eur", "EUR 5.00"},
		{0, "usd", "USD 0.00"},
		{99, "gbp", "GBP 0.99"},
	}

	for _, tt := range tests {
		got := formatAmount(tt.cents, tt.currency)
		if got != tt.want {
			t.Errorf("formatAmount(%d, %q) = %q, want %q", tt.cents, tt.currency, got, tt.want)
		}
	}
}

// TestRenderInvoiceHTML exercises the layout + content render path end-to-
// end so template drift (missing var, escape slip) surfaces here instead
// of during a live send. The full HTML isn't byte-matched — we check the
// key substrings industry-standard verifiers look for.
func TestRenderInvoiceHTML(t *testing.T) {
	subject, content, ctaURL, ctaLabel := renderInvoiceHTML("Acme Corp", "INV-42", "USD 199.00", "https://app.velox.dev/invoice/vlx_pinv_abc")
	if subject != "Invoice INV-42 — USD 199.00" {
		t.Errorf("subject = %q", subject)
	}
	if ctaURL == "" || ctaLabel == "" {
		t.Errorf("CTA missing when hosted URL provided: url=%q label=%q", ctaURL, ctaLabel)
	}
	html, err := renderLayout(layoutInputs{
		Subject: subject, CompanyName: "YourCo", LogoURL: "https://cdn.example.com/logo.png",
		BrandColor: "#1f6feb", SupportURL: "https://yourco.com/support",
		Content: template.HTML(content),
		CTAURL:  ctaURL, CTALabel: ctaLabel,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"YourCo",                           // company name in header
		"https://cdn.example.com/logo.png", // logo
		"#1f6feb",                          // brand color accent
		"INV-42",                           // invoice number
		"USD 199.00",                       // amount
		"vlx_pinv_abc",                     // hosted token in CTA URL
		"View &amp; pay invoice",           // CTA label (html-escaped "&")
		"Acme Corp",                        // customer name interpolated in content
		"Powered by Velox Billing",         // footer credit
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered HTML missing %q", want)
		}
	}
	// No raw "<script" even if a hostile customer name got in — html-escape
	// happens inside renderInvoiceHTML because we wrap interpolations in
	// html.EscapeString before concatenation.
	hostile, _, _, _ := renderInvoiceHTML("<script>alert(1)</script>", "INV-42", "USD 0.01", "")
	if strings.Contains(hostile, "<script>") {
		t.Errorf("renderInvoiceHTML should escape customer name, got %q", hostile)
	}
}

// TestIsPermanentSMTPBounce checks the heuristic classifier used by
// the bounce-capture hook. 5xx codes in the error message → permanent,
// 4xx → transient, random 5-digit strings → must NOT misclassify.
func TestIsPermanentSMTPBounce(t *testing.T) {
	tests := []struct {
		name      string
		msg       string
		permanent bool
	}{
		{"no error", "", false},
		{"classic 550", "550 5.1.1 User unknown", true},
		{"553 relaying", "553 relaying denied", true},
		{"552 too big", "552 message size exceeds limits", true},
		{"421 transient", "421 try again later", false},
		{"450 soft bounce", "450 mailbox busy", false},
		{"zip code false-positive", "SMTP relay error for customer at 95014", false},
		{"wrapped 550", "send email: smtp: 550 mailbox unavailable", true},
		{"timeout", "dial tcp: i/o timeout", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			if tt.msg != "" {
				err = wrapErr(tt.msg)
			}
			got, _ := isPermanentSMTPBounce(err)
			if got != tt.permanent {
				t.Errorf("isPermanentSMTPBounce(%q) = %v, want %v", tt.msg, got, tt.permanent)
			}
		})
	}
}

type testErr struct{ m string }

func (e *testErr) Error() string { return e.m }
func wrapErr(m string) error     { return &testErr{m: m} }

// TestHostedInvoiceURL covers the base-URL-and-token assembly rules.
func TestHostedInvoiceURL(t *testing.T) {
	tests := []struct {
		name     string
		base     string
		token    string
		expected string
	}{
		{"empty base", "", "vlx_pinv_abc", ""},
		{"empty token", "https://app.example.com", "", ""},
		{"trailing slash trimmed", "https://app.example.com/", "vlx_pinv_abc", "https://app.example.com/invoice/vlx_pinv_abc"},
		{"no trailing slash", "https://app.example.com", "vlx_pinv_abc", "https://app.example.com/invoice/vlx_pinv_abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Sender{hostedInvoiceBaseURL: tt.base}
			if got := s.hostedInvoiceURL(tt.token); got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}
