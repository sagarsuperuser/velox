package email

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/smtp"
	"os"
	"strings"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
)

// Sender sends emails via SMTP. If SMTP is not configured, it logs the
// email instead of sending (graceful degradation for local dev).
//
// Customer-facing emails render as multipart/alternative (text + HTML)
// with tenant-branded chrome pulled via SettingsGetter. Operator emails
// (password reset, member invite, portal magic link) stay plain text —
// they carry security-sensitive tokens and have no tenant branding
// context to begin with.
type Sender struct {
	host     string
	port     string
	username string
	password string
	from     string

	// hostedInvoiceBaseURL controls where email CTAs point for the
	// Stripe-parity hosted invoice page (T0-17). When unset, emails
	// still render branded HTML but without the pay/view CTA button.
	hostedInvoiceBaseURL string

	// settings, when non-nil, is consulted on every customer-facing
	// send to fetch the tenant's branding (logo, brand color, company
	// name, support URL, from-name). Nil => fall back to Velox defaults.
	settings SettingsGetter
}

// SettingsGetter resolves a tenantID to the tenant's public-facing
// settings. Satisfied by *tenant.SettingsStore at runtime; unit tests
// pass a fake.
type SettingsGetter interface {
	Get(ctx context.Context, tenantID string) (domain.TenantSettings, error)
}

// NewSender creates an email sender from environment variables. Returns
// a sender that logs instead of sending when SMTP is not configured, so
// `make dev` works without SMTP credentials.
func NewSender() *Sender {
	return &Sender{
		host:                 strings.TrimSpace(os.Getenv("SMTP_HOST")),
		port:                 envOr("SMTP_PORT", "587"),
		username:             strings.TrimSpace(os.Getenv("SMTP_USERNAME")),
		password:             strings.TrimSpace(os.Getenv("SMTP_PASSWORD")),
		from:                 envOr("SMTP_FROM", "billing@velox.dev"),
		hostedInvoiceBaseURL: strings.TrimSpace(os.Getenv("HOSTED_INVOICE_BASE_URL")),
	}
}

func (s *Sender) IsConfigured() bool { return s.host != "" }

// SetSettingsGetter wires the tenant settings store so Send* methods can
// look up branding per email. Called from router.go; optional for tests.
func (s *Sender) SetSettingsGetter(g SettingsGetter) { s.settings = g }

// brandingFor resolves branding for tenantID. Never errors — a missing
// tenant or a cold-start settings row just gets Velox defaults.
func (s *Sender) brandingFor(tenantID string) Branding {
	b := Branding{}
	if s.settings == nil || tenantID == "" {
		return b.fill()
	}
	// Fresh context per lookup — the send path is fire-and-forget from
	// an outbox worker, so we don't have a request ctx to inherit. 5s
	// bound so a slow tenant settings query doesn't stall email dispatch.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ts, err := s.settings.Get(ctx, tenantID)
	if err != nil {
		if !errors.Is(err, errs.ErrNotFound) {
			slog.Warn("email: branding lookup failed — falling back to defaults",
				"tenant_id", tenantID, "error", err)
		}
		return b.fill()
	}
	b = Branding{
		CompanyName: ts.CompanyName,
		LogoURL:     ts.LogoURL,
		BrandColor:  ts.BrandColor,
		SupportURL:  ts.SupportURL,
		FromAddress: ts.CompanyEmail,
	}
	return b.fill()
}

// hostedInvoiceURL returns the shareable hosted-invoice URL for the
// given public_token, or "" if either the token or the base URL is
// missing. Empty string means "no CTA button" at the render layer.
func (s *Sender) hostedInvoiceURL(publicToken string) string {
	if publicToken == "" || s.hostedInvoiceBaseURL == "" {
		return ""
	}
	return strings.TrimRight(s.hostedInvoiceBaseURL, "/") + "/invoice/" + publicToken
}

// ---- Customer-facing emails (HTML + plaintext multipart) ----

