package email

import (
	"context"
	"errors"
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
		_ payment.EmailPaymentFailed = s
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

	err := s.SendInvoice(context.Background(), "", "to@x.com", nil, "n", "inv", 1, "USD", nil, "")
	if err == nil {
		t.Error("SendInvoice with empty tenantID should error")
	}
}

// TestOutboxSender_SuppressedRecipient verifies that when the
// suppression checker reports a recipient is bounced/complained, the
// enqueue is soft-skipped with ErrRecipientSuppressed and no DB write
// is attempted. Nil store proves no write happened (would panic).
func TestOutboxSender_SuppressedRecipient(t *testing.T) {
	t.Parallel()
	s := NewOutboxSender(nil)
	s.SetSuppressionChecker(suppressionFake{suppressed: true, reason: "bounced: 550 user unknown"})

	err := s.SendInvoice(context.Background(), "tnt_a", "bounced@x.com", nil, "n", "inv", 1, "USD", nil, "")
	if !errors.Is(err, ErrRecipientSuppressed) {
		t.Errorf("expected ErrRecipientSuppressed; got %v", err)
	}
}

// TestOutboxSender_SuppressionCheckerErrorFailsOpen — a flaky lookup
// must NOT block legitimate sends. The enqueue should proceed and we
// see the store call (nil store → expected nil-deref panic, which
// confirms we passed the gate). Caught + recovered.
func TestOutboxSender_SuppressionCheckerErrorFailsOpen(t *testing.T) {
	t.Parallel()
	defer func() {
		// Recovery confirms we reached the store call (intended panic)
		// rather than short-circuiting on the suppression error.
		_ = recover()
	}()
	s := NewOutboxSender(nil) // nil store will panic on enqueue
	s.SetSuppressionChecker(suppressionFake{err: errors.New("db down")})

	_ = s.SendInvoice(context.Background(), "tnt_a", "ok@x.com", nil, "n", "inv", 1, "USD", nil, "")
	t.Error("expected the nil-store panic confirming fail-open path")
}

type suppressionFake struct {
	suppressed bool
	reason     string
	err        error
}

func (f suppressionFake) IsSuppressed(_ context.Context, _, _ string) (bool, string, error) {
	return f.suppressed, f.reason, f.err
}
