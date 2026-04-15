package email

import (
	"fmt"
	"log/slog"
	"net/smtp"
	"os"
	"strings"
)

// Sender sends emails via SMTP. If SMTP is not configured, it logs
// the email instead of sending (graceful degradation for local dev).
type Sender struct {
	host     string
	port     string
	username string
	password string
	from     string
}

// NewSender creates an email sender from environment variables.
// Returns a sender that logs instead of sending if SMTP is not configured.
func NewSender() *Sender {
	return &Sender{
		host:     strings.TrimSpace(os.Getenv("SMTP_HOST")),
		port:     envOr("SMTP_PORT", "587"),
		username: strings.TrimSpace(os.Getenv("SMTP_USERNAME")),
		password: strings.TrimSpace(os.Getenv("SMTP_PASSWORD")),
		from:     envOr("SMTP_FROM", "billing@velox.dev"),
	}
}

func (s *Sender) IsConfigured() bool {
	return s.host != ""
}

// SendInvoice sends an invoice email with PDF attachment.
func (s *Sender) SendInvoice(to, customerName, invoiceNumber string, totalCents int64, currency string, pdfBytes []byte) error {
	subject := fmt.Sprintf("Invoice %s — %s", invoiceNumber, formatAmount(totalCents, currency))
	body := fmt.Sprintf(`Dear %s,

Please find attached your invoice %s for %s.

Amount due: %s
Currency: %s

Thank you for your business.

— Velox Billing
`, customerName, invoiceNumber, invoiceNumber, formatAmount(totalCents, currency), currency)

	return s.send(to, subject, body, invoiceNumber+".pdf", pdfBytes)
}

// SendPaymentReceipt sends a payment receipt after successful payment.
func (s *Sender) SendPaymentReceipt(to, customerName, invoiceNumber string, amountCents int64, currency string) error {
	subject := fmt.Sprintf("Payment received for invoice %s", invoiceNumber)
	body := fmt.Sprintf(`Dear %s,

We have received your payment of %s for invoice %s.

Thank you for your prompt payment.

— Velox Billing
`, customerName, formatAmount(amountCents, currency), invoiceNumber)

	return s.send(to, subject, body, "", nil)
}

// SendDunningWarning notifies a customer about a failed payment retry.
func (s *Sender) SendDunningWarning(to, customerName, invoiceNumber string, attemptNumber, maxAttempts int, nextRetryDate string) error {
	subject := fmt.Sprintf("Action required — payment retry failed for invoice %s", invoiceNumber)
	body := fmt.Sprintf(`Dear %s,

Payment attempt %d of %d for invoice %s has failed.

We will retry your payment on %s. Please update your payment method to avoid further issues.

— Velox Billing
`, customerName, attemptNumber, maxAttempts, invoiceNumber, nextRetryDate)

	return s.send(to, subject, body, "", nil)
}

// SendDunningEscalation notifies a customer that all payment retries have been exhausted.
func (s *Sender) SendDunningEscalation(to, customerName, invoiceNumber string, action string) error {
	subject := fmt.Sprintf("Payment retries exhausted for invoice %s", invoiceNumber)
	body := fmt.Sprintf(`Dear %s,

All payment retry attempts for invoice %s have been exhausted.

Action taken: %s

Please contact us to resolve this matter.

— Velox Billing
`, customerName, invoiceNumber, action)

	return s.send(to, subject, body, "", nil)
}

// SendPaymentFailed notifies a customer about a failed payment.
func (s *Sender) SendPaymentFailed(to, customerName, invoiceNumber, reason string) error {
	subject := fmt.Sprintf("Payment failed for invoice %s", invoiceNumber)
	body := fmt.Sprintf(`Dear %s,

We were unable to process payment for invoice %s.

Reason: %s

Please update your payment method to avoid service interruption.

— Velox Billing
`, customerName, invoiceNumber, reason)

	return s.send(to, subject, body, "", nil)
}

// SendPaymentUpdateRequest sends an email requesting the customer to update their payment method.
func (s *Sender) SendPaymentUpdateRequest(to, customerName, invoiceNumber string, amountDueCents int64, currency, updateURL string) error {
	subject := fmt.Sprintf("Action required — update payment method for invoice %s", invoiceNumber)
	body := fmt.Sprintf(`Dear %s,

We were unable to process payment for invoice %s (%s).

Please update your payment method using the secure link below:

%s

This link will expire in 24 hours.

— Velox Billing
`, customerName, invoiceNumber, formatAmount(amountDueCents, currency), updateURL)

	return s.send(to, subject, body, "", nil)
}

func (s *Sender) send(to, subject, body, attachName string, attachData []byte) error {
	if !s.IsConfigured() {
		slog.Info("email (not sent — SMTP not configured)",
			"to", to,
			"subject", subject,
			"body_length", len(body),
			"attachment", attachName,
		)
		return nil
	}

	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("From: %s\r\n", s.from))
	msg.WriteString(fmt.Sprintf("To: %s\r\n", to))
	msg.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))

	if attachData != nil {
		boundary := "velox-boundary-12345"
		msg.WriteString("MIME-Version: 1.0\r\n")
		msg.WriteString(fmt.Sprintf("Content-Type: multipart/mixed; boundary=%s\r\n\r\n", boundary))

		// Text part
		msg.WriteString(fmt.Sprintf("--%s\r\n", boundary))
		msg.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
		msg.WriteString(body)
		msg.WriteString("\r\n")

		// PDF attachment
		msg.WriteString(fmt.Sprintf("--%s\r\n", boundary))
		msg.WriteString(fmt.Sprintf("Content-Type: application/pdf; name=\"%s\"\r\n", attachName))
		msg.WriteString("Content-Transfer-Encoding: base64\r\n")
		msg.WriteString(fmt.Sprintf("Content-Disposition: attachment; filename=\"%s\"\r\n\r\n", attachName))
		msg.WriteString(encodeBase64(attachData))
		msg.WriteString(fmt.Sprintf("\r\n--%s--\r\n", boundary))
	} else {
		msg.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
		msg.WriteString(body)
	}

	auth := smtp.PlainAuth("", s.username, s.password, s.host)
	addr := fmt.Sprintf("%s:%s", s.host, s.port)

	if err := smtp.SendMail(addr, auth, s.from, []string{to}, []byte(msg.String())); err != nil {
		slog.Error("send email failed", "to", to, "subject", subject, "error", err)
		return fmt.Errorf("send email: %w", err)
	}

	slog.Info("email sent", "to", to, "subject", subject)
	return nil
}

func formatAmount(cents int64, currency string) string {
	return fmt.Sprintf("%s %d.%02d", strings.ToUpper(currency), cents/100, cents%100)
}

func encodeBase64(data []byte) string {
	const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var buf strings.Builder
	for i := 0; i < len(data); i += 3 {
		var b [3]byte
		n := copy(b[:], data[i:])
		buf.WriteByte(charset[b[0]>>2])
		buf.WriteByte(charset[((b[0]&0x03)<<4)|(b[1]>>4)])
		if n > 1 {
			buf.WriteByte(charset[((b[1]&0x0F)<<2)|(b[2]>>6)])
		} else {
			buf.WriteByte('=')
		}
		if n > 2 {
			buf.WriteByte(charset[b[2]&0x3F])
		} else {
			buf.WriteByte('=')
		}
		if (i+3)%57 == 0 {
			buf.WriteString("\r\n")
		}
	}
	return buf.String()
}

func envOr(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}
