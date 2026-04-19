package email

import (
	"context"
	"fmt"
)

// Email type tags written into email_outbox.email_type. Kept stable because
// they live in row data; adding new types is append-only.
const (
	TypeInvoice              = "invoice"
	TypePaymentReceipt       = "payment_receipt"
	TypeDunningWarning       = "dunning_warning"
	TypeDunningEscalation    = "dunning_escalation"
	TypePaymentFailed        = "payment_failed"
	TypePaymentUpdateRequest = "payment_update_request"
)

// outboxMessage is the union payload persisted to email_outbox.payload. Each
// email_type populates the subset of fields it needs; unused fields stay zero.
// Keeping it a single struct (not a tagged union of N structs) avoids per-type
// serialisation ceremony — the dispatcher reads the type tag and knows which
// fields are meaningful.
type outboxMessage struct {
	To             string `json:"to"`
	CustomerName   string `json:"customer_name,omitempty"`
	InvoiceNumber  string `json:"invoice_number,omitempty"`
	AmountCents    int64  `json:"amount_cents,omitempty"`
	Currency       string `json:"currency,omitempty"`
	AttemptNumber  int    `json:"attempt_number,omitempty"`
	MaxAttempts    int    `json:"max_attempts,omitempty"`
	NextRetryDate  string `json:"next_retry_date,omitempty"`
	Action         string `json:"action,omitempty"`
	Reason         string `json:"reason,omitempty"`
	UpdateURL      string `json:"update_url,omitempty"`
	PDF            []byte `json:"pdf,omitempty"`
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

func (s *OutboxSender) enqueue(tenantID, emailType string, msg outboxMessage) error {
	if tenantID == "" {
		return fmt.Errorf("email outbox sender: tenant_id required for %s", emailType)
	}
	payload := map[string]any{
		"to":              msg.To,
		"customer_name":   msg.CustomerName,
		"invoice_number":  msg.InvoiceNumber,
		"amount_cents":    msg.AmountCents,
		"currency":        msg.Currency,
		"attempt_number":  msg.AttemptNumber,
		"max_attempts":    msg.MaxAttempts,
		"next_retry_date": msg.NextRetryDate,
		"action":          msg.Action,
		"reason":          msg.Reason,
		"update_url":      msg.UpdateURL,
	}
	if len(msg.PDF) > 0 {
		payload["pdf"] = msg.PDF
	}
	_, err := s.store.EnqueueStandalone(context.Background(), tenantID, emailType, payload)
	return err
}

// SendInvoice enqueues an invoice email. Satisfies invoice.EmailSender.
func (s *OutboxSender) SendInvoice(tenantID, to, customerName, invoiceNumber string, totalCents int64, currency string, pdfBytes []byte) error {
	return s.enqueue(tenantID, TypeInvoice, outboxMessage{
		To:            to,
		CustomerName:  customerName,
		InvoiceNumber: invoiceNumber,
		AmountCents:   totalCents,
		Currency:      currency,
		PDF:           pdfBytes,
	})
}

// SendPaymentReceipt enqueues a receipt email. Satisfies payment.EmailReceipt.
func (s *OutboxSender) SendPaymentReceipt(tenantID, to, customerName, invoiceNumber string, amountCents int64, currency string) error {
	return s.enqueue(tenantID, TypePaymentReceipt, outboxMessage{
		To:            to,
		CustomerName:  customerName,
		InvoiceNumber: invoiceNumber,
		AmountCents:   amountCents,
		Currency:      currency,
	})
}

// SendDunningWarning enqueues a dunning-warning email. Satisfies dunning.EmailNotifier.
func (s *OutboxSender) SendDunningWarning(tenantID, to, customerName, invoiceNumber string, attemptNumber, maxAttempts int, nextRetryDate string) error {
	return s.enqueue(tenantID, TypeDunningWarning, outboxMessage{
		To:            to,
		CustomerName:  customerName,
		InvoiceNumber: invoiceNumber,
		AttemptNumber: attemptNumber,
		MaxAttempts:   maxAttempts,
		NextRetryDate: nextRetryDate,
	})
}

// SendDunningEscalation enqueues a dunning-escalation email. Satisfies dunning.EmailNotifier.
func (s *OutboxSender) SendDunningEscalation(tenantID, to, customerName, invoiceNumber string, action string) error {
	return s.enqueue(tenantID, TypeDunningEscalation, outboxMessage{
		To:            to,
		CustomerName:  customerName,
		InvoiceNumber: invoiceNumber,
		Action:        action,
	})
}

// SendPaymentFailed enqueues a payment-failed email. Satisfies dunning.EmailNotifier.
func (s *OutboxSender) SendPaymentFailed(tenantID, to, customerName, invoiceNumber, reason string) error {
	return s.enqueue(tenantID, TypePaymentFailed, outboxMessage{
		To:            to,
		CustomerName:  customerName,
		InvoiceNumber: invoiceNumber,
		Reason:        reason,
	})
}

// SendPaymentUpdateRequest enqueues a payment-update-request email. Satisfies
// payment.EmailPaymentUpdate.
func (s *OutboxSender) SendPaymentUpdateRequest(tenantID, to, customerName, invoiceNumber string, amountDueCents int64, currency, updateURL string) error {
	return s.enqueue(tenantID, TypePaymentUpdateRequest, outboxMessage{
		To:            to,
		CustomerName:  customerName,
		InvoiceNumber: invoiceNumber,
		AmountCents:   amountDueCents,
		Currency:      currency,
		UpdateURL:     updateURL,
	})
}