// SendInvoice sends an invoice email with PDF attachment. publicToken
// is the hosted_invoice_url credential (T0-17) — empty means no pay CTA.
func (s *Sender) SendInvoice(tenantID, to, customerName, invoiceNumber string, totalCents int64, currency string, pdfBytes []byte, publicToken string) error {
	brand := s.brandingFor(tenantID)
	hostedURL := s.hostedInvoiceURL(publicToken)
	amount := formatAmount(totalCents, currency)

	subject, contentHTML, ctaURL, ctaLabel := renderInvoiceHTML(customerName, invoiceNumber, amount, hostedURL)
	htmlBody, err := renderLayout(layoutInputs{
		Subject: subject, CompanyName: brand.CompanyName, LogoURL: brand.LogoURL,
		BrandColor: brand.BrandColor, SupportURL: brand.SupportURL,
		Content: template.HTML(contentHTML),
		CTAURL:  ctaURL, CTALabel: ctaLabel,
	})
	if err != nil {
		return fmt.Errorf("render invoice html: %w", err)
	}
	textBody := invoiceTextBody(customerName, invoiceNumber, amount, currency, hostedURL)

	return s.sendRich(richMessage{
		TenantID:       tenantID,
		To:             to,
		Subject:        subject,
		TextBody:       textBody,
		HTMLBody:       htmlBody,
		FromName:       brand.CompanyName,
		AttachmentName: invoiceNumber + ".pdf",
		AttachmentData: pdfBytes,
	})
}

// SendPaymentReceipt sends a receipt after successful payment.
func (s *Sender) SendPaymentReceipt(tenantID, to, customerName, invoiceNumber string, amountCents int64, currency, publicToken string) error {
	brand := s.brandingFor(tenantID)
	hostedURL := s.hostedInvoiceURL(publicToken)
	amount := formatAmount(amountCents, currency)

	subject, contentHTML, ctaURL, ctaLabel := renderReceiptHTML(customerName, invoiceNumber, amount, hostedURL)
	htmlBody, err := renderLayout(layoutInputs{
		Subject: subject, CompanyName: brand.CompanyName, LogoURL: brand.LogoURL,
		BrandColor: brand.BrandColor, SupportURL: brand.SupportURL,
		Content: template.HTML(contentHTML), CTAURL: ctaURL, CTALabel: ctaLabel,
	})
	if err != nil {
		return fmt.Errorf("render receipt html: %w", err)
	}
	textBody := receiptTextBody(customerName, invoiceNumber, amount, hostedURL)

	return s.sendRich(richMessage{
		TenantID: tenantID, To: to, Subject: subject,
		TextBody: textBody, HTMLBody: htmlBody, FromName: brand.CompanyName,
	})
}

// SendDunningWarning notifies a customer about a failed retry with a
// branded HTML body + "Update payment" CTA to the hosted invoice page.
func (s *Sender) SendDunningWarning(tenantID, to, customerName, invoiceNumber string, attemptNumber, maxAttempts int, nextRetryDate, publicToken string) error {
	brand := s.brandingFor(tenantID)
	hostedURL := s.hostedInvoiceURL(publicToken)

	subject, contentHTML, ctaURL, ctaLabel := renderDunningWarningHTML(customerName, invoiceNumber, attemptNumber, maxAttempts, nextRetryDate, hostedURL)
	htmlBody, err := renderLayout(layoutInputs{
		Subject: subject, CompanyName: brand.CompanyName, LogoURL: brand.LogoURL,
		BrandColor: brand.BrandColor, SupportURL: brand.SupportURL,
		Content: template.HTML(contentHTML), CTAURL: ctaURL, CTALabel: ctaLabel,
	})
	if err != nil {
		return fmt.Errorf("render dunning warning html: %w", err)
	}
	textBody := dunningWarningTextBody(customerName, invoiceNumber, attemptNumber, maxAttempts, nextRetryDate, hostedURL)

	return s.sendRich(richMessage{
		TenantID: tenantID, To: to, Subject: subject,
		TextBody: textBody, HTMLBody: htmlBody, FromName: brand.CompanyName,
	})
}

// SendDunningEscalation emails the "retries exhausted" escalation.
func (s *Sender) SendDunningEscalation(tenantID, to, customerName, invoiceNumber, action, publicToken string) error {
	brand := s.brandingFor(tenantID)
	hostedURL := s.hostedInvoiceURL(publicToken)

	subject, contentHTML, ctaURL, ctaLabel := renderDunningEscalationHTML(customerName, invoiceNumber, action, hostedURL)
	htmlBody, err := renderLayout(layoutInputs{
		Subject: subject, CompanyName: brand.CompanyName, LogoURL: brand.LogoURL,
		BrandColor: brand.BrandColor, SupportURL: brand.SupportURL,
		Content: template.HTML(contentHTML), CTAURL: ctaURL, CTALabel: ctaLabel,
	})
	if err != nil {
		return fmt.Errorf("render dunning escalation html: %w", err)
	}
	textBody := dunningEscalationTextBody(customerName, invoiceNumber, action, hostedURL)

	return s.sendRich(richMessage{
		TenantID: tenantID, To: to, Subject: subject,
		TextBody: textBody, HTMLBody: htmlBody, FromName: brand.CompanyName,
	})
}

