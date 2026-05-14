package email

import (
	"context"
	"fmt"
)

// Email type tags written into email_outbox.email_type. Kept stable because
// they live in row data; adding new types is append-only.
//
// payment_setup_request: customer needs to set up a payment method on a
//   finalized invoice (no PM on file at finalize). Distinct from
//   payment_failed which means a charge attempt actually went to Stripe
//   and was declined.
// payment_failed: a Stripe charge attempt failed (decline, insufficient
//   funds, etc.). Used for both immediate post-decline notifications
//   AND dunning retry escalations.
const (
	TypeInvoice             = "invoice"
	TypePaymentReceipt      = "payment_receipt"
	TypeDunningWarning      = "dunning_warning"
	TypeDunningEscalation   = "dunning_escalation"
	TypePaymentFailed       = "payment_failed"
	TypePaymentSetupRequest = "payment_setup_request"
	TypePortalMagicLink     = "portal_magic_link"
	TypePasswordReset       = "password_reset"
	TypeMemberInvite        = "member_invite"
)

// outboxMessage is the union payload persisted to email_outbox.payload. Each
// email_type populates the subset of fields it needs; unused fields stay zero.
// Keeping it a single struct (not a tagged union of N structs) avoids per-type
// serialisation ceremony — the dispatcher reads the type tag and knows which
// fields are meaningful.
type outboxMessage struct {
	To               string `json:"to"`
	CustomerName     string `json:"customer_name,omitempty"`
	InvoiceNumber    string `json:"invoice_number,omitempty"`
	AmountCents      int64  `json:"amount_cents,omitempty"`
	Currency         string `json:"currency,omitempty"`
	AttemptNumber    int    `json:"attempt_number,omitempty"`
	MaxAttempts      int    `json:"max_attempts,omitempty"`
	NextRetryDate    string `json:"next_retry_date,omitempty"`
	Action           string `json:"action,omitempty"`
	Reason           string `json:"reason,omitempty"`
	// FailureReason carries the latest decline-or-error message for
	// dunning_warning + payment_failed templates. Surfaced inline so
	// the customer can act (insufficient_funds → top up; lost_card →
	// swap card). Distinct from Reason which is used by dunning_
	// escalation to carry the final-action verb.
	FailureReason    string `json:"failure_reason,omitempty"`
	UpdateURL        string `json:"update_url,omitempty"`
	MagicLinkURL     string `json:"magic_link_url,omitempty"`
	PasswordResetURL string `json:"password_reset_url,omitempty"`
	InviteURL        string `json:"invite_url,omitempty"`
	InviterEmail     string `json:"inviter_email,omitempty"`
	TenantName       string `json:"tenant_name,omitempty"`
	// PublicToken is the Stripe-equivalent hosted_invoice_url credential
	// (T0-17). Carried on invoice-related email types so the dispatcher
	// can build the hosted-invoice CTA URL for the rendered HTML body.
	PublicToken string `json:"public_token,omitempty"`
	PDF         []byte `json:"pdf,omitempty"`
}

// OutboxSender satisfies the four domain email interfaces (invoice.EmailSender,
// dunning.EmailNotifier, payment.EmailReceipt, payment.EmailPaymentUpdate) by
// enqueueing a row into email_outbox instead of calling SMTP directly. The
// dispatcher drains the queue asynchronously.
//
// It requires a tenantID on every call because the outbox is tenant-scoped
// via RLS (and operators need per-tenant visibility on failures). Callers
// already know the tenant at every site — they just weren't passing it
// through.
type OutboxSender struct {
	store *OutboxStore
}

func NewOutboxSender(store *OutboxStore) *OutboxSender {
	return &OutboxSender{store: store}
}

func (s *OutboxSender) enqueue(ctx context.Context, tenantID, emailType string, msg outboxMessage) error {
	if tenantID == "" {
		return fmt.Errorf("email outbox sender: tenant_id required for %s", emailType)
	}
	payload := map[string]any{
		"to":                 msg.To,
		"customer_name":      msg.CustomerName,
		"invoice_number":     msg.InvoiceNumber,
		"amount_cents":       msg.AmountCents,
		"currency":           msg.Currency,
		"attempt_number":     msg.AttemptNumber,
		"max_attempts":       msg.MaxAttempts,
		"next_retry_date":    msg.NextRetryDate,
		"action":             msg.Action,
		"reason":             msg.Reason,
		"failure_reason":     msg.FailureReason,
		"update_url":         msg.UpdateURL,
		"magic_link_url":     msg.MagicLinkURL,
		"password_reset_url": msg.PasswordResetURL,
		"invite_url":         msg.InviteURL,
		"inviter_email":      msg.InviterEmail,
		"tenant_name":        msg.TenantName,
		"public_token":       msg.PublicToken,
	}
	if len(msg.PDF) > 0 {
		payload["pdf"] = msg.PDF
	}
	// ctx must carry livemode (set by caller's auth middleware or, for
	// system-initiated emails fired from background workers, derived
	// from the originating row). EnqueueStandalone opens TxTenant; the
	// email_outbox BEFORE INSERT trigger reads app.livemode from the
	// session and stamps it on the row, so emails enqueued in test mode
	// land on test-mode rows and the dispatcher routes them correctly.
	// Pre-fix this used context.Background() and every email_outbox row
	// got stamped livemode=true regardless of the actual mode.
	_, err := s.store.EnqueueStandalone(ctx, tenantID, emailType, payload)
	return err
}

