package domain

import (
	"testing"
	"time"
)

// helper: minimal draft invoice in healthy state
func draft() Invoice {
	return Invoice{
		ID:            "vlx_inv_test",
		Status:        InvoiceDraft,
		PaymentStatus: PaymentPending,
		TaxStatus:     InvoiceTaxOK,
	}
}

func TestClassifyInvoiceAttention_HealthyReturnsNil(t *testing.T) {
	if got := ClassifyInvoiceAttention(draft()); got != nil {
		t.Fatalf("healthy invoice should return nil, got %+v", got)
	}
}

func TestClassifyInvoiceAttention_TerminalStatesReturnNil(t *testing.T) {
	for _, status := range []InvoiceStatus{InvoicePaid, InvoiceVoided} {
		t.Run(string(status), func(t *testing.T) {
			inv := draft()
			inv.Status = status
			// Even with active failure modes, terminal status must suppress.
			inv.TaxStatus = InvoiceTaxFailed
			inv.TaxErrorCode = "customer_data_invalid"
			inv.PaymentStatus = PaymentFailed
			inv.PaymentOverdue = true
			if got := ClassifyInvoiceAttention(inv); got != nil {
				t.Fatalf("terminal status %s should suppress attention, got %+v", status, got)
			}
		})
	}
}

func TestClassifyInvoiceAttention_TaxFailedSubcodes(t *testing.T) {
	cases := []struct {
		errorCode  string
		wantReason AttentionReason
		wantParam  string
		wantPrimAct AttentionAction
	}{
		{"customer_data_invalid", AttentionReasonTaxLocationRequired, "customer.address.postal_code", AttentionActionEditBillingProfile},
		{"jurisdiction_unsupported", AttentionReasonTaxCalculationFailed, "", AttentionActionReviewRegistration},
		{"provider_outage", AttentionReasonTaxCalculationFailed, "", AttentionActionWaitProvider},
		{"provider_auth", AttentionReasonTaxCalculationFailed, "", AttentionActionRotateAPIKey},
		{"unknown", AttentionReasonTaxCalculationFailed, "", AttentionActionRetryTax},
		{"", AttentionReasonTaxCalculationFailed, "", AttentionActionRetryTax}, // empty falls through to unknown branch
	}
	for _, tc := range cases {
		t.Run(tc.errorCode, func(t *testing.T) {
			inv := draft()
			inv.TaxStatus = InvoiceTaxFailed
			inv.TaxErrorCode = tc.errorCode
			inv.TaxPendingReason = "raw provider response"
			now := time.Now()
			inv.TaxDeferredAt = &now

			att := ClassifyInvoiceAttention(inv)
			if att == nil {
				t.Fatalf("expected attention, got nil")
			}
			if att.Severity != AttentionSeverityCritical {
				t.Errorf("severity = %s, want critical", att.Severity)
			}
			if att.Reason != tc.wantReason {
				t.Errorf("reason = %s, want %s", att.Reason, tc.wantReason)
			}
			if att.Param != tc.wantParam {
				t.Errorf("param = %q, want %q", att.Param, tc.wantParam)
			}
			if len(att.Actions) == 0 {
				t.Fatalf("expected at least one action")
			}
			if att.Actions[0].Code != tc.wantPrimAct {
				t.Errorf("primary action = %s, want %s", att.Actions[0].Code, tc.wantPrimAct)
			}
			if att.DocURL == "" {
				t.Errorf("expected DocURL to be set")
			}
			if att.Detail != "raw provider response" {
				t.Errorf("expected Detail to carry raw provider response")
			}
			if att.Since == nil {
				t.Errorf("expected Since to be set from TaxDeferredAt")
			}
			if want := "tax." + tc.errorCode; tc.errorCode != "" && att.Code != want {
				t.Errorf("code = %q, want %q", att.Code, want)
			}
		})
	}
}

