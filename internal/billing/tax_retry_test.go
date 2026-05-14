package billing

import (
	"context"
	"testing"
	"time"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/tax"
)

// TestTaxRetryBackoff verifies the schedule produces strictly
// increasing delays per attempt and stays within ±10% of the
// declared base for each step.
func TestTaxRetryBackoff(t *testing.T) {
	expectedBase := []time.Duration{
		5 * time.Minute,
		15 * time.Minute,
		1 * time.Hour,
		4 * time.Hour,
		12 * time.Hour,
		24 * time.Hour,
		48 * time.Hour,
		96 * time.Hour,
	}
	for i, base := range expectedBase {
		got := taxRetryBackoff(i)
		min := time.Duration(float64(base) * 0.9)
		max := time.Duration(float64(base) * 1.1)
		if got < min || got > max {
			t.Errorf("attempt %d: got %v, want within ±10%% of %v", i, got, base)
		}
	}
}

// TestTaxRetryBackoff_OutOfRangeClamps confirms callers that pass
// an attempts value past the schedule's last index get the final
// (4d ± jitter) bucket — the schedule "saturates" rather than
// returning zero.
func TestTaxRetryBackoff_OutOfRangeClamps(t *testing.T) {
	got := taxRetryBackoff(99)
	min := time.Duration(float64(96*time.Hour) * 0.9)
	if got < min {
		t.Errorf("out-of-range attempt: got %v, want >= %v (clamped to last bucket)", got, min)
	}
}

// TestNextTaxRetry_Outcomes covers the four decision branches:
// success, retryable-under-cap, non-retryable, retryable-at-cap.
func TestNextTaxRetry_Outcomes(t *testing.T) {
	cases := []struct {
		name     string
		status   domain.InvoiceTaxStatus
		errCode  string
		attempts int
		wantNil  bool
	}{
		{"success clears retry", domain.InvoiceTaxOK, "", 3, true},
		{"retryable-under-cap schedules", domain.InvoiceTaxPending, string(tax.ErrCodeProviderOutage), 0, false},
		{"unknown is also retryable", domain.InvoiceTaxPending, string(tax.ErrCodeUnknown), 2, false},
		{"non-retryable code skips", domain.InvoiceTaxFailed, string(tax.ErrCodeProviderAuth), 0, true},
		{"customer-data is non-retryable", domain.InvoiceTaxFailed, string(tax.ErrCodeCustomerDataInvalid), 0, true},
		{"final attempt clears", domain.InvoiceTaxPending, string(tax.ErrCodeProviderOutage), maxTaxRetryAttempts - 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nextTaxRetry(context.Background(), tc.status, tc.errCode, tc.attempts)
			if tc.wantNil && got != nil {
				t.Errorf("expected nil, got %v", *got)
			}
			if !tc.wantNil && got == nil {
				t.Errorf("expected non-nil retry timestamp")
			}
		})
	}
}