// SendInvoice enqueues an invoice email. Satisfies invoice.EmailSender.
func (s *OutboxSender) SendInvoice(ctx context.Context, tenantID, to, customerName, invoiceNumber string, totalCents int64, currency string, pdfBytes []byte, publicToken string) error {
	return s.enqueue(ctx, tenantID, TypeInvoice, outboxMessage{
		To:            to,
		CustomerName:  customerName,
		InvoiceNumber: invoiceNumber,
		AmountCents:   totalCents,
		Currency:      currency,
		PublicToken:   publicToken,
		PDF:           pdfBytes,
	})
}

// SendPaymentReceipt enqueues a receipt email. Satisfies payment.EmailReceipt.
func (s *OutboxSender) SendPaymentReceipt(ctx context.Context, tenantID, to, customerName, invoiceNumber string, amountCents int64, currency, publicToken string) error {
	return s.enqueue(ctx, tenantID, TypePaymentReceipt, outboxMessage{
		To:            to,
		CustomerName:  customerName,
		InvoiceNumber: invoiceNumber,
		AmountCents:   amountCents,
		Currency:      currency,
		PublicToken:   publicToken,
	})
}

// SendDunningWarning enqueues a dunning-warning email. Satisfies dunning.EmailNotifier.
func (s *OutboxSender) SendDunningWarning(ctx context.Context, tenantID, to, customerName, invoiceNumber string, attemptNumber, maxAttempts int, nextRetryDate, failureReason, publicToken string) error {
	return s.enqueue(ctx, tenantID, TypeDunningWarning, outboxMessage{
		To:            to,
		CustomerName:  customerName,
		InvoiceNumber: invoiceNumber,
		AttemptNumber: attemptNumber,
		MaxAttempts:   maxAttempts,
		NextRetryDate: nextRetryDate,
		FailureReason: failureReason,
		PublicToken:   publicToken,
	})
}

// SendDunningEscalation enqueues a dunning-escalation email. Satisfies dunning.EmailNotifier.
func (s *OutboxSender) SendDunningEscalation(ctx context.Context, tenantID, to, customerName, invoiceNumber, action, publicToken string) error {
	return s.enqueue(ctx, tenantID, TypeDunningEscalation, outboxMessage{
		To:            to,
		CustomerName:  customerName,
		InvoiceNumber: invoiceNumber,
		Action:        action,
		PublicToken:   publicToken,
	})
}

// SendPaymentFailed enqueues a payment-failed email. Satisfies dunning.EmailNotifier.
func (s *OutboxSender) SendPaymentFailed(ctx context.Context, tenantID, to, customerName, invoiceNumber, reason, publicToken string) error {
	return s.enqueue(ctx, tenantID, TypePaymentFailed, outboxMessage{
		To:            to,
		CustomerName:  customerName,
		InvoiceNumber: invoiceNumber,
		Reason:        reason,
		PublicToken:   publicToken,
	})
}

// SendPaymentSetupRequest enqueues a payment-setup-request email
// (customer must set up a PM on a finalized invoice).
func (s *OutboxSender) SendPaymentSetupRequest(ctx context.Context, tenantID, to, customerName, invoiceNumber string, amountDueCents int64, currency, updateURL string) error {
	return s.enqueue(ctx, tenantID, TypePaymentSetupRequest, outboxMessage{
		To:            to,
		CustomerName:  customerName,
		InvoiceNumber: invoiceNumber,
		AmountCents:   amountDueCents,
		Currency:      currency,
		UpdateURL:     updateURL,
	})
}

// SendPortalMagicLink enqueues a portal magic-link email. Satisfies
// customerportal.MagicLinkEmailSender (narrow interface at the wiring
// layer). The URL carries the one-time-use raw token that lands the
// customer at the frontend /login page for consumption.
func (s *OutboxSender) SendPortalMagicLink(ctx context.Context, tenantID, to, customerName, magicLinkURL string) error {
	return s.enqueue(ctx, tenantID, TypePortalMagicLink, outboxMessage{
		To:           to,
		CustomerName: customerName,
		MagicLinkURL: magicLinkURL,
	})
}

// SendPasswordReset enqueues a password-reset email. The URL carries the
// one-time-use raw reset token that lands the dashboard user on the
// frontend password-reset confirmation page.
func (s *OutboxSender) SendPasswordReset(ctx context.Context, tenantID, to, displayName, resetURL string) error {
	return s.enqueue(ctx, tenantID, TypePasswordReset, outboxMessage{
		To:               to,
		CustomerName:     displayName,
		PasswordResetURL: resetURL,
	})
}

// SendMemberInvite enqueues a team-invite email. The URL carries the
// raw invite token. Unlike the other flows this is tenant-initiated
// (inviter sits inside tenantID) rather than system-initiated.
func (s *OutboxSender) SendMemberInvite(ctx context.Context, tenantID, to, inviterEmail, tenantName, acceptURL string) error {
	return s.enqueue(ctx, tenantID, TypeMemberInvite, outboxMessage{
		To:           to,
		InviterEmail: inviterEmail,
		TenantName:   tenantName,
		InviteURL:    acceptURL,
	})
}