func TestClassifyInvoiceAttention_TaxPendingIsWarning(t *testing.T) {
	inv := draft()
	inv.TaxStatus = InvoiceTaxPending
	inv.TaxErrorCode = "customer_data_invalid"
	att := ClassifyInvoiceAttention(inv)
	if att == nil || att.Severity != AttentionSeverityWarning {
		t.Fatalf("pending should be warning, got %+v", att)
	}
	if att.Reason != AttentionReasonTaxLocationRequired {
		t.Errorf("reason = %s, want %s", att.Reason, AttentionReasonTaxLocationRequired)
	}
}

func TestClassifyInvoiceAttention_PaymentFailed(t *testing.T) {
	inv := draft()
	inv.PaymentStatus = PaymentFailed
	inv.LastPaymentError = "card declined: insufficient funds"
	inv.UpdatedAt = time.Now()

	att := ClassifyInvoiceAttention(inv)
	if att == nil {
		t.Fatalf("expected attention")
	}
	if att.Severity != AttentionSeverityCritical {
		t.Errorf("severity = %s, want critical", att.Severity)
	}
	if att.Reason != AttentionReasonPaymentFailed {
		t.Errorf("reason = %s, want %s", att.Reason, AttentionReasonPaymentFailed)
	}
	if att.Message != "card declined: insufficient funds" {
		t.Errorf("message = %q, want headline from LastPaymentError", att.Message)
	}
	if len(att.Actions) == 0 || att.Actions[0].Code != AttentionActionRetryPayment {
		t.Errorf("expected primary action retry_payment")
	}
}

func TestClassifyInvoiceAttention_PaymentUnknownIsInfo(t *testing.T) {
	inv := draft()
	inv.PaymentStatus = PaymentUnknown
	att := ClassifyInvoiceAttention(inv)
	if att == nil || att.Severity != AttentionSeverityInfo {
		t.Fatalf("payment_unknown should be info, got %+v", att)
	}
	if att.Reason != AttentionReasonPaymentUnconfirmed {
		t.Errorf("reason = %s, want %s", att.Reason, AttentionReasonPaymentUnconfirmed)
	}
}

func TestClassifyInvoiceAttention_OverdueIsWarning(t *testing.T) {
	inv := draft()
	inv.PaymentOverdue = true
	due := time.Now().Add(-48 * time.Hour)
	inv.DueAt = &due

	att := ClassifyInvoiceAttention(inv)
	if att == nil || att.Severity != AttentionSeverityWarning {
		t.Fatalf("overdue should be warning, got %+v", att)
	}
	if att.Reason != AttentionReasonOverdue {
		t.Errorf("reason = %s, want %s", att.Reason, AttentionReasonOverdue)
	}
	if att.Since == nil || !att.Since.Equal(due) {
		t.Errorf("Since should equal DueAt, got %v vs %v", att.Since, due)
	}
}

func TestClassifyInvoiceAttention_PriorityOrder(t *testing.T) {
	// Tax_failed must beat payment_failed must beat tax_pending must beat overdue must beat payment_unknown.
	inv := draft()
	inv.TaxStatus = InvoiceTaxFailed
	inv.TaxErrorCode = "customer_data_invalid"
	inv.PaymentStatus = PaymentFailed // also bad
	inv.PaymentOverdue = true         // also bad

	att := ClassifyInvoiceAttention(inv)
	if att == nil {
		t.Fatalf("expected attention")
	}
	if att.Reason != AttentionReasonTaxLocationRequired {
		t.Errorf("priority broken: tax_failed should win, got %s", att.Reason)
	}

	// Drop tax — payment_failed should now win over overdue + unknown.
	inv.TaxStatus = InvoiceTaxOK
	inv.TaxErrorCode = ""
	att = ClassifyInvoiceAttention(inv)
	if att == nil || att.Reason != AttentionReasonPaymentFailed {
		t.Errorf("priority broken: payment_failed should beat overdue, got %+v", att)
	}

	// Drop payment_failed — overdue should win over payment_unknown.
	inv.PaymentStatus = PaymentUnknown
	att = ClassifyInvoiceAttention(inv)
	if att == nil || att.Reason != AttentionReasonOverdue {
		t.Errorf("priority broken: overdue should beat payment_unknown, got %+v", att)
	}

	// Drop overdue — payment_unknown remains.
	inv.PaymentOverdue = false
	att = ClassifyInvoiceAttention(inv)
	if att == nil || att.Reason != AttentionReasonPaymentUnconfirmed {
		t.Errorf("priority broken: payment_unknown should remain, got %+v", att)
	}
}

