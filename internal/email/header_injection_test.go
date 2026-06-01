package email

import (
	"strings"
	"testing"
)

// TestFromHeaderValue_NeutralizesInjection covers the medium-severity audit
// finding: a tenant CompanyName flowed unsanitized into the raw From: SMTP
// header, so a value like "Acme\r\nBcc: victim@x.com" could smuggle extra
// headers. mail.Address.String() RFC-2047-encodes the display name and never
// emits raw CR/LF.
func TestFromHeaderValue_NeutralizesInjection(t *testing.T) {
	addr := "billing@velox.dev"

	t.Run("plain name passes through", func(t *testing.T) {
		got := fromHeaderValue("Acme Inc", addr)
		if !strings.Contains(got, addr) || !strings.Contains(got, "Acme") {
			t.Errorf("expected name + address, got %q", got)
		}
		assertNoRawCRLF(t, got)
	})

	t.Run("empty name yields bare address", func(t *testing.T) {
		if got := fromHeaderValue("", addr); got != addr {
			t.Errorf("got %q, want %q", got, addr)
		}
	})

	t.Run("CRLF injection neutralized", func(t *testing.T) {
		got := fromHeaderValue("Acme\r\nBcc: victim@evil.com", addr)
		assertNoRawCRLF(t, got)
		if strings.Contains(got, "Bcc: victim@evil.com\r\n") {
			t.Errorf("injected Bcc header survived: %q", got)
		}
	})
}

// TestEncodeHeader_NeutralizesInjection ensures Subject (and any other header
// value) carrying CR/LF is Q-encoded rather than breaking the header block.
func TestEncodeHeader_NeutralizesInjection(t *testing.T) {
	t.Run("plain ascii unchanged", func(t *testing.T) {
		if got := encodeHeader("Invoice VLX-001 is ready"); got != "Invoice VLX-001 is ready" {
			t.Errorf("plain subject altered: %q", got)
		}
	})

	t.Run("CRLF injection encoded", func(t *testing.T) {
		got := encodeHeader("Hi\r\nBcc: victim@evil.com")
		assertNoRawCRLF(t, got)
	})
}

func assertNoRawCRLF(t *testing.T, s string) {
	t.Helper()
	if strings.ContainsAny(s, "\r\n") {
		t.Errorf("header value contains raw CR/LF: %q", s)
	}
}
