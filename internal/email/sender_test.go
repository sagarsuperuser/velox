package email

import "testing"

func TestSender_NotConfigured(t *testing.T) {
	s := &Sender{} // No SMTP config
	if s.IsConfigured() {
		t.Error("should not be configured without host")
	}

	// Should not error — just logs
	err := s.SendInvoice("t1", "test@example.com", "Test Customer", "VLX-000001", 19900, "USD", []byte("%PDF"))
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

func TestEncodeBase64(t *testing.T) {
	input := []byte("Hello, World!")
	encoded := encodeBase64(input)
	if encoded == "" {
		t.Error("base64 should not be empty")
	}
	// "Hello, World!" in base64 is "SGVsbG8sIFdvcmxkIQ=="
	if encoded != "SGVsbG8sIFdvcmxkIQ==" {
		t.Errorf("got %q, want SGVsbG8sIFdvcmxkIQ==", encoded)
	}
}