// SendPaymentFailed notifies a customer about a failed charge.
func (s *Sender) SendPaymentFailed(tenantID, to, customerName, invoiceNumber, reason, publicToken string) error {
	brand := s.brandingFor(tenantID)
	hostedURL := s.hostedInvoiceURL(publicToken)

	subject, contentHTML, ctaURL, ctaLabel := renderPaymentFailedHTML(customerName, invoiceNumber, reason, hostedURL)
	htmlBody, err := renderLayout(layoutInputs{
		Subject: subject, CompanyName: brand.CompanyName, LogoURL: brand.LogoURL,
		BrandColor: brand.BrandColor, SupportURL: brand.SupportURL,
		Content: template.HTML(contentHTML), CTAURL: ctaURL, CTALabel: ctaLabel,
	})
	if err != nil {
		return fmt.Errorf("render payment failed html: %w", err)
	}
	textBody := paymentFailedTextBody(customerName, invoiceNumber, reason, hostedURL)

	return s.sendRich(richMessage{
		TenantID: tenantID, To: to, Subject: subject,
		TextBody: textBody, HTMLBody: htmlBody, FromName: brand.CompanyName,
	})
}

// SendPaymentUpdateRequest emails the tokenized payment-method-update
// link. CTA points at updateURL (NOT the hosted invoice URL — this flow
// changes the saved payment method, a separate concern).
func (s *Sender) SendPaymentUpdateRequest(tenantID, to, customerName, invoiceNumber string, amountDueCents int64, currency, updateURL string) error {
	brand := s.brandingFor(tenantID)
	amount := formatAmount(amountDueCents, currency)

	subject, contentHTML, ctaURL, ctaLabel := renderPaymentUpdateRequestHTML(customerName, invoiceNumber, amount, updateURL)
	htmlBody, err := renderLayout(layoutInputs{
		Subject: subject, CompanyName: brand.CompanyName, LogoURL: brand.LogoURL,
		BrandColor: brand.BrandColor, SupportURL: brand.SupportURL,
		Content: template.HTML(contentHTML), CTAURL: ctaURL, CTALabel: ctaLabel,
	})
	if err != nil {
		return fmt.Errorf("render payment update request html: %w", err)
	}
	textBody := paymentUpdateRequestTextBody(customerName, invoiceNumber, amount, updateURL)

	return s.sendRich(richMessage{
		TenantID: tenantID, To: to, Subject: subject,
		TextBody: textBody, HTMLBody: htmlBody, FromName: brand.CompanyName,
	})
}

// ---- Operator-facing emails (plain text; carry security tokens, no
// tenant branding context) ----

func (s *Sender) SendPasswordReset(tenantID, to, displayName, resetURL string) error {
	subject := "Reset your Velox password"
	body := fmt.Sprintf(`Hi %s,

We received a request to reset the password on your Velox account. Use the link below within the next hour to choose a new one:

%s

If you didn't request this, you can safely ignore this email — your current password keeps working, and nobody can sign in without the link.

— Velox
`, displayName, resetURL)
	return s.sendPlain(tenantID, to, "", subject, body)
}

func (s *Sender) SendMemberInvite(tenantID, to, inviterEmail, tenantName, acceptURL string) error {
	subject := fmt.Sprintf("You've been invited to %s on Velox", tenantName)
	body := fmt.Sprintf(`Hi,

%s invited you to join %s on Velox. Click the link below within the next 72 hours to set up your account:

%s

If you weren't expecting this invitation, you can safely ignore this email.

— Velox
`, inviterEmail, tenantName, acceptURL)
	return s.sendPlain(tenantID, to, "", subject, body)
}

func (s *Sender) SendPortalMagicLink(tenantID, to, customerName, magicLinkURL string) error {
	subject := "Your Velox customer portal sign-in link"
	body := fmt.Sprintf(`Hi %s,

Click the link below to sign in to your customer portal. It expires in 15 minutes and can only be used once:

%s

If you didn't request this, you can safely ignore this email — nobody can sign in without the link.

— Velox
`, customerName, magicLinkURL)
	return s.sendPlain(tenantID, to, "", subject, body)
}

