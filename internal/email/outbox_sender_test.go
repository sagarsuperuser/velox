package email

import (
	"testing"

	"github.com/sagarsuperuser/velox/internal/dunning"
	"github.com/sagarsuperuser/velox/internal/invoice"
	"github.com/sagarsuperuser/velox/internal/payment"
)

// TestOutboxSender_SatisfiesAllInterfaces is a compile-time guarantee that
// OutboxSender is a drop-in replacement for *Sender across every domain that
// consumes email. If a new method is added to any of these interfaces without
// a matching OutboxSender method, this assertion fails to compile — which is
// exactly the signal we want, since a missed method would silently bypass the
// outbox for one email type.
func TestOutboxSender_SatisfiesAllInterfaces(t *testing.T) {
	t.Parallel()
	var s *OutboxSender
	var (
		_ invoice.EmailSender        = s
		_ dunning.EmailNotifier      = s
		_ payment.EmailReceipt       = s
		_ payment.EmailPaymentUpdate = s
	)
}

// TestOutboxSender_RequiresTenantID guards the contract: every enqueue must
// carry a tenant_id (enforced by the RLS policy on email_outbox and by
// outbox.Enqueue). A caller that passes "" should error rather than silently
// writing orphan rows — that would break operator visibility.
func TestOutboxSender_RequiresTenantID(t *testing.T) {
	t.Parallel()
	// A nil store is fine — the tenantID check short-circuits before any DB
	// access, so we never reach the store call.
	s := NewOutboxSender(nil)

	err := s.SendInvoice("", "to@x.com", "n", "inv", 1, "USD", nil, "")
	if err == nil {
		t.Error("SendInvoice with empty tenantID should error")
	}
}
