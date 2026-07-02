package domain

import (
	"strings"
	"testing"
)

// TestClassifyInvoiceAttention_PaymentAnomalyPiercesTerminal locks the
// ADR-068 exception: the payment-anomaly banner fires on PAID and VOIDED
// invoices — exactly the rows the terminal early-return hides, and exactly
// where a duplicate charge lands. Mutation seam: move the anomaly check
// below the terminal return and both terminal cases yield nil.
func TestClassifyInvoiceAttention_PaymentAnomalyPiercesTerminal(t *testing.T) {
	paidDup := Invoice{
		Status: InvoicePaid, PaymentStatus: PaymentSucceeded,
		StripePaymentIntentID:         "pi_A",
		PaymentAnomalyKind:            EventPaymentDuplicateCharge,
		PaymentAnomalyPaymentIntentID: "pi_B",
	}
	att := ClassifyInvoiceAttention(paidDup, AttentionContext{})
	if att == nil {
		t.Fatal("duplicate-charge anomaly on a PAID invoice produced no attention — the terminal early-return swallowed it")
	}
	if att.Reason != AttentionReasonPaymentAnomaly || att.Severity != AttentionSeverityCritical {
		t.Fatalf("attention = %s/%s, want payment_anomaly/critical", att.Reason, att.Severity)
	}
	if !strings.Contains(att.Message, "pi_B") || !strings.Contains(att.Message, "pi_A") {
		t.Errorf("message must name both PaymentIntents: %q", att.Message)
	}

	voidedPay := Invoice{
		Status:                        InvoiceVoided,
		PaymentAnomalyKind:            EventPaymentReceivedOnVoidedInvoice,
		PaymentAnomalyPaymentIntentID: "pi_Z",
	}
	if att := ClassifyInvoiceAttention(voidedPay, AttentionContext{}); att == nil || att.Code != EventPaymentReceivedOnVoidedInvoice {
		t.Fatalf("voided-payment anomaly attention = %+v, want Critical with the per-cause code", att)
	}

	// No anomaly: paid invoices stay banner-free (the early-return contract).
	clean := Invoice{Status: InvoicePaid, PaymentStatus: PaymentSucceeded}
	if att := ClassifyInvoiceAttention(clean, AttentionContext{}); att != nil {
		t.Fatalf("clean paid invoice grew a banner: %+v", att)
	}
}
