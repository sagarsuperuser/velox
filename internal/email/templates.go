package email

import (
	"bytes"
	"html"
	"html/template"
	"strings"
)

// Branding is the per-tenant visual identity a sender pulls from
// TenantSettings before rendering an outbound email. Every field is
// optional — NewSender falls back to Velox defaults when a tenant
// hasn't set their own (T0-11 onboarding surfaces this in the
// dashboard, but cold-start tenants will still send branded-enough
// email).
type Branding struct {
	CompanyName string
	LogoURL     string
	BrandColor  string // 7-char hex, e.g. #1f6feb. Empty = fall back to neutral.
	SupportURL  string
	FromName    string // Appears as "From: FromName <from@address>" when set.
	FromAddress string // Defaults to SMTP_FROM at send time.
}

// fill fills in Velox defaults so every rendered email has something in
// each slot; keeps the templates simple (no "if empty" branches) at the
// cost of one method call per send.
func (b Branding) fill() Branding {
	if b.CompanyName == "" {
		b.CompanyName = "Velox Billing"
	}
	// BrandColor / LogoURL / SupportURL stay empty when unset — templates
	// {{if}}-gate their renders so "no brand color" means no accent bar,
	// and "no logo" means no <img> tag. Defensive fallbacks would look
	// weirder than absence.
	return b
}

// layoutTemplate is the shared chrome every customer-facing HTML email
// wraps around its specific content. Mirrors the hosted invoice page
// aesthetic (T0-17.5) so a customer landing on the page from an email
// CTA sees visual continuity. Inline styles throughout because email
// clients (Outlook especially) strip <style> blocks inconsistently —
// industry best practice is inline everything.
//
// Structure:
//   - optional 3px brand_color accent bar at the very top
//   - header: tenant logo + company name
//   - content: per-email HTML (injected as template.HTML, pre-rendered)
//   - optional CTA button
//   - footer: support link + "Powered by Velox Billing" micro-credit
const layoutHTML = `<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{.Subject}}</title>
</head>
<body style="margin:0;padding:0;background-color:#f5f6f8;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;color:#1f2937;">
  <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background-color:#f5f6f8;">
    <tr>
      <td align="center" style="padding:32px 16px;">
        <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:600px;background:#ffffff;border-radius:8px;overflow:hidden;box-shadow:0 1px 3px rgba(0,0,0,0.04);">
          {{if .BrandColor}}<tr><td style="height:3px;background-color:{{.BrandColor}};line-height:0;font-size:0;">&nbsp;</td></tr>{{end}}
          <tr>
            <td style="padding:24px 32px;border-bottom:1px solid #e5e7eb;">
              <table role="presentation" cellpadding="0" cellspacing="0">
                <tr>
                  {{if .LogoURL}}<td style="padding-right:12px;vertical-align:middle;"><img src="{{.LogoURL}}" alt="" height="32" style="display:block;max-height:32px;"></td>{{end}}
                  <td style="vertical-align:middle;font-size:16px;font-weight:600;color:#111827;">{{.CompanyName}}</td>
                </tr>
              </table>
            </td>
          </tr>
          <tr>
            <td style="padding:32px;font-size:15px;line-height:1.6;color:#1f2937;">
              {{.Content}}
              {{if .CTAURL}}
              <table role="presentation" cellpadding="0" cellspacing="0" style="margin:24px 0 8px;">
                <tr>
                  <td style="border-radius:6px;background-color:{{if .BrandColor}}{{.BrandColor}}{{else}}#111827{{end}};">
                    <a href="{{.CTAURL}}" style="display:inline-block;padding:12px 24px;color:#ffffff;text-decoration:none;font-weight:500;font-size:15px;">{{.CTALabel}}</a>
                  </td>
                </tr>
              </table>
              {{end}}
            </td>
          </tr>
          <tr>
            <td style="padding:20px 32px;border-top:1px solid #e5e7eb;font-size:13px;color:#6b7280;">
              {{if .SupportURL}}Need help? <a href="{{.SupportURL}}" style="color:#6b7280;text-decoration:underline;">Contact support</a><br>{{end}}
              <span style="color:#9ca3af;">Powered by Velox Billing</span>
            </td>
          </tr>
        </table>
      </td>
    </tr>
  </table>
</body>
</html>`

