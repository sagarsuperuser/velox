package api

import (
	"errors"
	"fmt"
	"testing"

	"github.com/sagarsuperuser/velox/internal/dunning"
	"github.com/sagarsuperuser/velox/internal/payment"
)

// TestClassifyDunningRetryError locks the dunning-retry outcome mapping:
// a transient (call-never-happened) and an AMBIGUOUS (Stripe 5xx/timeout —
// the PI may have actually succeeded) both skip without burning a dunning
// attempt, while a DEFINITE decline is returned raw so dunning counts it
// and the campaign advances to its terminal. Pre-fix an unknown counted as
// a failure and could exhaust → cancel/write-off a possibly-paid invoice.
func TestClassifyDunningRetryError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantSkip bool
		wantNil  bool
	}{
		{"nil → nil", nil, false, true},
		{"transient → skip", fmt.Errorf("wrap: %w", payment.ErrPaymentTransient), true, false},
		{"unknown outcome → skip", &payment.PaymentError{Unknown: true}, true, false},
		{"definite decline → counted (raw, NOT skip)", &payment.PaymentError{Unknown: false}, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyDunningRetryError(tc.err)
			switch {
			case tc.wantNil:
				if got != nil {
					t.Fatalf("got %v, want nil", got)
				}
			case tc.wantSkip:
				if !errors.Is(got, dunning.ErrTransientSkip) {
					t.Fatalf("got %v, want ErrTransientSkip", got)
				}
			default:
				if got == nil || errors.Is(got, dunning.ErrTransientSkip) {
					t.Fatalf("got %v, want the raw error counted (a definite decline must escalate, not skip)", got)
				}
			}
		})
	}
}