// ---- Plaintext fallback bodies (multipart/alternative text part) ----

func invoiceTextBody(customerName, invoiceNumber, amount, currency, hostedURL string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Hi %s,\n\n", customerName)
	fmt.Fprintf(&b, "Invoice %s is ready.\n\nAmount due: %s\nCurrency: %s\n\n", invoiceNumber, amount, strings.ToUpper(currency))
	if hostedURL != "" {
		fmt.Fprintf(&b, "View or pay online: %s\n\n", hostedURL)
	}
	b.WriteString("The invoice PDF is attached for your records.\n")
	return b.String()
}

func receiptTextBody(customerName, invoiceNumber, amount, hostedURL string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Hi %s,\n\nThanks — we received your payment of %s for invoice %s.\n\n", customerName, amount, invoiceNumber)
	if hostedURL != "" {
		fmt.Fprintf(&b, "View receipt: %s\n", hostedURL)
	}
	return b.String()
}

func dunningWarningTextBody(customerName, invoiceNumber string, attemptNumber, maxAttempts int, nextRetryDate, hostedURL string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Hi %s,\n\nAttempt %d of %d to charge invoice %s was declined. We'll try again on %s.\n\n", customerName, attemptNumber, maxAttempts, invoiceNumber, nextRetryDate)
	if hostedURL != "" {
		fmt.Fprintf(&b, "Update your payment method: %s\n", hostedURL)
	}
	return b.String()
}

func dunningEscalationTextBody(customerName, invoiceNumber, action, hostedURL string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Hi %s,\n\nAll retry attempts for invoice %s have failed.\n\nAction taken: %s\n\n", customerName, invoiceNumber, action)
	if hostedURL != "" {
		fmt.Fprintf(&b, "Resolve invoice: %s\n", hostedURL)
	}
	return b.String()
}

func paymentFailedTextBody(customerName, invoiceNumber, reason, hostedURL string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Hi %s,\n\nWe couldn't process payment for invoice %s.\n", customerName, invoiceNumber)
	if strings.TrimSpace(reason) != "" {
		fmt.Fprintf(&b, "Reason: %s\n", reason)
	}
	if hostedURL != "" {
		fmt.Fprintf(&b, "\nUpdate payment method: %s\n", hostedURL)
	}
	return b.String()
}

func paymentUpdateRequestTextBody(customerName, invoiceNumber, amount, updateURL string) string {
	return fmt.Sprintf(`Hi %s,

We couldn't process payment for invoice %s (%s).

Update your payment method using the secure link below. It expires in 24 hours.

%s
`, customerName, invoiceNumber, amount, updateURL)
}

// ---- Low-level send helpers ----

// richMessage is a multipart/alternative-or-mixed email payload. When
// AttachmentData is set the envelope becomes multipart/mixed containing
// a multipart/alternative (text+html) part plus the attachment; otherwise
// it's multipart/alternative directly.
type richMessage struct {
	TenantID       string
	To             string
	Subject        string
	TextBody       string
	HTMLBody       string
	FromName       string
	AttachmentName string
	AttachmentData []byte
}