// layoutInputs is the view model fed into layoutHTML. Content is
// pre-rendered HTML (template.HTML marks it pre-escaped) so per-email
// templates can inject their own safe markup without html/template
// double-escaping it.
type layoutInputs struct {
	Subject     string
	CompanyName string
	LogoURL     string
	BrandColor  string
	SupportURL  string
	Content     template.HTML
	CTAURL      string
	CTALabel    string
}

var layoutTmpl = template.Must(template.New("layout").Parse(layoutHTML))

// renderLayout assembles the final HTML email by wrapping pre-rendered
// content in the shared chrome.
func renderLayout(inputs layoutInputs) (string, error) {
	var buf bytes.Buffer
	if err := layoutTmpl.Execute(&buf, inputs); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// escape wraps html.EscapeString so per-email helpers can build
// template.HTML content without dragging in the full html/template
// pipeline for single-field interpolation.
func escape(s string) string { return html.EscapeString(s) }

// --- Per-email content builders ---

// Each builder returns a (subject, html-content, cta-url, cta-label)
// tuple. The caller renders through renderLayout then ships via the
// multipart pipeline alongside the plaintext fallback.

func renderInvoiceHTML(customerName, invoiceNumber, amount string, hostedURL string) (subject, contentHTML, ctaURL, ctaLabel string) {
	subject = "Invoice " + invoiceNumber + " — " + amount
	var b strings.Builder
	b.WriteString(`<h1 style="margin:0 0 12px;font-size:20px;color:#111827;">Your invoice is ready</h1>`)
	b.WriteString(`<p style="margin:0 0 8px;color:#4b5563;">Hi ` + escape(customerName) + `,</p>`)
	b.WriteString(`<p style="margin:0 0 20px;color:#4b5563;">Invoice <strong style="color:#111827;">` + escape(invoiceNumber) + `</strong> is now available.</p>`)
	b.WriteString(`<div style="background:#f9fafb;border-radius:8px;padding:16px 20px;margin:8px 0 16px;">`)
	b.WriteString(`<div style="font-size:13px;color:#6b7280;text-transform:uppercase;letter-spacing:0.05em;">Amount due</div>`)
	b.WriteString(`<div style="font-size:24px;font-weight:600;color:#111827;margin-top:4px;font-variant-numeric:tabular-nums;">` + escape(amount) + `</div>`)
	b.WriteString(`</div>`)
	if hostedURL != "" {
		b.WriteString(`<p style="margin:0 0 16px;color:#4b5563;">Tap the button below to view or pay your invoice. The PDF is also attached for your records.</p>`)
		ctaURL = hostedURL
		ctaLabel = "View & pay invoice"
	} else {
		b.WriteString(`<p style="margin:0 0 16px;color:#4b5563;">Please find the invoice PDF attached.</p>`)
	}
	return subject, b.String(), ctaURL, ctaLabel
}

func renderReceiptHTML(customerName, invoiceNumber, amount, hostedURL string) (subject, contentHTML, ctaURL, ctaLabel string) {
	subject = "Payment received for invoice " + invoiceNumber
	var b strings.Builder
	b.WriteString(`<h1 style="margin:0 0 12px;font-size:20px;color:#111827;">Payment received</h1>`)
	b.WriteString(`<p style="margin:0 0 8px;color:#4b5563;">Hi ` + escape(customerName) + `,</p>`)
	b.WriteString(`<p style="margin:0 0 20px;color:#4b5563;">Thanks — we've received your payment of <strong style="color:#111827;">` + escape(amount) + `</strong> for invoice <strong style="color:#111827;">` + escape(invoiceNumber) + `</strong>.</p>`)
	if hostedURL != "" {
		ctaURL = hostedURL
		ctaLabel = "View receipt"
	}
	return subject, b.String(), ctaURL, ctaLabel
}

func renderDunningWarningHTML(customerName, invoiceNumber string, attemptNumber, maxAttempts int, nextRetryDate, failureReason, hostedURL string) (subject, contentHTML, ctaURL, ctaLabel string) {
	// Final-attempt branch: switch tone + subject so the customer
	// understands this is the last automatic charge before a service-
	// impacting action. Prevents the silent-spiral where every retry
	// uses the same template and the customer never realises time is
	// running out.
	finalAttempt := maxAttempts > 0 && attemptNumber >= maxAttempts
	if finalAttempt {
		subject = "Last attempt — pay invoice " + invoiceNumber + " to keep service active"
	} else {
		subject = "Action required — payment retry for invoice " + invoiceNumber
	}
	var b strings.Builder
	if finalAttempt {
		b.WriteString(`<h1 style="margin:0 0 12px;font-size:20px;color:#b91c1c;">Last attempt — please update your payment</h1>`)
	} else {
		b.WriteString(`<h1 style="margin:0 0 12px;font-size:20px;color:#111827;">We weren't able to process your payment</h1>`)
	}
	b.WriteString(`<p style="margin:0 0 8px;color:#4b5563;">Hi ` + escape(customerName) + `,</p>`)
	b.WriteString(`<p style="margin:0 0 12px;color:#4b5563;">Attempt <strong style="color:#111827;">` + escape(itoa(attemptNumber)) + ` of ` + escape(itoa(maxAttempts)) + `</strong> to charge invoice <strong style="color:#111827;">` + escape(invoiceNumber) + `</strong> was declined.</p>`)
	// Surface the decline reason inline so customers can act
	// (insufficient_funds → top up; lost_card → swap card). Empty
	// reason = no callout, no false noise.
	if strings.TrimSpace(failureReason) != "" {
		b.WriteString(`<div style="background:#fef2f2;border-left:3px solid #fca5a5;padding:10px 14px;margin:0 0 16px;color:#7f1d1d;font-size:13px;">`)
		b.WriteString(`Reason: ` + escape(failureReason))
		b.WriteString(`</div>`)
	}
	if finalAttempt {
		b.WriteString(`<p style="margin:0 0 16px;color:#4b5563;">This was the final automatic retry. If we can't collect this invoice, your subscription may be paused or canceled. Please pay the invoice or update your payment method now.</p>`)
	} else {
		b.WriteString(`<p style="margin:0 0 16px;color:#4b5563;">We'll try again on <strong style="color:#111827;">` + escape(nextRetryDate) + `</strong>. To avoid further retries, please pay the invoice or update your payment method.</p>`)
	}
	if hostedURL != "" {
		ctaURL = hostedURL
		ctaLabel = "Pay invoice"
	}
	return subject, b.String(), ctaURL, ctaLabel
}

func renderDunningEscalationHTML(customerName, invoiceNumber, action, hostedURL string) (subject, contentHTML, ctaURL, ctaLabel string) {
	subject = "Payment retries exhausted for invoice " + invoiceNumber
	var b strings.Builder
	b.WriteString(`<h1 style="margin:0 0 12px;font-size:20px;color:#111827;">Payment retries exhausted</h1>`)
	b.WriteString(`<p style="margin:0 0 8px;color:#4b5563;">Hi ` + escape(customerName) + `,</p>`)
	b.WriteString(`<p style="margin:0 0 16px;color:#4b5563;">All retry attempts for invoice <strong style="color:#111827;">` + escape(invoiceNumber) + `</strong> have failed.</p>`)
	b.WriteString(`<p style="margin:0 0 20px;color:#4b5563;">Action taken: <strong style="color:#111827;">` + escape(action) + `</strong></p>`)
	b.WriteString(`<p style="margin:0 0 16px;color:#4b5563;">To resume service, please settle the invoice using the link below or reach out to support.</p>`)
	if hostedURL != "" {
		ctaURL = hostedURL
		ctaLabel = "Resolve invoice"
	}
	return subject, b.String(), ctaURL, ctaLabel
}

func renderPaymentFailedHTML(customerName, invoiceNumber, reason, hostedURL string) (subject, contentHTML, ctaURL, ctaLabel string) {
	subject = "Payment failed for invoice " + invoiceNumber
	var b strings.Builder
	b.WriteString(`<h1 style="margin:0 0 12px;font-size:20px;color:#111827;">Payment unsuccessful</h1>`)
	b.WriteString(`<p style="margin:0 0 8px;color:#4b5563;">Hi ` + escape(customerName) + `,</p>`)
	b.WriteString(`<p style="margin:0 0 16px;color:#4b5563;">We weren't able to process the charge for invoice <strong style="color:#111827;">` + escape(invoiceNumber) + `</strong>.</p>`)
	if strings.TrimSpace(reason) != "" {
		b.WriteString(`<div style="background:#fef2f2;border-left:3px solid #fca5a5;padding:12px 16px;margin:8px 0 16px;color:#7f1d1d;font-size:14px;">` + escape(reason) + `</div>`)
	}
	b.WriteString(`<p style="margin:0 0 16px;color:#4b5563;">Pay the invoice or update your payment method to avoid service interruption.</p>`)
	if hostedURL != "" {
		ctaURL = hostedURL
		ctaLabel = "Pay invoice"
	}
	return subject, b.String(), ctaURL, ctaLabel
}

// renderPaymentUpdateRequestHTML is the invoice-context thin wrapper
// retained for back-compat with the SendPaymentSetupRequest call
// site. Delegates to the unified renderPaymentSetupLinkHTML so the
// template stays single-source.
func renderPaymentUpdateRequestHTML(customerName, invoiceNumber, amountDue, updateURL string) (subject, contentHTML, ctaURL, ctaLabel string) {
	return renderPaymentSetupLinkHTML(paymentSetupLinkContext{
		CustomerName:   customerName,
		SetupURL:       updateURL,
		InvoiceNumber:  invoiceNumber,
		AmountDueLabel: amountDue,
	})
}

// paymentSetupLinkContext is the unified shape for both
// operator-initiated and engine-fired "add a payment method" emails.
// Invoice fields are optional: when InvoiceNumber is empty the
// template renders the generic operator-initiated copy; when set
// it embeds invoice context ("for invoice X — $Y") so the customer
// reads exactly why they need to add a card.
type paymentSetupLinkContext struct {
	CustomerName   string
	OperatorNote   string // optional free-form operator note
	SetupURL       string
	InvoiceNumber  string // optional — engine-fired no-PM-at-finalize path
	AmountDueLabel string // formatted ($X.XX); empty when InvoiceNumber empty
}

// renderPaymentSetupLinkHTML is the single template used by every
// "add a payment method" email send (operator-initiated +
// engine-fired no-PM-at-finalize). Replaces the previous two-
// template split (renderPaymentUpdateRequestHTML +
// renderPaymentSetupLinkHTML) so subject + body stay consistent
// across send paths. Industry parity: Stripe "Send Payment Method",
// Chargebee "Request Payment Method" — both use one template
// internally with optional invoice context.
func renderPaymentSetupLinkHTML(ctx paymentSetupLinkContext) (subject, contentHTML, ctaURL, ctaLabel string) {
	hasInvoice := ctx.InvoiceNumber != ""
	if hasInvoice {
		subject = "Action required — update payment for invoice " + ctx.InvoiceNumber
	} else {
		subject = "Add a payment method"
	}
	var b strings.Builder
	heading := "Add a payment method"
	if hasInvoice {
		heading = "Update your payment method"
	}
	b.WriteString(`<h1 style="margin:0 0 12px;font-size:20px;color:#111827;">` + escape(heading) + `</h1>`)
	b.WriteString(`<p style="margin:0 0 8px;color:#4b5563;">Hi ` + escape(ctx.CustomerName) + `,</p>`)
	switch {
	case ctx.OperatorNote != "":
		// Operator's custom note renders verbatim — they may have
		// added billing context ("your card on file expired") that
		// the template shouldn't try to autocompose.
		b.WriteString(`<p style="margin:0 0 16px;color:#4b5563;">` + escape(ctx.OperatorNote) + `</p>`)
	case hasInvoice:
		b.WriteString(`<p style="margin:0 0 8px;color:#4b5563;">We couldn't process payment for invoice <strong style="color:#111827;">` + escape(ctx.InvoiceNumber) + `</strong> (<strong style="color:#111827;">` + escape(ctx.AmountDueLabel) + `</strong>).</p>`)
		b.WriteString(`<p style="margin:0 0 16px;color:#4b5563;">Use the secure link below to add or replace your payment method.</p>`)
	default:
		b.WriteString(`<p style="margin:0 0 16px;color:#4b5563;">Please add a payment method on file so we can process your billing. Use the secure link below — your card details go directly to our payment processor and never touch our servers.</p>`)
	}
	b.WriteString(`<p style="margin:0 0 16px;color:#6b7280;font-size:13px;">The link expires in 24 hours and can only be used once.</p>`)
	ctaURL = ctx.SetupURL
	if hasInvoice {
		ctaLabel = "Update payment method"
	} else {
		ctaLabel = "Add payment method"
	}
	return subject, b.String(), ctaURL, ctaLabel
}

// itoa is a local shim to avoid pulling strconv just for escape() input.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