func TestClassifyInvoiceAttention_PaymentProcessing(t *testing.T) {
	inv := draft()
	inv.Status = InvoiceFinalized
	inv.PaymentStatus = PaymentProcessing
	inv.UpdatedAt = time.Now()

	att := ClassifyInvoiceAttention(inv)
	if att == nil {
		t.Fatalf("expected attention")
	}
	if att.Severity != AttentionSeverityInfo {
		t.Errorf("severity = %s, want info", att.Severity)
	}
	if att.Reason != AttentionReasonPaymentProcessing {
		t.Errorf("reason = %s, want %s", att.Reason, AttentionReasonPaymentProcessing)
	}
	if len(att.Actions) != 0 {
		t.Errorf("processing should expose no actions (waiting on provider), got %d", len(att.Actions))
	}
}

func TestClassifyInvoiceAttention_PaymentScheduled(t *testing.T) {
	inv := draft()
	inv.Status = InvoiceFinalized
	inv.PaymentStatus = PaymentPending
	inv.AutoChargePending = true
	inv.UpdatedAt = time.Now()

	att := ClassifyInvoiceAttention(inv)
	if att == nil {
		t.Fatalf("expected attention")
	}
	if att.Severity != AttentionSeverityInfo {
		t.Errorf("severity = %s, want info", att.Severity)
	}
	if att.Reason != AttentionReasonPaymentScheduled {
		t.Errorf("reason = %s, want %s", att.Reason, AttentionReasonPaymentScheduled)
	}
	if len(att.Actions) == 0 || att.Actions[0].Code != AttentionActionChargeNow {
		t.Errorf("expected primary action charge_now, got %+v", att.Actions)
	}
}

func TestClassifyInvoiceAttention_AwaitingPayment(t *testing.T) {
	inv := draft()
	inv.Status = InvoiceFinalized
	inv.PaymentStatus = PaymentPending
	// AutoChargePending = false (default) — no scheduler queue, no charge yet.
	inv.UpdatedAt = time.Now()

	att := ClassifyInvoiceAttention(inv)
	if att == nil {
		t.Fatalf("expected attention")
	}
	if att.Severity != AttentionSeverityInfo {
		t.Errorf("severity = %s, want info", att.Severity)
	}
	if att.Reason != AttentionReasonAwaitingPayment {
		t.Errorf("reason = %s, want %s", att.Reason, AttentionReasonAwaitingPayment)
	}
	codes := make(map[AttentionAction]bool)
	for _, a := range att.Actions {
		codes[a.Code] = true
	}
	if !codes[AttentionActionChargeNow] || !codes[AttentionActionSendReminder] {
		t.Errorf("awaiting_payment should offer charge_now + send_reminder, got %+v", att.Actions)
	}
}

func TestClassifyInvoiceAttention_DraftSuppressesAttention(t *testing.T) {
	inv := draft()
	// Status=draft, payment_status=pending — should NOT raise attention
	// (the page itself communicates draft state).
	att := ClassifyInvoiceAttention(inv)
	if att != nil {
		t.Errorf("draft should suppress info attention, got %+v", att)
	}
}

func TestClassifyInvoiceAttention_EmptyPaymentErrorFallsBackToGeneric(t *testing.T) {
	inv := draft()
	inv.PaymentStatus = PaymentFailed
	inv.LastPaymentError = ""
	att := ClassifyInvoiceAttention(inv)
	if att == nil {
		t.Fatalf("expected attention")
	}
	if att.Message == "" {
		t.Errorf("expected non-empty fallback message")
	}
}