func (s *Sender) sendRich(msg richMessage) error {
	if !s.IsConfigured() {
		slog.Info("email (not sent — SMTP not configured)",
			"tenant_id", msg.TenantID, "to", msg.To, "subject", msg.Subject,
			"has_html", msg.HTMLBody != "", "attachment", msg.AttachmentName,
		)
		return nil
	}

	var body strings.Builder
	fromHeader := s.from
	if msg.FromName != "" {
		// RFC 5322 name-then-address. Special characters in FromName
		// would need quoting; for now assume tenant CompanyName is safe
		// ASCII (server-side validation enforces that).
		fromHeader = fmt.Sprintf("%s <%s>", msg.FromName, s.from)
	}
	fmt.Fprintf(&body, "From: %s\r\n", fromHeader)
	fmt.Fprintf(&body, "To: %s\r\n", msg.To)
	fmt.Fprintf(&body, "Subject: %s\r\n", msg.Subject)
	body.WriteString("MIME-Version: 1.0\r\n")

	altBoundary := "velox-alt-" + nonce()
	mixedBoundary := "velox-mix-" + nonce()

	if len(msg.AttachmentData) > 0 {
		fmt.Fprintf(&body, "Content-Type: multipart/mixed; boundary=\"%s\"\r\n\r\n", mixedBoundary)
		fmt.Fprintf(&body, "--%s\r\n", mixedBoundary)
	}

	// multipart/alternative wrapper for text + html
	fmt.Fprintf(&body, "Content-Type: multipart/alternative; boundary=\"%s\"\r\n\r\n", altBoundary)

	// text part
	fmt.Fprintf(&body, "--%s\r\n", altBoundary)
	body.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	body.WriteString("Content-Transfer-Encoding: 7bit\r\n\r\n")
	body.WriteString(msg.TextBody)
	body.WriteString("\r\n")

	// html part
	fmt.Fprintf(&body, "--%s\r\n", altBoundary)
	body.WriteString("Content-Type: text/html; charset=utf-8\r\n")
	body.WriteString("Content-Transfer-Encoding: 7bit\r\n\r\n")
	body.WriteString(msg.HTMLBody)
	body.WriteString("\r\n")

	fmt.Fprintf(&body, "--%s--\r\n", altBoundary)

	if len(msg.AttachmentData) > 0 {
		// PDF attachment part
		fmt.Fprintf(&body, "--%s\r\n", mixedBoundary)
		fmt.Fprintf(&body, "Content-Type: application/pdf; name=\"%s\"\r\n", msg.AttachmentName)
		body.WriteString("Content-Transfer-Encoding: base64\r\n")
		fmt.Fprintf(&body, "Content-Disposition: attachment; filename=\"%s\"\r\n\r\n", msg.AttachmentName)
		body.WriteString(wrapBase64(base64.StdEncoding.EncodeToString(msg.AttachmentData), 76))
		fmt.Fprintf(&body, "\r\n--%s--\r\n", mixedBoundary)
	}

	auth := smtp.PlainAuth("", s.username, s.password, s.host)
	addr := fmt.Sprintf("%s:%s", s.host, s.port)
	if err := smtp.SendMail(addr, auth, s.from, []string{msg.To}, []byte(body.String())); err != nil {
		slog.Error("send email failed", "to", msg.To, "subject", msg.Subject, "error", err)
		return fmt.Errorf("send email: %w", err)
	}
	slog.Info("email sent", "to", msg.To, "subject", msg.Subject, "html", true)
	return nil
}

// sendPlain is the text-only path for operator emails that stay simple.
func (s *Sender) sendPlain(tenantID, to, fromName, subject, body string) error {
	if !s.IsConfigured() {
		slog.Info("email (not sent — SMTP not configured)",
			"tenant_id", tenantID, "to", to, "subject", subject,
		)
		return nil
	}
	var msg strings.Builder
	fromHeader := s.from
	if fromName != "" {
		fromHeader = fmt.Sprintf("%s <%s>", fromName, s.from)
	}
	fmt.Fprintf(&msg, "From: %s\r\n", fromHeader)
	fmt.Fprintf(&msg, "To: %s\r\n", to)
	fmt.Fprintf(&msg, "Subject: %s\r\n", subject)
	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	msg.WriteString(body)

	auth := smtp.PlainAuth("", s.username, s.password, s.host)
	addr := fmt.Sprintf("%s:%s", s.host, s.port)
	if err := smtp.SendMail(addr, auth, s.from, []string{to}, []byte(msg.String())); err != nil {
		slog.Error("send email failed", "to", to, "subject", subject, "error", err)
		return fmt.Errorf("send email: %w", err)
	}
	slog.Info("email sent", "to", to, "subject", subject, "html", false)
	return nil
}

// ---- Utilities ----

func formatAmount(cents int64, currency string) string {
	return fmt.Sprintf("%s %d.%02d", strings.ToUpper(currency), cents/100, cents%100)
}

// wrapBase64 inserts CRLF every n chars. RFC 2045 recommends ≤76 per
// line; most clients accept longer but some classical relays don't.
func wrapBase64(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i += n {
		end := i + n
		if end > len(s) {
			end = len(s)
		}
		b.WriteString(s[i:end])
		b.WriteString("\r\n")
	}
	return strings.TrimRight(b.String(), "\r\n")
}

// nonce produces a short MIME-boundary-safe token. No cryptographic
// requirement — just uniqueness within a single message.
func nonce() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

func envOr(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}
